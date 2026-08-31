package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulevoicechat "github.com/Nyukimin/RenCrow_CORE/modules/voicechat"
	"golang.org/x/net/websocket"
)

func float64Ptr(v float64) *float64 { return &v }
func intPtr(v int) *int             { return &v }

type slowVoiceDirectHandler struct {
	delay time.Duration
	done  chan struct{}
}

func (h *slowVoiceDirectHandler) ProcessVoiceDirect(context.Context, orchestrator.ProcessVoiceDirectRequest) (orchestrator.ProcessMessageResponse, error) {
	time.Sleep(h.delay)
	if h.done != nil {
		close(h.done)
	}
	return orchestrator.ProcessMessageResponse{}, nil
}

func (h *slowVoiceDirectHandler) NotifyVoiceDirectFirstToken(context.Context, orchestrator.ProcessVoiceDirectRequest, task.JobID, time.Time) {
}

type inputAudioVoiceDirectHandler struct {
	mu          sync.Mutex
	response    orchestrator.ProcessMessageResponse
	err         error
	requests    []orchestrator.ProcessVoiceDirectRequest
	notifyCalls int
	started     chan struct{}
	release     chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
}

func (h *inputAudioVoiceDirectHandler) ProcessVoiceDirect(_ context.Context, req orchestrator.ProcessVoiceDirectRequest) (orchestrator.ProcessMessageResponse, error) {
	h.mu.Lock()
	h.requests = append(h.requests, req)
	h.startOnce.Do(func() {
		if h.started != nil {
			close(h.started)
		}
	})
	response, err, release := h.response, h.err, h.release
	h.mu.Unlock()
	if release != nil {
		<-release
	}
	return response, err
}

func (h *inputAudioVoiceDirectHandler) NotifyVoiceDirectFirstToken(context.Context, orchestrator.ProcessVoiceDirectRequest, task.JobID, time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifyCalls++
}

func (h *inputAudioVoiceDirectHandler) snapshot() ([]orchestrator.ProcessVoiceDirectRequest, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]orchestrator.ProcessVoiceDirectRequest(nil), h.requests...), h.notifyCalls
}

func (h *inputAudioVoiceDirectHandler) unblock() {
	if h == nil || h.release == nil {
		return
	}
	h.releaseOnce.Do(func() { close(h.release) })
}

func validInputAudioVoiceDirectResponse(text string) orchestrator.ProcessMessageResponse {
	return orchestrator.ProcessMessageResponse{
		Response:  text,
		JobID:     task.NewJobID().String(),
		TraceID:   string(modulecore.NewTraceID()),
		MessageID: string(modulecore.NewMessageID()),
	}
}

type recordingVoiceChatIdleNotifier struct {
	activities int
	chatBusy   []bool
}

func (n *recordingVoiceChatIdleNotifier) NotifyActivity() {
	n.activities++
}

func (n *recordingVoiceChatIdleNotifier) SetChatBusy(busy bool) {
	n.chatBusy = append(n.chatBusy, busy)
}

func (n *recordingVoiceChatIdleNotifier) SetWorkerBusy(bool) {
}

func TestInferVoiceChatGatewayURLUsesCanonicalLLMGateway(t *testing.T) {
	t.Setenv("VOICE_CHAT_GATEWAY_URL", " ws://192.168.1.207:8081/v1/chat/audio/sessions ")
	t.Setenv("RENCROW_LLM_CHAT_WS", "ws://192.168.1.31:8081/v1/chat/audio/sessions")
	cfg := &config.Config{LLMGateway: config.LLMGatewayConfig{
		Enabled: true,
		BaseURL: "http://127.0.0.1:8090",
	}}
	got := inferVoiceChatGatewayURL(cfg)
	want := "ws://127.0.0.1:8090/v1/chat/audio/sessions"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestSplitVoiceChatStructuredFinalKeepsUserTextForServer(t *testing.T) {
	msg := []byte(`{"type":"llm.final","utterance_id":"utt-1","text":"{\"user_text\":\"Mioさんいますか\",\"reply\":\"はい、います。\"}"}`)
	updated, transcript := splitVoiceChatStructuredFinal(msg)
	if transcript != "Mioさんいますか" {
		t.Fatalf("unexpected transcript: %q", transcript)
	}
	var ev map[string]any
	if err := json.Unmarshal(updated, &ev); err != nil {
		t.Fatalf("unmarshal updated final: %v", err)
	}
	if ev["text"] != "はい、います。" {
		t.Fatalf("expected llm.final text to be reply, got %#v", ev["text"])
	}
	if ev["user_text"] != "Mioさんいますか" {
		t.Fatalf("expected internal user_text hint, got %#v", ev["user_text"])
	}
}

func TestInferVoiceChatGatewayURLRequiresEnabledValidCanonicalGateway(t *testing.T) {
	t.Setenv("VOICE_CHAT_GATEWAY_URL", "ws://192.168.1.207:8081/v1/chat/audio/sessions")
	t.Setenv("RENCROW_LLM_CHAT_WS", "ws://192.168.1.31:8081/v1/chat/audio/sessions")
	for _, test := range []struct {
		name string
		cfg  *config.Config
	}{
		{name: "nil config"},
		{name: "disabled", cfg: &config.Config{LLMGateway: config.LLMGatewayConfig{
			BaseURL: "http://127.0.0.1:8090",
		}}},
		{name: "empty base url", cfg: &config.Config{LLMGateway: config.LLMGatewayConfig{
			Enabled: true,
		}}},
		{name: "invalid base url", cfg: &config.Config{LLMGateway: config.LLMGatewayConfig{
			Enabled: true,
			BaseURL: "not-a-url",
		}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := inferVoiceChatGatewayURL(test.cfg); got != "" {
				t.Fatalf("expected empty gateway URL, got %q", got)
			}
		})
	}
}

func TestVoiceChatEnabledFromEnv_DefaultFalse(t *testing.T) {
	t.Setenv("VOICE_CHAT_ENABLED", "")
	if voiceChatEnabledFromEnv() {
		t.Fatal("expected voice chat disabled by default")
	}
}

func TestVoiceChatWebSocketHandshake_AllowsTailscaleServeWithoutOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/voice-chat", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "fujitsu-ubunts.tailb07d8d.ts.net")
	cfg := &websocket.Config{Version: websocket.ProtocolVersionHybi13}

	server, ok := voiceChatWebSocketHandler(nil).(websocket.Server)
	if !ok {
		t.Fatal("expected voice chat handler to use websocket.Server")
	}
	if err := server.Handshake(cfg, req); err != nil {
		t.Fatalf("handshake rejected no-origin tailscale serve request: %v", err)
	}
	if cfg.Origin != nil {
		t.Fatalf("expected nil origin to be accepted, got %v", cfg.Origin)
	}
}

func TestRegisterVoiceChatRoutes_RegistersPrimaryAndAliasPaths(t *testing.T) {
	mux := http.NewServeMux()
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	registerVoiceChatRoutes(mux, handler)
	for _, path := range modulevoicechat.WebSocketRoutePaths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("path %s expected %d, got %d", path, http.StatusNoContent, rec.Code)
		}
	}
}

func TestVoiceChatWebSocketBridgeE2E_RelaysStartPCMCommitAndFinalWithoutClosing(t *testing.T) {
	pcm := rawPCM16Chunk()
	gatewayDone := make(chan error, 1)
	gateway := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		var start string
		if err := websocket.Message.Receive(conn, &start); err != nil {
			gatewayDone <- err
			return
		}
		if !strings.Contains(start, `"type":"session.start"`) || !strings.Contains(start, `"format":"pcm16le"`) {
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
		var commit string
		if err := websocket.Message.Receive(conn, &commit); err != nil {
			gatewayDone <- err
			return
		}
		if !strings.Contains(commit, `"type":"session.commit"`) {
			gatewayDone <- fmt.Errorf("unexpected commit control: %s", commit)
			return
		}
		if err := websocket.Message.Send(conn, `{"type":"session.ready","utterance_id":"utt-1","session_id":"sess-1"}`); err != nil {
			gatewayDone <- err
			return
		}
		if err := websocket.Message.Send(conn, `{"type":"llm.delta","utterance_id":"utt-1","session_id":"sess-1","seq":1,"text":"お"}`); err != nil {
			gatewayDone <- err
			return
		}
		if err := websocket.Message.Send(conn, `{"type":"llm.final","utterance_id":"utt-1","session_id":"sess-1","text":"おはよう"}`); err != nil {
			gatewayDone <- err
			return
		}
		gatewayDone <- nil
	}))
	defer gateway.Close()

	mux := http.NewServeMux()
	gatewayURL := "ws" + strings.TrimPrefix(gateway.URL, "http")
	registerVoiceChatRoutes(mux, handleVoiceChatWebSocketBridge(gatewayURL, nil, nil))
	bridge := httptest.NewServer(mux)
	defer bridge.Close()

	conn, err := websocket.Dial("ws"+strings.TrimPrefix(bridge.URL, "http")+modulevoicechat.RoutePathPrimary, "", "http://localhost/")
	if err != nil {
		t.Fatalf("dial bridge websocket: %v", err)
	}
	defer conn.Close()

	if err := websocket.Message.Send(conn, `{"type":"session.start","utterance_id":"utt-1","sample_rate":16000,"channels":1,"format":"pcm16le","model":"Chat"}`); err != nil {
		t.Fatalf("send start: %v", err)
	}
	if err := websocket.Message.Send(conn, pcm); err != nil {
		t.Fatalf("send pcm: %v", err)
	}
	if err := websocket.Message.Send(conn, `{"type":"session.commit","utterance_id":"utt-1"}`); err != nil {
		t.Fatalf("send commit: %v", err)
	}

	var ready string
	if err := websocket.Message.Receive(conn, &ready); err != nil {
		t.Fatalf("receive ready: %v", err)
	}
	if !strings.Contains(ready, `"type":"session.ready"`) {
		t.Fatalf("unexpected ready event: %s", ready)
	}
	var delta string
	if err := websocket.Message.Receive(conn, &delta); err != nil {
		t.Fatalf("receive delta: %v", err)
	}
	if !strings.Contains(delta, `"type":"llm.delta"`) || !strings.Contains(delta, `"text":"お"`) {
		t.Fatalf("unexpected delta event: %s", delta)
	}
	var final string
	if err := websocket.Message.Receive(conn, &final); err != nil {
		t.Fatalf("receive final: %v", err)
	}
	if !strings.Contains(final, `"type":"llm.final"`) || !strings.Contains(final, `"text":"おはよう"`) {
		t.Fatalf("unexpected final event: %s", final)
	}
	if err := <-gatewayDone; err != nil {
		t.Fatalf("gateway relay: %v", err)
	}
}

func TestVoiceChatWebSocketBridgeE2E_FinalRelayDoesNotWaitForVoiceDirectProcessing(t *testing.T) {
	gatewayDone := make(chan error, 1)
	gateway := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		var start string
		if err := websocket.Message.Receive(conn, &start); err != nil {
			gatewayDone <- err
			return
		}
		var commit string
		if err := websocket.Message.Receive(conn, &commit); err != nil {
			gatewayDone <- err
			return
		}
		if err := websocket.Message.Send(conn, `{"type":"session.ready","utterance_id":"utt-1","session_id":"sess-1"}`); err != nil {
			gatewayDone <- err
			return
		}
		if err := websocket.Message.Send(conn, `{"type":"llm.final","utterance_id":"utt-1","session_id":"sess-1","text":"おはよう"}`); err != nil {
			gatewayDone <- err
			return
		}
		gatewayDone <- nil
	}))
	defer gateway.Close()

	handlerDone := make(chan struct{})
	voiceDirect := &slowVoiceDirectHandler{delay: 500 * time.Millisecond, done: handlerDone}
	mux := http.NewServeMux()
	gatewayURL := "ws" + strings.TrimPrefix(gateway.URL, "http")
	registerVoiceChatRoutes(mux, handleVoiceChatWebSocketBridge(gatewayURL, voiceDirect, nil))
	bridge := httptest.NewServer(mux)
	defer bridge.Close()

	conn, err := websocket.Dial("ws"+strings.TrimPrefix(bridge.URL, "http")+modulevoicechat.RoutePathPrimary, "", "http://localhost/")
	if err != nil {
		t.Fatalf("dial bridge websocket: %v", err)
	}
	defer conn.Close()

	if err := websocket.Message.Send(conn, `{"type":"session.start","utterance_id":"utt-1","sample_rate":16000,"channels":1,"format":"pcm16le","model":"Chat"}`); err != nil {
		t.Fatalf("send start: %v", err)
	}
	if err := websocket.Message.Send(conn, `{"type":"session.commit","utterance_id":"utt-1"}`); err != nil {
		t.Fatalf("send commit: %v", err)
	}
	var ready string
	if err := websocket.Message.Receive(conn, &ready); err != nil {
		t.Fatalf("receive ready: %v", err)
	}

	started := time.Now()
	var final string
	if err := websocket.Message.Receive(conn, &final); err != nil {
		t.Fatalf("receive final: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 300*time.Millisecond {
		t.Fatalf("llm.final relay waited for ProcessVoiceDirect: elapsed=%s final=%s", elapsed, final)
	}
	if !strings.Contains(final, `"type":"llm.final"`) || !strings.Contains(final, `"text":"おはよう"`) {
		t.Fatalf("unexpected final event: %s", final)
	}
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("expected ProcessVoiceDirect to run asynchronously")
	}
	if err := <-gatewayDone; err != nil {
		t.Fatalf("gateway relay: %v", err)
	}
}

func TestVoiceChatInputAudioBridgeE2E_PostsWAVAndReturnsFinal(t *testing.T) {
	pcm := rawPCM16Chunk()
	const rawLLMText = `{"user_text":"Mioさんいますか","reply":"Gatewayのraw応答"}`
	const publishedText = "Owner確定応答"
	voiceDirect := &inputAudioVoiceDirectHandler{response: validInputAudioVoiceDirectResponse(publishedText)}
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload["model"] != "mio-gemma-4-12b-it" || payload["max_tokens"] != float64(256) || payload["temperature"] != 0.3 {
			t.Fatalf("unexpected generation payload: %#v", payload)
		}
		if payload["top_p"] != 0.9 || payload["top_k"] != float64(40) || payload["min_p"] != 0.0 {
			t.Fatalf("unexpected sampling payload: %#v", payload)
		}
		kwargs, _ := payload["chat_template_kwargs"].(map[string]any)
		if payload["think"] != false || kwargs["enable_thinking"] != false {
			t.Fatalf("voice CHAT must force enable_thinking=false: %#v", payload)
		}
		responseFormat, _ := payload["response_format"].(map[string]any)
		if responseFormat["type"] != "json_object" {
			t.Fatalf("input_audio must request JSON object response format: %#v", payload["response_format"])
		}
		rawMessages, _ := payload["messages"].([]any)
		if len(rawMessages) != 2 {
			t.Fatalf("messages = %#v", payload["messages"])
		}
		systemMessage, _ := rawMessages[0].(map[string]any)
		if systemMessage["role"] != "system" {
			t.Fatalf("first message must be the fixed CORE system contract: %#v", systemMessage)
		}
		systemPrompt, _ := systemMessage["content"].(string)
		for _, required := range []string{"exactly one JSON object", `"user_text"`, "audio transcript", `"reply"`, "existing client/config prompt"} {
			if !strings.Contains(systemPrompt, required) {
				t.Fatalf("fixed input_audio system contract missing %q: %q", required, systemPrompt)
			}
		}
		if strings.Contains(systemPrompt, "短く確認") {
			t.Fatalf("client prompt must remain a user message, not enter the fixed system contract: %q", systemPrompt)
		}
		msg, _ := rawMessages[1].(map[string]any)
		if msg["role"] != "user" {
			t.Fatalf("second message must preserve the user audio prompt: %#v", msg)
		}
		content, _ := msg["content"].([]any)
		if len(content) != 2 {
			t.Fatalf("content = %#v", msg["content"])
		}
		audioPart, _ := content[0].(map[string]any)
		inputAudio, _ := audioPart["input_audio"].(map[string]any)
		data, _ := inputAudio["data"].(string)
		if data == "" {
			t.Fatal("missing input_audio data")
		}
		textPart, _ := content[1].(map[string]any)
		if textPart["type"] != "text" || textPart["text"] != "短く確認" {
			t.Fatalf("client prompt must remain the user text part: %#v", textPart)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"user_text\":\"Mioさんいますか\",\"reply\":\"Gatewayのraw応答\"}"}}]}`))
	}))
	defer llm.Close()

	mux := http.NewServeMux()
	registerVoiceChatRoutes(mux, handleVoiceChatInputAudioBridge("ws"+strings.TrimPrefix(llm.URL, "http")+"/v1/chat/audio/sessions", voiceChatInputAudioSettings{
		Model:       "mio-gemma-4-12b-it",
		MaxTokens:   256,
		Temperature: 0.3,
		TopP:        float64Ptr(0.9),
		TopK:        intPtr(40),
		MinP:        float64Ptr(0.0),
	}, voiceDirect, nil))
	bridge := httptest.NewServer(mux)
	defer bridge.Close()

	conn, err := websocket.Dial("ws"+strings.TrimPrefix(bridge.URL, "http")+modulevoicechat.RoutePathPrimary, "", "http://localhost/")
	if err != nil {
		t.Fatalf("dial bridge websocket: %v", err)
	}
	defer conn.Close()

	if err := websocket.Message.Send(conn, `{"type":"session.start","utterance_id":"utt-1","sample_rate":16000,"channels":1,"format":"pcm16le","channel":"viewer","prompt":"短く確認"}`); err != nil {
		t.Fatalf("send start: %v", err)
	}
	var ready string
	if err := websocket.Message.Receive(conn, &ready); err != nil {
		t.Fatalf("receive ready: %v", err)
	}
	if !strings.Contains(ready, `"type":"session.ready"`) {
		t.Fatalf("unexpected ready event: %s", ready)
	}
	if err := websocket.Message.Send(conn, pcm); err != nil {
		t.Fatalf("send pcm: %v", err)
	}
	if err := websocket.Message.Send(conn, `{"type":"session.commit","utterance_id":"utt-1"}`); err != nil {
		t.Fatalf("send commit: %v", err)
	}
	var delta string
	if err := websocket.Message.Receive(conn, &delta); err != nil {
		t.Fatalf("receive delta: %v", err)
	}
	var deltaEvent map[string]any
	if err := json.Unmarshal([]byte(delta), &deltaEvent); err != nil {
		t.Fatalf("decode delta event: %v", err)
	}
	if deltaEvent["type"] != modulevoicechat.EventLLMDelta || deltaEvent["text"] != publishedText {
		t.Fatalf("unexpected delta event: %s", delta)
	}
	var final string
	if err := websocket.Message.Receive(conn, &final); err != nil {
		t.Fatalf("receive final: %v", err)
	}
	var finalEvent map[string]any
	if err := json.Unmarshal([]byte(final), &finalEvent); err != nil {
		t.Fatalf("decode final event: %v", err)
	}
	if finalEvent["type"] != modulevoicechat.EventLLMFinal || finalEvent["text"] != publishedText {
		t.Fatalf("unexpected final event: %s", final)
	}
	requests, notifyCalls := voiceDirect.snapshot()
	if len(requests) != 1 {
		t.Fatalf("expected one ProcessVoiceDirect request, got %d", len(requests))
	}
	if notifyCalls != 0 {
		t.Fatalf("input_audio must not notify a separate first-token job, got %d calls", notifyCalls)
	}
	if requests[0].FirstTokenAt.IsZero() {
		t.Fatal("input_audio ProcessVoiceDirect request must carry FirstTokenAt")
	}
	if requests[0].FinalText != rawLLMText {
		t.Fatalf("ProcessVoiceDirect must receive raw LLM text, got %q", requests[0].FinalText)
	}
	for _, event := range []map[string]any{deltaEvent, finalEvent} {
		if event["trace_id"] != voiceDirect.response.TraceID || event["job_id"] != voiceDirect.response.JobID || event["message_id"] != voiceDirect.response.MessageID {
			t.Fatalf("event identity does not match ProcessVoiceDirect response: event=%#v response=%+v", event, voiceDirect.response)
		}
	}
}

func TestVoiceChatInputAudioBridge_FinalizesBeforePublishingLLMEvents(t *testing.T) {
	pcm := rawPCM16Chunk()
	voiceDirect := &inputAudioVoiceDirectHandler{
		response: validInputAudioVoiceDirectResponse("音声を確認しました"),
		started:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	defer voiceDirect.unblock()
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"音声を確認しました"}}]}`))
	}))
	defer llm.Close()

	mux := http.NewServeMux()
	registerVoiceChatRoutes(mux, handleVoiceChatInputAudioBridge("ws"+strings.TrimPrefix(llm.URL, "http")+"/v1/chat/audio/sessions", voiceChatInputAudioSettings{}, voiceDirect, nil))
	bridge := httptest.NewServer(mux)
	defer bridge.Close()

	conn, err := websocket.Dial("ws"+strings.TrimPrefix(bridge.URL, "http")+modulevoicechat.RoutePathPrimary, "", "http://localhost/")
	if err != nil {
		t.Fatalf("dial bridge websocket: %v", err)
	}
	defer conn.Close()

	if err := websocket.Message.Send(conn, `{"type":"session.start","utterance_id":"utt-1","sample_rate":16000,"channels":1,"format":"pcm16le","channel":"viewer"}`); err != nil {
		t.Fatalf("send start: %v", err)
	}
	var ready string
	if err := websocket.Message.Receive(conn, &ready); err != nil {
		t.Fatalf("receive ready: %v", err)
	}
	if err := websocket.Message.Send(conn, pcm); err != nil {
		t.Fatalf("send pcm: %v", err)
	}
	if err := websocket.Message.Send(conn, `{"type":"session.commit","utterance_id":"utt-1"}`); err != nil {
		t.Fatalf("send commit: %v", err)
	}

	firstEvent := make(chan string, 1)
	go func() {
		var event string
		if err := websocket.Message.Receive(conn, &event); err != nil {
			firstEvent <- "receive error: " + err.Error()
			return
		}
		firstEvent <- event
	}()
	select {
	case event := <-firstEvent:
		t.Fatalf("input_audio published %s before ProcessVoiceDirect completed", event)
	case <-voiceDirect.started:
	case <-time.After(time.Second):
		t.Fatal("ProcessVoiceDirect did not start")
	}
	select {
	case event := <-firstEvent:
		t.Fatalf("input_audio published event while ProcessVoiceDirect was blocked: %s", event)
	case <-time.After(100 * time.Millisecond):
	}

	voiceDirect.unblock()
	var delta string
	select {
	case delta = <-firstEvent:
	case <-time.After(time.Second):
		t.Fatal("llm.delta was not published after ProcessVoiceDirect completed")
	}
	var deltaEvent map[string]any
	if err := json.Unmarshal([]byte(delta), &deltaEvent); err != nil {
		t.Fatalf("decode delta event: %v", err)
	}
	if deltaEvent["type"] != modulevoicechat.EventLLMDelta {
		t.Fatalf("first event = %#v, want llm.delta", deltaEvent)
	}
	var final string
	if err := websocket.Message.Receive(conn, &final); err != nil {
		t.Fatalf("receive final: %v", err)
	}
	var finalEvent map[string]any
	if err := json.Unmarshal([]byte(final), &finalEvent); err != nil {
		t.Fatalf("decode final event: %v", err)
	}
	if finalEvent["type"] != modulevoicechat.EventLLMFinal {
		t.Fatalf("final event = %#v, want llm.final", finalEvent)
	}
	for _, event := range []map[string]any{deltaEvent, finalEvent} {
		if event["trace_id"] != voiceDirect.response.TraceID || event["job_id"] != voiceDirect.response.JobID || event["message_id"] != voiceDirect.response.MessageID {
			t.Fatalf("event identity does not match finalized response: event=%#v response=%+v", event, voiceDirect.response)
		}
	}
}

func TestVoiceChatInputAudioBridge_FailsClosedWhenVoiceResultCannotBePublished(t *testing.T) {
	for _, test := range []struct {
		name     string
		response orchestrator.ProcessMessageResponse
		err      error
	}{
		{
			name: "owner error",
			err:  fmt.Errorf("canonical append failed"),
		},
		{
			name: "invalid trace identity",
			response: orchestrator.ProcessMessageResponse{
				JobID:     task.NewJobID().String(),
				TraceID:   "not-a-trace",
				MessageID: string(modulecore.NewMessageID()),
			},
		},
		{
			name: "trace and job identity collide",
			response: func() orchestrator.ProcessMessageResponse {
				traceID := string(modulecore.NewTraceID())
				return orchestrator.ProcessMessageResponse{
					JobID:     traceID,
					TraceID:   traceID,
					MessageID: string(modulecore.NewMessageID()),
				}
			}(),
		},
		{
			name: "missing message identity",
			response: orchestrator.ProcessMessageResponse{
				JobID:   task.NewJobID().String(),
				TraceID: string(modulecore.NewTraceID()),
			},
		},
		{
			name: "missing owner response",
			response: func() orchestrator.ProcessMessageResponse {
				response := validInputAudioVoiceDirectResponse(" ")
				response.Response = ""
				return response
			}(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pcm := rawPCM16Chunk()
			voiceDirect := &inputAudioVoiceDirectHandler{response: test.response, err: test.err}
			llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"音声を確認しました"}}]}`))
			}))
			defer llm.Close()

			mux := http.NewServeMux()
			registerVoiceChatRoutes(mux, handleVoiceChatInputAudioBridge("ws"+strings.TrimPrefix(llm.URL, "http")+"/v1/chat/audio/sessions", voiceChatInputAudioSettings{}, voiceDirect, nil))
			bridge := httptest.NewServer(mux)
			defer bridge.Close()

			conn, err := websocket.Dial("ws"+strings.TrimPrefix(bridge.URL, "http")+modulevoicechat.RoutePathPrimary, "", "http://localhost/")
			if err != nil {
				t.Fatalf("dial bridge websocket: %v", err)
			}
			defer conn.Close()
			if err := websocket.Message.Send(conn, `{"type":"session.start","utterance_id":"utt-1","sample_rate":16000,"channels":1,"format":"pcm16le","channel":"viewer"}`); err != nil {
				t.Fatalf("send start: %v", err)
			}
			var ready string
			if err := websocket.Message.Receive(conn, &ready); err != nil {
				t.Fatalf("receive ready: %v", err)
			}
			if err := websocket.Message.Send(conn, pcm); err != nil {
				t.Fatalf("send pcm: %v", err)
			}
			if err := websocket.Message.Send(conn, `{"type":"session.commit","utterance_id":"utt-1"}`); err != nil {
				t.Fatalf("send commit: %v", err)
			}

			var failure string
			if err := websocket.Message.Receive(conn, &failure); err != nil {
				t.Fatalf("receive failure: %v", err)
			}
			var failureEvent map[string]any
			if err := json.Unmarshal([]byte(failure), &failureEvent); err != nil {
				t.Fatalf("decode failure event: %v", err)
			}
			if failureEvent["type"] != modulevoicechat.EventError || failureEvent["error_code"] != modulevoicechat.ErrorVoiceResultPublishFailed {
				t.Fatalf("unexpected failure event: %#v", failureEvent)
			}
			_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			var unexpected string
			if err := websocket.Message.Receive(conn, &unexpected); err == nil {
				t.Fatalf("unexpected success event after publish failure: %s", unexpected)
			}
			_ = conn.SetReadDeadline(time.Time{})
		})
	}
}

func TestVoiceChatInputAudioBridge_InterruptsIdleChatDuringVoiceSession(t *testing.T) {
	pcm := rawPCM16Chunk()
	voiceDirect := &inputAudioVoiceDirectHandler{response: validInputAudioVoiceDirectResponse("音声を確認しました")}
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"音声を確認しました"}}]}`))
	}))
	defer llm.Close()

	idle := &recordingVoiceChatIdleNotifier{}
	mux := http.NewServeMux()
	registerVoiceChatRoutes(mux, handleVoiceChatInputAudioBridge("ws"+strings.TrimPrefix(llm.URL, "http")+"/v1/chat/audio/sessions", voiceChatInputAudioSettings{}, voiceDirect, idle))
	bridge := httptest.NewServer(mux)
	defer bridge.Close()

	conn, err := websocket.Dial("ws"+strings.TrimPrefix(bridge.URL, "http")+modulevoicechat.RoutePathPrimary, "", "http://localhost/")
	if err != nil {
		t.Fatalf("dial bridge websocket: %v", err)
	}
	defer conn.Close()

	if err := websocket.Message.Send(conn, `{"type":"session.start","utterance_id":"utt-1","sample_rate":16000,"channels":1,"format":"pcm16le","channel":"viewer"}`); err != nil {
		t.Fatalf("send start: %v", err)
	}
	var ready string
	if err := websocket.Message.Receive(conn, &ready); err != nil {
		t.Fatalf("receive ready: %v", err)
	}
	if idle.activities != 1 {
		t.Fatalf("expected voice session start to notify idle activity, got %d", idle.activities)
	}
	if got := idle.chatBusy; len(got) != 1 || got[0] != true {
		t.Fatalf("expected chat busy to start on input_audio voice input, got %#v", got)
	}
	if err := websocket.Message.Send(conn, pcm); err != nil {
		t.Fatalf("send pcm: %v", err)
	}
	if err := websocket.Message.Send(conn, `{"type":"session.commit","utterance_id":"utt-1"}`); err != nil {
		t.Fatalf("send commit: %v", err)
	}
	var delta string
	if err := websocket.Message.Receive(conn, &delta); err != nil {
		t.Fatalf("receive delta: %v", err)
	}
	var final string
	if err := websocket.Message.Receive(conn, &final); err != nil {
		t.Fatalf("receive final: %v", err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		if got := idle.chatBusy; len(got) >= 2 && got[len(got)-1] == false {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected chat busy to end after input_audio final, got %#v", idle.chatBusy)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAnnotateVoiceChatFinalMetrics_AddsRenCrowRelayTimings(t *testing.T) {
	timing := &voiceChatRelayTiming{}
	commitIn := time.Unix(100, 100*int64(time.Millisecond))
	commitOut := time.Unix(100, 115*int64(time.Millisecond))
	finalIn := time.Unix(101, 350*int64(time.Millisecond))
	timing.markCommitIn(commitIn)
	timing.markCommitOut(commitOut)

	got := annotateVoiceChatFinalMetrics(
		[]byte(`{"type":"llm.final","utterance_id":"utt-1","metrics":{"commit_to_final_ms":1234.5},"text":"ok"}`),
		timing,
		finalIn,
	)
	var ev map[string]any
	if err := json.Unmarshal(got, &ev); err != nil {
		t.Fatalf("decode annotated final: %v; payload=%s", err, got)
	}
	metrics, _ := ev["metrics"].(map[string]any)
	if metrics["commit_to_final_ms"] != 1234.5 {
		t.Fatalf("RenCrow_LLM metrics were not preserved: %#v", metrics)
	}
	if metrics["rencrow_commit_recv_to_sent_ms"] != 15.0 {
		t.Fatalf("unexpected commit relay ms: %#v", metrics)
	}
	if metrics["rencrow_commit_sent_to_final_recv_ms"] != 1235.0 {
		t.Fatalf("unexpected final wait ms: %#v", metrics)
	}
	if metrics["rencrow_commit_recv_to_final_recv_ms"] != 1250.0 {
		t.Fatalf("unexpected total relay ms: %#v", metrics)
	}
}

func TestSplitVoiceChatStructuredFinalExtractsTranscriptAndReply(t *testing.T) {
	got, transcript := splitVoiceChatStructuredFinal([]byte(`{
		"type":"llm.final",
		"utterance_id":"utt-1",
		"session_id":"sess-1",
		"text":"{\"user_text\":\"Mioさんいますか\",\"reply\":\"いますよ。どうしましたか？\"}"
	}`))
	if transcript != "Mioさんいますか" {
		t.Fatalf("transcript=%q", transcript)
	}
	var ev map[string]any
	if err := json.Unmarshal(got, &ev); err != nil {
		t.Fatalf("decode final: %v payload=%s", err, got)
	}
	if ev["text"] != "いますよ。どうしましたか？" {
		t.Fatalf("final text=%#v", ev["text"])
	}
}

func TestSplitVoiceChatStructuredFinalLeavesPlainTextUnchanged(t *testing.T) {
	input := []byte(`{"type":"llm.final","utterance_id":"utt-1","text":"いますよ。"}`)
	got, transcript := splitVoiceChatStructuredFinal(input)
	if transcript != "" {
		t.Fatalf("transcript=%q", transcript)
	}
	if string(got) != string(input) {
		t.Fatalf("plain final changed: %s", got)
	}
}

func TestVoiceChatDisabledHandlerReturnsErrorFrame(t *testing.T) {
	mux := http.NewServeMux()
	registerVoiceChatRoutes(mux, handleVoiceChatDisabled())
	server := httptest.NewServer(mux)
	defer server.Close()

	conn, err := websocket.Dial("ws"+strings.TrimPrefix(server.URL, "http")+modulevoicechat.RoutePathPrimary, "", "http://localhost/")
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	var msg string
	if err := websocket.Message.Receive(conn, &msg); err != nil {
		t.Fatalf("receive error frame: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(msg), &payload); err != nil {
		t.Fatalf("decode error frame: %v", err)
	}
	if payload["type"] != modulevoicechat.EventError || payload["error_code"] != modulevoicechat.ErrorVoiceChatDisabled {
		t.Fatalf("unexpected error frame: %s", msg)
	}
}

func TestIsVoiceChatTextFramePayload(t *testing.T) {
	if !isVoiceChatTextFramePayload([]byte(`{"type":"session.ready"}`)) {
		t.Fatal("json object should be relayed as text")
	}
	if isVoiceChatTextFramePayload(rawPCM16Chunk()) {
		t.Fatal("pcm16 audio should be relayed as binary")
	}
}
