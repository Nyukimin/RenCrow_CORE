package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHandler_FileTranscribesMultipartWAV(t *testing.T) {
	h := NewHandler(MockProvider{Text: "ルミナ、今日の予定を確認して。"})
	h.Now = func() time.Time { return time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC) }
	req := multipartWAVRequest(t, "/stt/file", tinyWAV())
	rec := httptest.NewRecorder()

	h.File(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out Result
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Text == "" || out.Provider != ProviderMock || out.EventID == "" {
		t.Fatalf("unexpected result: %+v", out)
	}
}

func TestHandler_ChatInputReturnsVoiceUserInput(t *testing.T) {
	h := NewHandler(MockProvider{Text: "ルミナ、RenCrowの状態を確認して。"})
	req := multipartWAVRequest(t, "/stt/chat-input", tinyWAV())
	rec := httptest.NewRecorder()

	h.ChatInput(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["type"] != "user_input" || out["source"] != "local_stt" || out["input_type"] != "voice" {
		t.Fatalf("unexpected chat-input envelope: %+v", out)
	}
	if out["text"] == "" || out["event_id"] == "" {
		t.Fatalf("missing text/event_id: %+v", out)
	}
}

func TestHandler_InvalidAudioReturnsJSONError(t *testing.T) {
	h := NewHandler(MockProvider{Text: "ignored"})
	req := multipartWAVRequest(t, "/stt/file", []byte("not wav"))
	rec := httptest.NewRecorder()

	h.File(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out Result
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ErrorCode != ErrorInvalidAudio {
		t.Fatalf("unexpected error result: %+v", out)
	}
}

func multipartWAVRequest(t *testing.T, path string, wav []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "audio.wav")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(wav); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func tinyWAV() []byte {
	dataSize := 2
	out := make([]byte, 44+dataSize)
	copy(out[0:4], "RIFF")
	out[4] = byte(36 + dataSize)
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	out[16] = 16
	out[20] = 1
	out[22] = 1
	out[24] = 0x80
	out[25] = 0x3e
	out[28] = 0x00
	out[29] = 0x7d
	out[32] = 2
	out[34] = 16
	copy(out[36:40], "data")
	out[40] = byte(dataSize)
	return out
}

func TestQueuedProviderQueueLatestSupersedesPendingRequest(t *testing.T) {
	base := &blockingProvider{release: make(chan struct{})}
	provider := NewBusyPolicyProvider(base, BusyPolicyQueueLatest)

	firstDone := make(chan error, 1)
	go func() {
		_, err := provider.Transcribe(context.Background(), namedWAV("first"))
		firstDone <- err
	}()
	base.waitCalls(t, 1)

	secondDone := make(chan error, 1)
	go func() {
		_, err := provider.Transcribe(context.Background(), namedWAV("second"))
		secondDone <- err
	}()
	time.Sleep(50 * time.Millisecond)

	thirdDone := make(chan Result, 1)
	go func() {
		result, err := provider.Transcribe(context.Background(), namedWAV("third"))
		if err != nil {
			t.Errorf("third Transcribe returned error: %v", err)
			return
		}
		thirdDone <- result
	}()

	select {
	case err := <-secondDone:
		var sttErr *Error
		if !errors.As(err, &sttErr) || sttErr.Code != ErrorProviderBusy {
			t.Fatalf("second error = %v, want %s", err, ErrorProviderBusy)
		}
	case <-time.After(time.Second):
		t.Fatal("second request was not superseded")
	}

	close(base.release)

	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first Transcribe error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first request did not finish")
	}
	select {
	case result := <-thirdDone:
		if result.Text != "third" {
			t.Fatalf("third result text = %q", result.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("third request did not finish")
	}
}

func TestNewProviderSupportsOpenAIAPIEngineName(t *testing.T) {
	provider := NewProvider(Config{Enabled: true, Provider: ProviderOpenAIAPI, ExternalHTTPURL: "http://127.0.0.1:8766/v1/audio/transcriptions"})
	if provider.Name() != ProviderOpenAIAPI {
		t.Fatalf("provider.Name() = %q, want %q", provider.Name(), ProviderOpenAIAPI)
	}
}

type blockingProvider struct {
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

func (p *blockingProvider) Name() string { return "blocking" }

func (p *blockingProvider) Health(context.Context) Health {
	return Health{Status: "ok", Provider: p.Name(), Ready: true}
}

func (p *blockingProvider) Transcribe(_ context.Context, wav []byte) (Result, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	<-p.release
	return Result{Text: strings.Trim(strings.TrimSpace(string(wav[44:])), "\x00")}, nil
}

func (p *blockingProvider) waitCalls(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		got := p.calls
		p.mu.Unlock()
		if got >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("provider calls did not reach %d", want)
}

func namedWAV(name string) []byte {
	wav := tinyWAV()
	return append(wav, []byte(name)...)
}
