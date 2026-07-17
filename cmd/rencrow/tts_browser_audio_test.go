package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildLocalTTSAudioURL(t *testing.T) {
	outputDir := t.TempDir()
	audioPath := filepath.Join(outputDir, "sample.wav")
	if err := os.WriteFile(audioPath, []byte("RIFF"), 0o644); err != nil {
		t.Fatalf("write audio file: %v", err)
	}

	got := buildLocalTTSAudioURL(outputDir, audioPath)
	want := "/viewer/tts/audio?path=sample.wav"
	if got != want {
		t.Fatalf("unexpected local audio url: got %q want %q", got, want)
	}
}

func TestBuildLocalTTSAudioURL_RejectsOutsideOutputDir(t *testing.T) {
	outputDir := t.TempDir()
	outsidePath := filepath.Join(t.TempDir(), "sample.wav")
	if err := os.WriteFile(outsidePath, []byte("RIFF"), 0o644); err != nil {
		t.Fatalf("write outside audio file: %v", err)
	}

	if got := buildLocalTTSAudioURL(outputDir, outsidePath); got != "" {
		t.Fatalf("expected empty url for path outside output dir, got %q", got)
	}
}

func TestHandleLocalTTSAudio(t *testing.T) {
	outputDir := t.TempDir()
	audioPath := filepath.Join(outputDir, "chunk.wav")
	if err := os.WriteFile(audioPath, []byte("RIFF"), 0o644); err != nil {
		t.Fatalf("write audio file: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=chunk.wav", nil)
	rec := httptest.NewRecorder()

	handleLocalTTSAudio(outputDir)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if body := rec.Body.String(); body != "RIFF" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestHandleTTSAudio_ProxiesConfiguredRemoteAudioURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/chunk.wav" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFF"))
	}))
	defer upstream.Close()

	req := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?url="+url.QueryEscape(upstream.URL+"/audio/chunk.wav"), nil)
	rec := httptest.NewRecorder()

	handleTTSAudio(t.TempDir(), upstream.URL)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if body := rec.Body.String(); body != "RIFF" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestHandleTTSAudio_RejectsPublicRemoteAudioURL(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?url="+url.QueryEscape("https://example.com/audio.wav"), nil)
	rec := httptest.NewRecorder()

	handleTTSAudio(t.TempDir(), "http://127.0.0.1:7870")(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleTTSAudio_RejectsUnconfiguredPrivateRemoteAudioURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unconfigured private upstream must not be called")
	}))
	defer upstream.Close()

	req := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?url="+url.QueryEscape(upstream.URL+"/audio.wav"), nil)
	rec := httptest.NewRecorder()

	handleTTSAudio(t.TempDir(), "http://127.0.0.1:7870")(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleLocalTTSAudio_RejectsTraversal(t *testing.T) {
	outputDir := t.TempDir()
	req := httptest.NewRequest(http.MethodGet, "/viewer/tts/audio?path=../secret.wav", nil)
	rec := httptest.NewRecorder()

	handleLocalTTSAudio(outputDir)(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
