package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	modulevoicechat "github.com/Nyukimin/RenCrow_CORE/modules/voicechat"
	"golang.org/x/net/websocket"
)

// TestVoiceChatInputAudioBridge_RequestIsNonStreamingJSONObject は
// input_audio 要求が response_format=json_object と stream=true を同時に
// LLM ゲートウェイへ送らないことを検証する。
//
// 実測 (2026-09-25 11:35:51Z rencrow-llm journal):
//
//	llm.upstream.error path=/v1/chat/completions status=400
//	code=INVALID_REQUEST message="response_format json_object requires
//	stream=false at the Runtime boundary"
//
// Voice Direct の応答は {"user_text","reply"} の JSON オブジェクトを丸ごと
// 解析して確定させるため、トークン単位の配送先は無く、ストリーム化は
// 上流ランタイムの制約に抵触するだけである。core.yaml の
// mio.generation.stream=true がそのまま流れてくるため、境界で抑える。
func TestVoiceChatInputAudioBridge_RequestIsNonStreamingJSONObject(t *testing.T) {
	pcm := rawPCM16Chunk()
	voiceDirect := &inputAudioVoiceDirectHandler{
		response: validInputAudioVoiceDirectResponse("ストリーム非対応ゲートウェイ応答"),
	}
	var captured map[string]any
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"user_text\":\"音声\",\"reply\":\"回答\"}"}}]}`))
	}))
	defer llmServer.Close()

	mux := http.NewServeMux()
	registerVoiceChatRoutes(mux, handleVoiceChatInputAudioBridge("ws"+strings.TrimPrefix(llmServer.URL, "http")+"/v1/chat/audio/sessions", voiceChatInputAudioSettings{
		Model: "mio-gemma-4-12b-it",
	}, voiceDirect, nil, voiceChatExecutionOwners{}))
	bridge := httptest.NewServer(mux)
	defer bridge.Close()

	conn, err := websocket.Dial("ws"+strings.TrimPrefix(bridge.URL, "http")+modulevoicechat.RoutePathPrimary, "", "http://localhost/")
	if err != nil {
		t.Fatalf("dial bridge websocket: %v", err)
	}
	defer conn.Close()

	if err := websocket.Message.Send(conn, `{"type":"session.start","utterance_id":"utt-stream","sample_rate":16000,"channels":1,"format":"pcm16le","channel":"viewer","prompt":"短く確認"}`); err != nil {
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
	if err := websocket.Message.Send(conn, `{"type":"session.commit","utterance_id":"utt-stream"}`); err != nil {
		t.Fatalf("send commit: %v", err)
	}
	for _, want := range []string{`"type":"llm.delta"`, `"type":"llm.final"`} {
		var event string
		if err := websocket.Message.Receive(conn, &event); err != nil {
			t.Fatalf("receive %s: %v", want, err)
		}
		if !strings.Contains(event, want) {
			t.Fatalf("expected %s, got %s", want, event)
		}
	}

	if captured == nil {
		t.Fatal("gateway never received the input_audio request")
	}
	responseFormat, _ := captured["response_format"].(map[string]any)
	if responseFormat["type"] != "json_object" {
		t.Fatalf("input_audio must keep the json_object response format: %#v", captured["response_format"])
	}
	if stream, ok := captured["stream"]; ok && stream != false {
		t.Fatalf("json_object must not be sent with stream=true (gateway returns 400 INVALID_REQUEST): %#v", captured)
	}
	if _, ok := captured["stream_options"]; ok {
		t.Fatalf("json_object must not carry stream_options: %#v", captured["stream_options"])
	}
	// mio.generation.stream=true が input_audio の設定そのものに漏れることを防ぐ。
	// 設定が設定に残れば rencrowllm プロバイダーが stream=true を添え、
	// 上流ランタイムが 400 で拒否する（上記 journal 実測）。
	if _, ok := reflect.TypeOf(voiceChatInputAudioSettings{}).FieldByName("Stream"); ok {
		t.Fatal("voiceChatInputAudioSettings must not carry a stream knob: input_audio is always non-streaming")
	}
}
