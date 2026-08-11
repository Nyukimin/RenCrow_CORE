package rencrowllm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func TestReadToolChatCompletionsStreamAccumulatesContentAndToolCalls(t *testing.T) {
	stream := strings.NewReader("\n: keepalive\n" +
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"確認\",\"tool_calls\":[{\"index\":0,\"id\":\"call_\",\"function\":{\"name\":\"browser.\",\"arguments\":\"{\\\"start_url\\\":\"}}]}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"中\",\"tool_calls\":[{\"index\":0,\"id\":\"1\",\"function\":{\"name\":\"run\",\"arguments\":\"\\\"http://localhost\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n" +
		"data: [DONE]\n")

	got, err := readToolChatCompletionsStream(stream)
	if err != nil {
		t.Fatal(err)
	}
	if got.Message.Role != "assistant" || got.Message.Content != "確認中" || got.FinishReason != "tool_calls" {
		t.Fatalf("response = %#v", got)
	}
	if len(got.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", got.Message.ToolCalls)
	}
	call := got.Message.ToolCalls[0]
	if call.ID != "call_1" || call.Function.Name != "browser.run" || call.Function.Arguments["start_url"] != "http://localhost" {
		t.Fatalf("tool call = %#v", call)
	}
}

func TestReadToolChatCompletionsStreamDefaultsAndErrors(t *testing.T) {
	t.Run("content defaults stop", func(t *testing.T) {
		got, err := readToolChatCompletionsStream(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n"))
		if err != nil || got.FinishReason != "stop" || got.Message.Role != "assistant" {
			t.Fatalf("got=%#v err=%v", got, err)
		}
	})
	t.Run("malformed arguments preserve raw", func(t *testing.T) {
		got, err := readToolChatCompletionsStream(strings.NewReader("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"x\",\"arguments\":\"{bad\"}}]},\"finish_reason\":\"tool_calls\"}]}\n"))
		if err != nil || got.Message.ToolCalls[0].Function.Arguments["_raw"] != "{bad" {
			t.Fatalf("got=%#v err=%v", got, err)
		}
	})
	t.Run("stream error", func(t *testing.T) {
		_, err := readToolChatCompletionsStream(strings.NewReader("data: {\"error\":{\"code\":\"BAD_REQUEST\",\"message\":\"bad input\"}}\n"))
		if err == nil || !strings.Contains(err.Error(), "BAD_REQUEST") || !strings.Contains(err.Error(), "bad input") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("empty", func(t *testing.T) {
		if _, err := readToolChatCompletionsStream(strings.NewReader(": comment\n\ndata: [DONE]\n")); err == nil {
			t.Fatal("expected empty stream error")
		}
	})
}

func TestGatewayProviderChatUsesStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["stream"] != true {
			t.Fatalf("stream = %#v, want true", payload["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n"))
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "worker", server.URL, time.Second)
	got, err := provider.Chat(context.Background(), llm.ChatRequest{Messages: []llm.ChatMessage{{Role: "user", Content: "run"}}})
	if err != nil || got.Message.Content != "ok" {
		t.Fatalf("got=%#v err=%v", got, err)
	}
}
