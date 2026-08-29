package tts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestGatewayProviderSynthesizesAndDownloadsThroughGatewayRelay(t *testing.T) {
	var synthesisCalls int
	var audioCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tts":
			synthesisCalls++
			if got := r.Header.Get("Authorization"); got != "Bearer owner-test-token" {
				t.Fatalf("Authorization = %q, want owner Bearer token", got)
			}
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode synthesis payload: %v", err)
			}
			if payload["text"] != "テストです。" || payload["voice_id"] != "mio" {
				t.Fatalf("synthesis payload = %#v", payload)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"gateway_service": "tts-gateway",
				"voice_id":        "mio",
				"target_id":       "target-a",
				"audio_path":      "/audio/target-a/token",
				"audio_url":       "http://physical-target.invalid/audio/file.wav",
			})
		case "/audio/target-a/token":
			audioCalls++
			w.Header().Set("Content-Type", "audio/wav")
			_, _ = io.WriteString(w, "RIFF-test-wav")
		case "/health/ready":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"gateway_service": "tts-gateway",
				"status":          "ready",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := NewGatewayProvider(GatewayProviderConfig{
		BaseURL:   server.URL,
		AuthToken: "owner-test-token",
		VoiceID:   "mio",
		Speed:     1.2,
		Timeout:   time.Second,
	})
	out, err := provider.Synthesize(context.Background(), SynthesisInput{
		Text:       "テストです。",
		OutputDir:  t.TempDir(),
		FilePrefix: "gateway-test",
		VoiceProfile: VoiceProfile{
			VoiceID: "mio",
		},
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if synthesisCalls != 1 || audioCalls != 1 {
		t.Fatalf("calls synthesis=%d audio=%d", synthesisCalls, audioCalls)
	}
	if out.Provider != "tts-gateway" || out.VoiceID != "mio" {
		t.Fatalf("output = %+v", out)
	}
	if _, err := os.Stat(out.AudioFilePath); err != nil {
		t.Fatalf("downloaded WAV: %v", err)
	}
	if health := provider.Health(context.Background()); !health.Ready || health.Metadata["provider"] != "tts-gateway" {
		t.Fatalf("health = %+v", health)
	}
}

func TestResolveGatewayRelayURLRejectsTargetURL(t *testing.T) {
	if _, err := resolveGatewayRelayURL("http://gateway.example:7870", "http://target.example/audio/file.wav"); err == nil {
		t.Fatal("expected physical target URL to be rejected")
	}
}
