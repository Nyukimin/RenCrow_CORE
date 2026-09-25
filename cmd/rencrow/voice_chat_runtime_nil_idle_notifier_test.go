package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	"golang.org/x/net/websocket"
)

// TestVoiceChatRuntimeTypedNilIdleChatOrchestratorServesSessionReady is the regression test
// for the production panic observed on 2026-09-25 on the live /voice-chat route: with
// idle_chat disabled, cmd/rencrow/main.go passed a typed-nil *idlechat.IdleChatOrchestrator
// into buildVoiceChatRuntime, the orchestrator.IdleNotifier interface became non-nil, and
// session.start panicked in IdleChatOrchestrator.Interrupt (nil receiver mutex lock),
// closing the websocket before session.ready was sent.
func TestVoiceChatRuntimeTypedNilIdleChatOrchestratorServesSessionReady(t *testing.T) {
	t.Setenv("VOICE_CHAT_ENABLED", "true")
	t.Setenv("VOICE_CHAT_GATEWAY_URL", "")
	t.Setenv("RENCROW_LLM_CHAT_WS", "")

	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("LLM gateway must not be called for session.start, got %s", r.URL.Path)
	}))
	defer llm.Close()

	cfg := &config.Config{LLMGateway: config.LLMGatewayConfig{
		Enabled: true,
		BaseURL: llm.URL,
	}}

	var typedNilOrchestrator *idlechat.IdleChatOrchestrator
	rt := buildVoiceChatRuntime(cfg, nil, typedNilOrchestrator, voiceChatExecutionOwners{})
	if !rt.Enabled {
		t.Fatal("expected voice chat runtime enabled for the bridge regression path")
	}

	mux := http.NewServeMux()
	registerVoiceChatRoutes(mux, rt.WSHandler)
	bridge := httptest.NewServer(mux)
	defer bridge.Close()

	for _, path := range []string{"/voice-chat", "/voice-chat-ws"} {
		t.Run("path="+path, func(t *testing.T) {
			conn, err := websocket.Dial("ws"+strings.TrimPrefix(bridge.URL, "http")+path, "", "http://localhost/")
			if err != nil {
				t.Fatalf("dial bridge websocket: %v", err)
			}
			defer conn.Close()
			if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
				t.Fatalf("set read deadline: %v", err)
			}
			if err := websocket.Message.Send(conn, `{"type":"session.start","utterance_id":"utt-step17","sample_rate":16000,"channels":1,"format":"pcm16le","channel":"viewer"}`); err != nil {
				t.Fatalf("send start: %v", err)
			}
			var ready string
			if err := websocket.Message.Receive(conn, &ready); err != nil {
				t.Fatalf("receive ready: %v (typed-nil idle chat orchestrator must not panic the bridge)", err)
			}
			if !strings.Contains(ready, `"type":"session.ready"`) {
				t.Fatalf("unexpected ready event: %s", ready)
			}
		})
	}
}
