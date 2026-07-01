package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	sttinfra "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/stt"
	modulestt "github.com/Nyukimin/picoclaw_multiLLM/modules/stt"
	"golang.org/x/net/websocket"
)

func TestInferSTTGatewayURL_PrioritizesExplicitGateway(t *testing.T) {
	got := inferSTTGatewayURL(" ws://192.168.1.36:8090/stt ", "ws://192.168.1.36:8090/stt-ws")
	want := "ws://192.168.1.36:8090/stt"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestInferSTTGatewayURL_FallsBackToRencrowSTTURL(t *testing.T) {
	got := inferSTTGatewayURL("", " ws://192.168.1.36:8090/stt ")
	want := "ws://192.168.1.36:8090/stt"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestInferSTTGatewayURL_EmptyWhenBothUnset(t *testing.T) {
	got := inferSTTGatewayURL(" ", " ")
	if got != "" {
		t.Fatalf("expected empty gateway url, got %q", got)
	}
}

func TestRegisterSTTRoutes_RegistersPrimaryAndCompatiblePaths(t *testing.T) {
	mux := http.NewServeMux()
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	registerSTTRoutes(mux, handler)

	for _, path := range []string{"/stt", "/stt-ws", "/ws"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("path %s expected %d, got %d", path, http.StatusNoContent, rec.Code)
		}
	}
}

func TestInferSTTProviderURLFromConfig_UsesGoSTTFileByDefault(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = 8443
	cfg.Server.TLS.Enabled = true
	cfg.STT.Provider = "external_http"

	got := inferSTTProviderURLFromConfig(cfg)
	want := "https://127.0.0.1:8443/stt/file"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestInferSTTProviderURLFromConfig_UsesExternalProviderCompatibility(t *testing.T) {
	cfg := &config.Config{}
	cfg.STT.Provider = "external_http"
	cfg.STT.ProviderURL = "http://127.0.0.1:8080/inference"

	got := inferSTTProviderURLFromConfig(cfg)
	if got != cfg.STT.ProviderURL {
		t.Fatalf("expected external provider url, got %q", got)
	}
}

func TestSTTStreamURLFromConfig_InfersRealtimeEndpointFromProviderURL(t *testing.T) {
	cfg := &config.Config{}
	cfg.STT.ProviderURL = "http://192.168.1.33:8766/v1/audio/transcriptions"

	got := sttStreamURLFromConfig(cfg)
	want := "ws://192.168.1.33:8766/ws/transcribe"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestSTTStreamURLFromConfig_UsesExplicitStreamURL(t *testing.T) {
	cfg := &config.Config{}
	cfg.STT.ProviderURL = "http://192.168.1.33:8766/v1/audio/transcriptions"
	cfg.STT.StreamURL = "wss://stt.local/ws/transcribe"

	got := sttStreamURLFromConfig(cfg)
	if got != cfg.STT.StreamURL {
		t.Fatalf("expected explicit stream url, got %q", got)
	}
}

func TestIsSTTTextFramePayload(t *testing.T) {
	if !isSTTTextFramePayload([]byte(`{"type":"ready"}`)) {
		t.Fatal("json object should be relayed as text")
	}
	if !isSTTTextFramePayload([]byte(`"final_pending"`)) {
		t.Fatal("json string should be relayed as text")
	}
	if isSTTTextFramePayload(rawPCM16Chunk()) {
		t.Fatal("pcm16 audio should be relayed as binary")
	}
	if isSTTTextFramePayload([]byte{0xff, 0x00, 0x01}) {
		t.Fatal("non-json bytes should be relayed as binary")
	}
}

func TestSTTWebSocketBridgeE2E_RelaysStartStopTextAndPCM16Binary(t *testing.T) {
	pcm := rawPCM16Chunk()
	gatewayDone := make(chan error, 1)
	gateway := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		var start string
		if err := websocket.Message.Receive(conn, &start); err != nil {
			gatewayDone <- err
			return
		}
		if !strings.Contains(start, `"type":"start"`) || !strings.Contains(start, `"format":"pcm_s16le"`) {
			gatewayDone <- fmt.Errorf("unexpected start control: %s", start)
			return
		}

		var gotPCM []byte
		if err := websocket.Message.Receive(conn, &gotPCM); err != nil {
			gatewayDone <- err
			return
		}
		if string(gotPCM) != string(pcm) {
			gatewayDone <- fmt.Errorf("unexpected pcm chunk: got %d bytes", len(gotPCM))
			return
		}

		var stop string
		if err := websocket.Message.Receive(conn, &stop); err != nil {
			gatewayDone <- err
			return
		}
		if strings.TrimSpace(stop) != `{"type":"stop"}` {
			gatewayDone <- fmt.Errorf("unexpected stop control: %s", stop)
			return
		}
		if err := websocket.Message.Send(conn, `{"type":"final","text":"テスト"}`); err != nil {
			gatewayDone <- err
			return
		}
		gatewayDone <- nil
	}))
	defer gateway.Close()

	mux := http.NewServeMux()
	gatewayURL := "ws" + strings.TrimPrefix(gateway.URL, "http")
	registerSTTRoutes(mux, handleSTTWebSocketBridge(gatewayURL, ""))
	bridge := httptest.NewServer(mux)
	defer bridge.Close()

	conn, err := websocket.Dial("ws"+strings.TrimPrefix(bridge.URL, "http")+"/stt", "", "http://localhost/")
	if err != nil {
		t.Fatalf("dial bridge websocket: %v", err)
	}
	defer conn.Close()

	if err := websocket.Message.Send(conn, `{"type":"start","sample_rate":16000,"channels":1,"format":"pcm_s16le"}`); err != nil {
		t.Fatalf("send start: %v", err)
	}
	if err := websocket.Message.Send(conn, pcm); err != nil {
		t.Fatalf("send pcm: %v", err)
	}
	if err := websocket.Message.Send(conn, `{"type":"stop"}`); err != nil {
		t.Fatalf("send stop: %v", err)
	}

	var final string
	if err := websocket.Message.Receive(conn, &final); err != nil {
		t.Fatalf("receive final: %v", err)
	}
	if !strings.Contains(final, `"type":"final"`) || !strings.Contains(final, `"text":"テスト"`) {
		t.Fatalf("unexpected final event: %s", final)
	}
	if err := <-gatewayDone; err != nil {
		t.Fatalf("gateway relay: %v", err)
	}
}

func TestSTTWebSocketBridgeE2E_ConvertsGatewayFinalNoiseToErrorWithoutHTTPFallback(t *testing.T) {
	pcm := rawPCM16Chunk()
	gatewayDone := make(chan error, 1)
	gateway := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		var start string
		if err := websocket.Message.Receive(conn, &start); err != nil {
			gatewayDone <- err
			return
		}
		if !strings.Contains(start, `"type":"start"`) {
			gatewayDone <- fmt.Errorf("unexpected start control: %s", start)
			return
		}
		var gotPCM []byte
		if err := websocket.Message.Receive(conn, &gotPCM); err != nil {
			gatewayDone <- err
			return
		}
		if string(gotPCM) != string(pcm) {
			gatewayDone <- fmt.Errorf("unexpected pcm chunk: got %d bytes", len(gotPCM))
			return
		}
		var stop string
		if err := websocket.Message.Receive(conn, &stop); err != nil {
			gatewayDone <- err
			return
		}
		if strings.TrimSpace(stop) != `{"type":"stop"}` {
			gatewayDone <- fmt.Errorf("unexpected stop control: %s", stop)
			return
		}
		if err := websocket.Message.Send(conn, `{"type":"final","text":"<|channel>thought\n<channel|>"}`); err != nil {
			gatewayDone <- err
			return
		}
		gatewayDone <- nil
	}))
	defer gateway.Close()

	providerCalled := make(chan struct{}, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalled <- struct{}{}
		if err := r.ParseMultipartForm(16 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if _, _, err := r.FormFile("file"); err != nil {
			t.Fatalf("file missing: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "切手"})
	}))
	defer provider.Close()

	mux := http.NewServeMux()
	gatewayURL := "ws" + strings.TrimPrefix(gateway.URL, "http")
	registerSTTRoutes(mux, handleSTTWebSocketBridge(gatewayURL, provider.URL))
	bridge := httptest.NewServer(mux)
	defer bridge.Close()

	conn, err := websocket.Dial("ws"+strings.TrimPrefix(bridge.URL, "http")+"/stt", "", "http://localhost/")
	if err != nil {
		t.Fatalf("dial bridge websocket: %v", err)
	}
	defer conn.Close()

	if err := websocket.Message.Send(conn, `{"type":"start","sample_rate":16000,"channels":1,"format":"pcm_s16le"}`); err != nil {
		t.Fatalf("send start: %v", err)
	}
	if err := websocket.Message.Send(conn, pcm); err != nil {
		t.Fatalf("send pcm: %v", err)
	}
	if err := websocket.Message.Send(conn, `{"type":"stop"}`); err != nil {
		t.Fatalf("send stop: %v", err)
	}

	var final string
	if err := websocket.Message.Receive(conn, &final); err != nil {
		t.Fatalf("receive event: %v", err)
	}
	if !strings.Contains(final, `"type":"error"`) || !strings.Contains(final, modulestt.ProviderTranscriptErrorMessage) {
		t.Fatalf("unexpected bridge error event: %s", final)
	}
	select {
	case <-providerCalled:
		t.Fatal("bridge should not call HTTP fallback for gateway final noise")
	default:
	}
	if err := <-gatewayDone; err != nil {
		t.Fatalf("gateway relay: %v", err)
	}
}

func TestSTTWebSocketBridgeE2E_WaitsForRenCrowSTTFinalAfterStop(t *testing.T) {
	gatewayDone := make(chan error, 1)
	gateway := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		var start string
		if err := websocket.Message.Receive(conn, &start); err != nil {
			gatewayDone <- err
			return
		}
		if !strings.Contains(start, `"type":"start"`) {
			gatewayDone <- fmt.Errorf("unexpected start control: %s", start)
			return
		}
		var gotPCM []byte
		if err := websocket.Message.Receive(conn, &gotPCM); err != nil {
			gatewayDone <- err
			return
		}
		var stop string
		if err := websocket.Message.Receive(conn, &stop); err != nil {
			gatewayDone <- err
			return
		}
		if strings.TrimSpace(stop) != `{"type":"stop"}` {
			gatewayDone <- fmt.Errorf("unexpected stop control: %s", stop)
			return
		}
		time.Sleep(100 * time.Millisecond)
		if err := websocket.Message.Send(conn, `{"type":"final","text":"RenCrow_STT final","reason":"stop"}`); err != nil {
			gatewayDone <- err
			return
		}
		gatewayDone <- nil
	}))
	defer gateway.Close()

	providerCalled := make(chan struct{}, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalled <- struct{}{}
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "bridge fallback should not be used"})
	}))
	defer provider.Close()

	mux := http.NewServeMux()
	gatewayURL := "ws" + strings.TrimPrefix(gateway.URL, "http")
	registerSTTRoutes(mux, handleSTTWebSocketBridge(gatewayURL, provider.URL))
	bridge := httptest.NewServer(mux)
	defer bridge.Close()

	conn, err := websocket.Dial("ws"+strings.TrimPrefix(bridge.URL, "http")+"/stt", "", "http://localhost/")
	if err != nil {
		t.Fatalf("dial bridge websocket: %v", err)
	}
	defer conn.Close()

	if err := websocket.Message.Send(conn, `{"type":"start","sample_rate":16000,"channels":1,"format":"pcm_s16le"}`); err != nil {
		t.Fatalf("send start: %v", err)
	}
	if err := websocket.Message.Send(conn, rawPCM16Chunk()); err != nil {
		t.Fatalf("send pcm: %v", err)
	}
	if err := websocket.Message.Send(conn, `{"type":"stop"}`); err != nil {
		t.Fatalf("send stop: %v", err)
	}

	start := time.Now()
	var final string
	if err := websocket.Message.Receive(conn, &final); err != nil {
		t.Fatalf("receive final: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("final took too long: %s", time.Since(start))
	}
	if !strings.Contains(final, `"type":"final"`) || !strings.Contains(final, `"text":"RenCrow_STT final"`) {
		t.Fatalf("unexpected final event: %s", final)
	}
	select {
	case <-providerCalled:
		t.Fatal("bridge should not use HTTP fallback while waiting for RenCrow_STT final")
	default:
	}
	if err := <-gatewayDone; err != nil {
		t.Fatalf("gateway relay: %v", err)
	}
}

func TestSTTWebSocketBridgeE2E_DoesNotPromoteCachedPartialOnStop(t *testing.T) {
	pcm := rawPCM16Chunk()
	gatewayDone := make(chan error, 1)
	gateway := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		var start string
		if err := websocket.Message.Receive(conn, &start); err != nil {
			gatewayDone <- err
			return
		}
		if !strings.Contains(start, `"type":"start"`) {
			gatewayDone <- fmt.Errorf("unexpected start control: %s", start)
			return
		}
		var gotPCM []byte
		if err := websocket.Message.Receive(conn, &gotPCM); err != nil {
			gatewayDone <- err
			return
		}
		if string(gotPCM) != string(pcm) {
			gatewayDone <- fmt.Errorf("unexpected pcm chunk: got %d bytes", len(gotPCM))
			return
		}
		if err := websocket.Message.Send(conn, `{"type":"partial","text":"And so"}`); err != nil {
			gatewayDone <- err
			return
		}
		var stop string
		if err := websocket.Message.Receive(conn, &stop); err != nil {
			gatewayDone <- err
			return
		}
		if strings.TrimSpace(stop) != `{"type":"stop"}` {
			gatewayDone <- fmt.Errorf("unexpected stop control: %s", stop)
			return
		}
		time.Sleep(100 * time.Millisecond)
		if err := websocket.Message.Send(conn, `{"type":"final","text":"RenCrow_STT owns final","reason":"stop"}`); err != nil {
			gatewayDone <- err
			return
		}
		gatewayDone <- nil
	}))
	defer gateway.Close()

	providerCalled := make(chan struct{}, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalled <- struct{}{}
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "HTTP fallback should not be used"})
	}))
	defer provider.Close()

	mux := http.NewServeMux()
	gatewayURL := "ws" + strings.TrimPrefix(gateway.URL, "http")
	registerSTTRoutes(mux, handleSTTWebSocketBridge(gatewayURL, provider.URL))
	bridge := httptest.NewServer(mux)
	defer bridge.Close()

	conn, err := websocket.Dial("ws"+strings.TrimPrefix(bridge.URL, "http")+"/stt", "", "http://localhost/")
	if err != nil {
		t.Fatalf("dial bridge websocket: %v", err)
	}
	defer conn.Close()

	if err := websocket.Message.Send(conn, `{"type":"start","sample_rate":16000,"channels":1,"format":"pcm_s16le"}`); err != nil {
		t.Fatalf("send start: %v", err)
	}
	if err := websocket.Message.Send(conn, pcm); err != nil {
		t.Fatalf("send pcm: %v", err)
	}

	var partial string
	if err := websocket.Message.Receive(conn, &partial); err != nil {
		t.Fatalf("receive partial: %v", err)
	}
	if !strings.Contains(partial, `"type":"partial"`) || !strings.Contains(partial, `"text":"And so"`) {
		t.Fatalf("unexpected partial event: %s", partial)
	}

	if err := websocket.Message.Send(conn, `{"type":"stop"}`); err != nil {
		t.Fatalf("send stop: %v", err)
	}

	start := time.Now()
	var final string
	if err := websocket.Message.Receive(conn, &final); err != nil {
		t.Fatalf("receive final: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("final took too long: %s", time.Since(start))
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(final), &ev); err != nil {
		t.Fatalf("decode final: %v", err)
	}
	if ev["type"] != "final" || ev["text"] != "RenCrow_STT owns final" {
		t.Fatalf("unexpected final event: %+v", ev)
	}
	if ev["stt_fallback_required"] == true || ev["source"] == "cached_partial" || ev["fallback_reason"] == "partial_fast_path" {
		t.Fatalf("bridge should not emit provisional final metadata, got %+v", ev)
	}
	select {
	case <-providerCalled:
		t.Fatal("bridge should not call HTTP fallback when RenCrow_STT owns finalization")
	default:
	}
	if err := <-gatewayDone; err != nil {
		t.Fatalf("gateway relay: %v", err)
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
