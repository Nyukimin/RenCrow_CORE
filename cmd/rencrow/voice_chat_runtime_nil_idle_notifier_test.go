package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	modulevoicechat "github.com/Nyukimin/RenCrow_CORE/modules/voicechat"
	"golang.org/x/net/websocket"
)

// TestBuildVoiceChatRuntimeTypedNilIdleChatOrchestratorServesSessionReady は
// idle_chat 無効時に buildVoiceChatRuntime へ *idlechat.IdleChatOrchestrator の
// typed nil が渡された場合、/voice-chat の session.start が
// NotifyActivity の nil 参照 panic で閉じられる回帰（2026-09-25 本番 panic）を
// 本番同一経路で検出する。
func TestBuildVoiceChatRuntimeTypedNilIdleChatOrchestratorServesSessionReady(t *testing.T) {
	t.Setenv("VOICE_CHAT_ENABLED", "true")
	t.Setenv("VOICE_CHAT_GATEWAY_URL", "")
	t.Setenv("RENCROW_LLM_CHAT_WS", "")
	t.Setenv("VOICE_INPUT_MODE", "")

	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"user_text\":\"\",\"reply\":\"ok\"}"}}]}`))
	}))
	defer llm.Close()

	cfg := &config.Config{LLMGateway: config.LLMGatewayConfig{
		Enabled: true,
		BaseURL: llm.URL,
	}}
	var typedNil *idlechat.IdleChatOrchestrator
	rt := buildVoiceChatRuntime(cfg, nil, typedNil, voiceChatExecutionOwners{})
	if !rt.Enabled {
		t.Fatal("expected voice chat runtime enabled for this probe config")
	}

	mux := http.NewServeMux()
	registerVoiceChatRoutes(mux, rt.WSHandler)
	bridge := httptest.NewServer(mux)
	defer bridge.Close()

	conn, err := websocket.Dial("ws"+strings.TrimPrefix(bridge.URL, "http")+modulevoicechat.RoutePathPrimary, "", "http://localhost/")
	if err != nil {
		t.Fatalf("dial voice-chat websocket: %v", err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}

	startFrame := `{"type":"session.start","utterance_id":"utt-nil-notifier","sample_rate":16000,"channels":1,"format":"pcm16le","channel":"viewer"}`
	if err := websocket.Message.Send(conn, startFrame); err != nil {
		t.Fatalf("send session.start: %v", err)
	}
	var ready string
	if err := websocket.Message.Receive(conn, &ready); err != nil {
		t.Fatalf("receive session.ready (typed-nil idle notifier panics the bridge): %v", err)
	}
	if !strings.Contains(ready, `"type":"session.ready"`) {
		t.Fatalf("unexpected ready event: %s", ready)
	}
}
