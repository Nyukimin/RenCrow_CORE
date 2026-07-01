package comfyui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientGenerateImageQueuesPollsAndBuildsViewURL(t *testing.T) {
	var gotPrompt map[string]any
	pollCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/prompt":
			if err := json.NewDecoder(r.Body).Decode(&gotPrompt); err != nil {
				t.Fatalf("decode prompt body: %v", err)
			}
			_, _ = w.Write([]byte(`{"prompt_id":"pid-1","number":1,"node_errors":{}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/history/pid-1":
			pollCount++
			if pollCount == 1 {
				_, _ = w.Write([]byte(`{}`))
				return
			}
			_, _ = w.Write([]byte(`{"pid-1":{"outputs":{"10":{"images":[{"filename":"out.png","subfolder":"","type":"output"}]}}}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:      server.URL,
		ClientID:     "test-client",
		PollInterval: time.Millisecond,
		Timeout:      time.Second,
	})

	result, err := client.GenerateImageRequest(context.Background(), GenerateRequest{
		Prompt:         "cat portrait",
		Seed:           123,
		Width:          768,
		Height:         768,
		FilenamePrefix: "rencrow_test",
	})
	if err != nil {
		t.Fatalf("GenerateImage failed: %v", err)
	}
	if result.PromptID != "pid-1" {
		t.Fatalf("prompt id = %q", result.PromptID)
	}
	if result.ImageURL != server.URL+"/view?filename=out.png&type=output" {
		t.Fatalf("image url = %q", result.ImageURL)
	}
	if pollCount != 2 {
		t.Fatalf("poll count = %d", pollCount)
	}
	if gotPrompt["client_id"] != "test-client" {
		t.Fatalf("client_id = %#v", gotPrompt["client_id"])
	}
	body := mustJSON(t, gotPrompt)
	for _, want := range []string{"z_image_turbo_nvfp4.safetensors", "qwen_3_4b_fp8_mixed.safetensors", "AWPortrait-Z.safetensors", "REALSTAGRAM_ZIMG.safetensors", "cat portrait"} {
		if !strings.Contains(body, want) {
			t.Fatalf("prompt body missing %q:\n%s", want, body)
		}
	}
}

func TestClientGenerateImageRejectsInvalidRequest(t *testing.T) {
	client := NewClient(Config{BaseURL: "http://comfy.local"})
	if _, err := client.GenerateImageRequest(context.Background(), GenerateRequest{Prompt: strings.Repeat("x", MaxPromptRunes+1)}); err == nil {
		t.Fatal("expected prompt length error")
	}
	if _, err := client.GenerateImageRequest(context.Background(), GenerateRequest{Prompt: "ok", Width: 769, Height: 768}); err == nil {
		t.Fatal("expected width validation error")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
