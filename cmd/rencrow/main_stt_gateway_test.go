package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	sttinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/stt"
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

// The three tests below are the route-level E2E of /stt: a client talks to the
// route CORE registers, a stand-in for RenCrow_STT's stream answers, and the
// stand-in fails the test if CORE calls its HTTP transcription route.

func TestSTTRelayE2E_ReturnsFinal(t *testing.T) {
	fake := startFakeSTTServer(t)
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, nil, nil)))
	conn := fake.Accept()
	client.ExpectReady()

	client.Control(map[string]any{"type": "start", "utterance_id": "v1", "sample_rate": 16000, "channels": 1, "format": "pcm_s16le"})
	client.Audio(rawPCM16Chunk())
	client.Control(map[string]any{"type": "stop"})
	conn.NextControl(t)
	conn.Next(t)
	conn.NextControl(t)
	conn.Send(`{"type":"speech_start","utterance_id":"v1","offset_ms":0}`)
	conn.Send(`{"type":"final","utterance_id":"v1","text":"ルミナ、今日の予定を確認して。","end_reason":"stop","audio_ms":100,"target_called":true}`)

	client.Expect("speech_start")
	final := client.Expect("final")
	if final["text"] != "ルミナ、今日の予定を確認して。" || final["utterance_id"] != "v1" {
		t.Fatalf("final = %#v", final)
	}
}

func TestSTTRelayE2E_SendsSessionInfoAndReadyOnOpen(t *testing.T) {
	fake := startFakeSTTServer(t)
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, nil, nil)))
	fake.Accept()

	info := client.Expect("session_info")
	if _, ok := info["session_id"]; ok {
		t.Fatalf("session_info must not expose a synthetic conversation session: %+v", info)
	}
	if ready := client.Expect("ready"); ready["protocol"] != "utterance.v1" {
		t.Fatalf("ready = %+v", ready)
	}
}

func TestSTTRelayE2E_AcceptsRawPCM16Chunks(t *testing.T) {
	fake := startFakeSTTServer(t)
	client := dialRelay(t, serveRelay(t, realStreamConfig(fake, nil, nil)))
	conn := fake.Accept()
	client.ExpectReady()

	for i := 0; i < 4; i++ {
		client.Audio(rawPCM16Chunk())
	}
	client.Text(`{"type":"final_pending"}`)
	for i := 0; i < 4; i++ {
		if frame := conn.Next(t); frame.Kind != sttinfra.FrameBinary || len(frame.Data) != len(rawPCM16Chunk()) {
			t.Fatalf("chunk %d reached STT as kind %d with %d bytes", i, frame.Kind, len(frame.Data))
		}
	}
	if stop := conn.NextControl(t); stop["type"] != "utterance.stop" {
		t.Fatalf("final_pending must reach STT as utterance.stop: %+v", stop)
	}
}

func rawPCM16Chunk() []byte {
	out := make([]byte, 3200)
	for i := 0; i+1 < len(out); i += 2 {
		out[i] = 0x10
		out[i+1] = 0x01
	}
	return out
}
