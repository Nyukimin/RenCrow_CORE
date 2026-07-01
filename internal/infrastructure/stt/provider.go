package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

var eventSeq atomic.Uint64

func NextEventID(now time.Time) string {
	n := eventSeq.Add(1)
	return fmt.Sprintf("evt_stt_%s_%06d", now.Format("20060102"), n)
}

func IsWAV(b []byte) bool {
	return len(b) >= 44 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WAVE"
}

type MockProvider struct {
	Text string
	Err  error
}

func (p MockProvider) Name() string {
	return ProviderMock
}

func (p MockProvider) Health(_ context.Context) Health {
	return Health{Status: "ok", Provider: ProviderMock, Model: "mock", Device: "test", Ready: p.Err == nil}
}

func (p MockProvider) Transcribe(_ context.Context, wav []byte) (Result, error) {
	if p.Err != nil {
		return Result{}, p.Err
	}
	if !IsWAV(wav) {
		return Result{}, NewError(ErrorInvalidAudio, "invalid wav", nil)
	}
	text := strings.TrimSpace(p.Text)
	if text == "" {
		return Result{Text: "", Language: "ja", ErrorCode: ErrorNoSpeechDetected, Message: "音声が検出されませんでした。"}, nil
	}
	return Result{
		Text:     text,
		Language: "ja",
		Duration: 1.0,
		Segments: []Segment{{Start: 0, End: 1.0, Text: text}},
	}, nil
}

type HTTPProvider struct {
	URL      string
	Timeout  time.Duration
	Provider string
	Model    string
	Language string
}

func (p HTTPProvider) Name() string {
	if strings.TrimSpace(p.Provider) != "" {
		return strings.TrimSpace(p.Provider)
	}
	return ProviderExternalHTTP
}

func (p HTTPProvider) Health(_ context.Context) Health {
	return Health{Status: "ok", Provider: p.Name(), Model: strings.TrimSpace(p.Model), Device: "external_http", Ready: strings.TrimSpace(p.URL) != ""}
}

func (p HTTPProvider) Transcribe(ctx context.Context, wav []byte) (Result, error) {
	if !IsWAV(wav) {
		return Result{}, NewError(ErrorInvalidAudio, "invalid wav", nil)
	}
	if strings.TrimSpace(p.URL) == "" {
		return Result{}, NewError(ErrorProviderFailure, "stt provider url is not configured", nil)
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "audio.wav")
	if err != nil {
		return Result{}, err
	}
	if _, err := part.Write(wav); err != nil {
		return Result{}, err
	}
	_ = w.WriteField("response_format", "json")
	if err := w.Close(); err != nil {
		return Result{}, err
	}

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(p.URL), &body)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return Result{}, NewError(ErrorProviderFailure, "provider request failed", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, NewError(ErrorProviderFailure, fmt.Sprintf("provider status=%d", resp.StatusCode), fmt.Errorf("%s", strings.TrimSpace(string(respBody))))
	}
	var out Result
	if err := json.Unmarshal(respBody, &out); err != nil {
		return Result{}, fmt.Errorf("decode provider response: %w", err)
	}
	if strings.TrimSpace(out.Language) == "" {
		out.Language = fallback(p.Language, "ja")
	}
	if strings.TrimSpace(out.Text) == "" {
		out.ErrorCode = ErrorNoSpeechDetected
		out.Message = "音声が検出されませんでした。"
	}
	return out, nil
}

func NewProvider(cfg Config) Provider {
	cfg = cfg.WithDefaults()
	var provider Provider
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case ProviderMock:
		provider = MockProvider{Text: "ルミナ、今日の予定を確認して。"}
	case ProviderOpenAIAPI:
		provider = HTTPProvider{URL: cfg.ExternalHTTPURL, Timeout: cfg.Timeout, Provider: ProviderOpenAIAPI, Model: cfg.Model, Language: cfg.Language}
	case ProviderExternalHTTP:
		provider = HTTPProvider{URL: cfg.ExternalHTTPURL, Timeout: cfg.Timeout, Provider: ProviderExternalHTTP, Model: cfg.Model, Language: cfg.Language}
	default:
		provider = HTTPProvider{URL: cfg.ExternalHTTPURL, Timeout: cfg.Timeout, Provider: ProviderExternalHTTP, Model: cfg.Model, Language: cfg.Language}
	}
	return NewBusyPolicyProvider(provider, cfg.BusyPolicy)
}

func fallback(v, d string) string {
	if strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return d
}
