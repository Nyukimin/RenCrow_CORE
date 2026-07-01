package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	modulestt "github.com/Nyukimin/picoclaw_multiLLM/modules/stt"
)

var sttSharedHTTPClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
	},
}

func sttInferViaHTTP(providerURL string, wav []byte, timeout time.Duration) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	part, err := w.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(wav); err != nil {
		return "", err
	}
	if err := w.WriteField("response_format", "json"); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := sttSharedHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return out.Text, nil
}

func adjustAdaptiveSTTTimeout(cur, delta, minV, maxV time.Duration) time.Duration {
	return modulestt.AdjustAdaptiveTimeout(cur, delta, minV, maxV)
}

func sttHTTPTimeoutFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("STT_TIMEOUT_MS"))
	if raw == "" {
		return modulestt.DefaultHTTPTimeout
	}
	ms, err := strconv.Atoi(raw)
	if err != nil {
		return modulestt.DefaultHTTPTimeout
	}
	return modulestt.HTTPTimeoutFromMilliseconds(ms)
}

func isSTTTimeoutErr(err error) bool {
	return modulestt.IsTimeoutError(err)
}
