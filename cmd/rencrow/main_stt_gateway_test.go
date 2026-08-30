package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	sttinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/stt"
	"golang.org/x/net/websocket"
)

func TestRegisterSTTRoutes_RegistersPrimaryAndCompatiblePaths(t *testing.T) {
	mux := http.NewServeMux()
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	registerSTTRoutes(mux, handler)

	for _, path := range []string{"/stt"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("path %s expected %d, got %d", path, http.StatusNoContent, rec.Code)
		}
	}
}

func TestInferSTTGatewayHTTPURLFromConfig_UsesRenCrowSTTGateway(t *testing.T) {
	cfg := &config.Config{}
	cfg.STT.GatewayBaseURL = "http://192.168.1.33:8766/"

	got := inferSTTGatewayHTTPURLFromConfig(cfg)
	want := "http://192.168.1.33:8766/v1/audio/transcriptions"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestInferSTTGatewayHTTPURLFromConfig_UsesDefaultGateway(t *testing.T) {
	cfg := &config.Config{}

	got := inferSTTGatewayHTTPURLFromConfig(cfg)
	want := "http://127.0.0.1:8766/v1/audio/transcriptions"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestSTTWebSocketProviderE2E_ReturnsFinal(t *testing.T) {
	mux := http.NewServeMux()
	registerSTTRoutes(mux, handleSTTWebSocketProvider(sttinfra.MockProvider{Text: "ルミナ、今日の予定を確認して。"}))
	server := httptest.NewServer(mux)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/stt"
	conn, err := websocket.Dial(wsURL, "", "http://localhost/")
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	if err := websocket.Message.Send(conn, `{"type":"config","mimeType":"audio/wav"}`); err != nil {
		t.Fatalf("send config: %v", err)
	}
	if err := websocket.Message.Send(conn, tinyTestWAV()); err != nil {
		t.Fatalf("send wav: %v", err)
	}
	if err := websocket.Message.Send(conn, `{"type":"final_pending"}`); err != nil {
		t.Fatalf("send final_pending: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	seenDraft := false
	for time.Now().Before(deadline) {
		var raw string
		if err := websocket.Message.Receive(conn, &raw); err != nil {
			t.Fatalf("receive: %v", err)
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			t.Fatalf("decode event %q: %v", raw, err)
		}
		if ev["type"] == "draft" {
			seenDraft = true
		}
		if ev["type"] == "final" {
			if !seenDraft {
				t.Fatal("final arrived before draft")
			}
			if strings.TrimSpace(ev["text"].(string)) == "" {
				t.Fatalf("empty final event: %+v", ev)
			}
			return
		}
	}
	t.Fatal("timed out waiting for final")
}

func TestSTTWebSocketProviderE2E_SendsSessionReadyOnOpen(t *testing.T) {
	mux := http.NewServeMux()
	registerSTTRoutes(mux, handleSTTWebSocketProvider(sttinfra.MockProvider{Text: "ルミナ、今日の予定を確認して。"}))
	server := httptest.NewServer(mux)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/stt"
	conn, err := websocket.Dial(wsURL, "", "http://localhost/")
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	want := []string{"session_info", "ready"}
	for _, wantType := range want {
		var raw string
		if err := websocket.Message.Receive(conn, &raw); err != nil {
			t.Fatalf("receive %s: %v", wantType, err)
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			t.Fatalf("decode event %q: %v", raw, err)
		}
		if ev["type"] != wantType {
			t.Fatalf("expected %s, got %+v", wantType, ev)
		}
	}
}

func TestSTTWebSocketProviderE2E_AcceptsRawPCM16Chunks(t *testing.T) {
	mux := http.NewServeMux()
	registerSTTRoutes(mux, handleSTTWebSocketProvider(sttinfra.MockProvider{Text: "ルミナ、今日の予定を確認して。"}))
	server := httptest.NewServer(mux)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/stt"
	conn, err := websocket.Dial(wsURL, "", "http://localhost/")
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	if err := websocket.Message.Send(conn, rawPCM16Chunk()); err != nil {
		t.Fatalf("send raw pcm: %v", err)
	}
	if err := websocket.Message.Send(conn, `{"type":"final_pending"}`); err != nil {
		t.Fatalf("send final_pending: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var raw string
		if err := websocket.Message.Receive(conn, &raw); err != nil {
			t.Fatalf("receive: %v", err)
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			t.Fatalf("decode event %q: %v", raw, err)
		}
		if ev["type"] == "final" {
			if strings.TrimSpace(ev["text"].(string)) == "" {
				t.Fatalf("empty final event: %+v", ev)
			}
			return
		}
	}
	t.Fatal("timed out waiting for final")
}

func tinyTestWAV() []byte {
	dataSize := 32000
	out := make([]byte, 44+dataSize)
	copy(out[0:4], "RIFF")
	size := uint32(36 + dataSize)
	out[4] = byte(size)
	out[5] = byte(size >> 8)
	out[6] = byte(size >> 16)
	out[7] = byte(size >> 24)
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
	ds := uint32(dataSize)
	out[40] = byte(ds)
	out[41] = byte(ds >> 8)
	out[42] = byte(ds >> 16)
	out[43] = byte(ds >> 24)
	for i := 44; i+1 < len(out); i += 2 {
		out[i] = 0x10
		out[i+1] = 0x01
	}
	return out
}

func rawPCM16Chunk() []byte {
	out := make([]byte, 3200)
	for i := 0; i+1 < len(out); i += 2 {
		out[i] = 0x10
		out[i+1] = 0x01
	}
	return out
}
