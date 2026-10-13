package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	transportpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/transport"
	"golang.org/x/net/websocket"
)

func TestBuildSTTRuntimeWebSocketUsesRenCrowSTTHTTPContract(t *testing.T) {
	const frames = 10 // one second of Viewer-sized 100 ms frames
	requestSeen := make(chan struct{}, frames)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/transcriptions" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// CORE -> RenCrow_STT contract: file (audio.wav) + response_format=json only.
		if r.MultipartForm.Value["response_format"] == nil || r.MultipartForm.Value["response_format"][0] != "json" ||
			len(r.MultipartForm.Value) != 1 || len(r.MultipartForm.File) != 1 || r.Header.Get("X-RenCrow-Request-ID") == "" {
			http.Error(w, "CORE changed the RenCrow_STT request contract", http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("file")
		if err == nil && header.Filename != "audio.wav" {
			err = io.ErrUnexpectedEOF
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		payload, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(payload) < 44 || !bytes.Equal(payload[:4], []byte("RIFF")) || !bytes.Equal(payload[8:12], []byte("WAVE")) {
			http.Error(w, "CORE did not normalize Viewer PCM to WAV", http.StatusBadRequest)
			return
		}
		// One request carries the whole utterance as 16 kHz mono PCM16.
		if payload[22] != 1 || payload[24] != 0x80 || payload[25] != 0x3e || payload[34] != 16 ||
			len(payload) != 44+frames*len(rawPCM16Chunk()) {
			http.Error(w, "CORE did not send the whole utterance as one 16 kHz mono PCM16 WAV", http.StatusBadRequest)
			return
		}
		requestSeen <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"text":          "音声経路OK",
			"language":      "ja",
			"provider":      "test-rencrow-stt",
			"duration":      1.0,
			"processing_ms": 10,
		})
	}))
	defer gateway.Close()

	cfg := &config.Config{}
	cfg.STT.Enabled = true
	cfg.STT.GatewayBaseURL = gateway.URL
	cfg.STT.TimeoutMS = 5000
	cfg.STT.BusyPolicy = "queue_latest"
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	actions := actionmanager.New(actionStore)
	transportStore, err := transportpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "transport"))
	if err != nil {
		t.Fatal(err)
	}
	transports := transportmanager.New(transportStore, actions)
	runtime := buildSTTRuntime(
		cfg,
		taskmanager.New(taskStore, taskmanager.DefaultParallelLimits()),
		actions,
		transports,
	)

	mux := http.NewServeMux()
	registerSTTRuntimeRoutes(mux, runtime)
	server := httptest.NewServer(mux)
	defer server.Close()

	conn, err := websocket.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/stt", "", "http://localhost/")
	if err != nil {
		t.Fatalf("dial CORE stt websocket: %v", err)
	}
	defer conn.Close()

	if err := websocket.Message.Send(conn, `{"type":"start","sample_rate":16000,"channels":1,"format":"pcm_s16le"}`); err != nil {
		t.Fatalf("send start: %v", err)
	}
	for i := 0; i < frames; i++ {
		if err := websocket.Message.Send(conn, rawPCM16Chunk()); err != nil {
			t.Fatalf("send raw pcm: %v", err)
		}
	}
	if err := websocket.Message.Send(conn, `{"type":"stop"}`); err != nil {
		t.Fatalf("send stop: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		var raw string
		if err := websocket.Message.Receive(conn, &raw); err != nil {
			t.Fatalf("receive CORE stt event: %v", err)
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatalf("decode CORE stt event %q: %v", raw, err)
		}
		if event["type"] != "final" {
			continue
		}
		if event["text"] != "音声経路OK" {
			t.Fatalf("unexpected final event: %+v", event)
		}
		select {
		case <-requestSeen:
			if len(requestSeen) != 0 {
				t.Fatalf("RenCrow_STT was called %d times for one utterance, want 1", len(requestSeen)+1)
			}
			requests, err := transports.ListRequests(context.Background(), domaintransport.RequestFilter{})
			if err != nil {
				t.Fatal(err)
			}
			responses, err := transports.ListResponses(context.Background(), domaintransport.ResponseFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if len(requests) != 1 || len(responses) != 1 || responses[0].RequestID != requests[0].RequestID {
				t.Fatalf("STT transport receipts requests=%#v responses=%#v", requests, responses)
			}
			return
		default:
			t.Fatal("RenCrow_STT HTTP transcription contract was not called")
		}
	}
	t.Fatal("timed out waiting for final STT event")
}
