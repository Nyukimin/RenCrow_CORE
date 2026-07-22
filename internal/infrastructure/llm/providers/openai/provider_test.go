package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func TestNewOpenAIProvider(t *testing.T) {
	provider := NewOpenAIProvider("test-api-key", "gpt-4")

	if provider == nil {
		t.Fatal("NewOpenAIProvider should not return nil")
	}

	if provider.Name() != "openai-gpt-4" {
		t.Errorf("Expected name 'openai-gpt-4', got '%s'", provider.Name())
	}
}

func TestStreamingGatewayErrorIsNotAnEmptySuccess(t *testing.T) {
	provider := NewOpenAIProviderWithOptions("", "mio", "http://gateway.invalid", time.Second)
	stream := strings.NewReader("data: {\"error\":{\"code\":\"EMPTY_FINAL_CONTENT\",\"message\":\"target stream returned no content\"}}\n\ndata: [DONE]\n\n")

	_, err := provider.readChatCompletionsStream(stream, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "EMPTY_FINAL_CONTENT") {
		t.Fatalf("error=%v", err)
	}
}

func TestOpenAIProviderGenerate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("Expected path '/v1/chat/completions', got '%s'", r.URL.Path)
		}

		// Authorizationヘッダー確認
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-api-key" {
			t.Errorf("Expected 'Bearer test-api-key', got '%s'", auth)
		}

		// リクエストボディ検証
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		if reqBody["model"] != "gpt-4" {
			t.Errorf("Expected model 'gpt-4', got '%v'", reqBody["model"])
		}

		// レスポンス
		response := map[string]interface{}{
			"id":      "chatcmpl-123",
			"object":  "chat.completion",
			"created": 1677652288,
			"model":   "gpt-4",
			"choices": []map[string]interface{}{
				{
					"index": 0,
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "こんにちは！お手伝いします。",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]interface{}{
				"prompt_tokens":     10,
				"completion_tokens": 20,
				"total_tokens":      30,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-api-key", "gpt-4")
	provider.SetBaseURL(server.URL)

	req := llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "こんにちは"},
		},
		MaxTokens:   1000,
		Temperature: 0.7,
	}

	resp, err := provider.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if resp.Content != "こんにちは！お手伝いします。" {
		t.Errorf("Expected response content, got '%s'", resp.Content)
	}

	if resp.TokensUsed != 30 {
		t.Errorf("Expected 30 tokens used, got %d", resp.TokensUsed)
	}

	if resp.FinishReason != "stop" {
		t.Errorf("Expected finish reason 'stop', got '%s'", resp.FinishReason)
	}
}

func TestOpenAIProviderGenerate_LocalCompatibleNoAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("expected no Authorization header, got %q", got)
		}
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if reqBody["model"] != "Chat" {
			t.Fatalf("expected model Chat, got %v", reqBody["model"])
		}
		if reqBody["parse_reasoning"] != true {
			t.Fatalf("expected parse_reasoning=true, got %v", reqBody["parse_reasoning"])
		}
		if reqBody["include_reasoning"] != false {
			t.Fatalf("expected include_reasoning=false, got %v", reqBody["include_reasoning"])
		}
		if reqBody["separate_reasoning"] != true {
			t.Fatalf("expected separate_reasoning=true, got %v", reqBody["separate_reasoning"])
		}
		if _, ok := reqBody["enable_thinking"]; ok {
			t.Fatalf("enable_thinking should not be sent to ThinkingBridge server: %#v", reqBody)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message":       map[string]interface{}{"role": "assistant", "content": "ok", "reasoning_content": "hidden", "thinking": "hidden", "raw_content": "<think>hidden</think>ok"},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]interface{}{"total_tokens": 1},
		})
	}))
	defer server.Close()

	provider := NewOpenAIProviderWithOptions("", "Chat", server.URL, 0)
	resp, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages:  []llm.Message{{Role: "user", Content: "ping"}},
		MaxTokens: 1,
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("unexpected content: %q", resp.Content)
	}
}

func TestOpenAIProviderGenerate_LocalCompatibleMergesProviderOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if reqBody["think"] != false {
			t.Fatalf("expected think=false, got %#v in %#v", reqBody["think"], reqBody)
		}
		if reqBody["parse_reasoning"] != true || reqBody["include_reasoning"] != false || reqBody["separate_reasoning"] != true {
			t.Fatalf("thinking bridge fields should remain enabled: %#v", reqBody)
		}
		if reqBody["model"] != "Worker" {
			t.Fatalf("provider options must not override reserved model field: %#v", reqBody)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message":       map[string]interface{}{"role": "assistant", "content": "ok"},
					"finish_reason": "stop",
				},
			},
		})
	}))
	defer server.Close()

	provider := NewOpenAIProviderWithOptions("", "Worker", server.URL, 0)
	if _, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "ping"}},
		ProviderOptions: map[string]any{
			"think": false,
			"model": "BadOverride",
		},
	}); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
}

func TestOpenAIProviderGenerate_LocalCompatibleSendsModelContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		options, ok := reqBody["options"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected options in local request: %#v", reqBody)
		}
		if got := int(options["num_ctx"].(float64)); got != 131072 {
			t.Fatalf("num_ctx = %d, want 131072", got)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message":       map[string]interface{}{"role": "assistant", "content": "ok"},
					"finish_reason": "stop",
				},
			},
		})
	}))
	defer server.Close()

	provider := NewOpenAIProviderWithModelContext("", "Worker", server.URL, 0, 131072)
	if _, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "ping"}},
	}); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
}

func TestOpenAIProviderGenerate_LocalCompatiblePreservesExplicitNumCtx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		options, ok := reqBody["options"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected options in local request: %#v", reqBody)
		}
		if got := int(options["num_ctx"].(float64)); got != 32768 {
			t.Fatalf("num_ctx = %d, want explicit 32768", got)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message":       map[string]interface{}{"role": "assistant", "content": "ok"},
					"finish_reason": "stop",
				},
			},
		})
	}))
	defer server.Close()

	provider := NewOpenAIProviderWithModelContext("", "Worker", server.URL, 0, 131072)
	if _, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "ping"}},
		ProviderOptions: map[string]any{
			"options": map[string]any{"num_ctx": 32768},
		},
	}); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
}

func TestOpenAIProviderGenerate_PublicOpenAIDoesNotSendThinkingBridgeFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		for _, key := range []string{"parse_reasoning", "include_reasoning", "separate_reasoning", "think"} {
			if _, ok := reqBody[key]; ok {
				t.Fatalf("public OpenAI request should not include %s: %#v", key, reqBody)
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message":       map[string]interface{}{"role": "assistant", "content": "ok"},
					"finish_reason": "stop",
				},
			},
		})
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-api-key", "gpt-4")
	provider.SetBaseURL(server.URL)

	if _, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "ping"}},
	}); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
}

func TestOpenAIProviderGenerate_LocalCompatibleStreamingUsesDeltaContentOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if reqBody["stream"] != true {
			t.Fatalf("expected stream=true, got %v", reqBody["stream"])
		}
		if reqBody["include_reasoning"] != false || reqBody["separate_reasoning"] != true {
			t.Fatalf("unexpected thinking bridge flags: %#v", reqBody)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range []string{
			`data: {"choices":[{"delta":{"reasoning_content":"hidden","content":""}}]}`,
			`data: {"choices":[{"delta":{"reasoning_content":"","content":"最終"}}]}`,
			`data: {"choices":[{"delta":{"content":"回答"}}]}`,
			`data: [DONE]`,
		} {
			fmt.Fprintln(w, line)
			fmt.Fprintln(w)
		}
	}))
	defer server.Close()

	var tokens []string
	provider := NewOpenAIProviderWithOptions("", "Worker", server.URL, 0)
	resp, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages:  []llm.Message{{Role: "user", Content: "ping"}},
		OnToken:   func(token string) { tokens = append(tokens, token) },
		MaxTokens: 4,
	})
	if err != nil {
		t.Fatalf("Generate streaming failed: %v", err)
	}
	if resp.Content != "最終回答" {
		t.Fatalf("stream content = %q, want final content only", resp.Content)
	}
	if strings.Join(tokens, "|") != "最終|回答" {
		t.Fatalf("tokens = %#v, want content deltas only", tokens)
	}
}

func TestOpenAIProviderGenerate_LocalCompatibleStreamingDropsUntaggedReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range []string{
			`data: {"choices":[{"delta":{"content":"Okay, the user is asking for a confirmation message. "}}]}`,
			`data: {"choices":[{"delta":{"content":"Let me check the query again.\n\nFinal answer: "}}]}`,
			`data: {"choices":[{"delta":{"content":"了解しました。"}}]}`,
			`data: [DONE]`,
		} {
			fmt.Fprintln(w, line)
			fmt.Fprintln(w)
		}
	}))
	defer server.Close()

	var tokens []string
	provider := NewOpenAIProviderWithOptions("", "Worker", server.URL, 0)
	resp, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "ping"}},
		OnToken:  func(token string) { tokens = append(tokens, token) },
	})
	if err != nil {
		t.Fatalf("Generate streaming failed: %v", err)
	}
	if resp.Content != "了解しました。" {
		t.Fatalf("stream content = %q, want final answer only", resp.Content)
	}
	if strings.Join(tokens, "|") != "了解しました。" {
		t.Fatalf("tokens = %#v, want sanitized final answer only", tokens)
	}
}

func TestOpenAIProviderGenerate_LocalCompatibleTreatsNoReasoningLeakAsReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":         "assistant",
						"content":      "Okay, the user is asking for a confirmation message in Japanese, just one sentence. Let me check the query again.\n\nFinal answer: 了解しました。",
						"parse_status": "no_reasoning",
						"parser_name":  "qwen3",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]interface{}{"total_tokens": 32},
		})
	}))
	defer server.Close()

	provider := NewOpenAIProviderWithOptions("", "Worker", server.URL, 0)
	resp, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "疎通確認"}},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if resp.Content != "了解しました。" {
		t.Fatalf("content = %q, want final answer only", resp.Content)
	}
}

func TestOpenAIProviderGenerate_LocalCompatibleDropsReasoningOnlyNoReasoningLeak(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":         "assistant",
						"content":      "Okay, the user is asking for a confirmation message in Japanese, just one sentence. Let me check the query again.\n\nThey wrote: \"疎通確認です。\" So they want me to confirm communication.",
						"parse_status": "no_reasoning",
						"parser_name":  "qwen3",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]interface{}{"total_tokens": 32},
		})
	}))
	defer server.Close()

	provider := NewOpenAIProviderWithOptions("", "Worker", server.URL, 0)
	resp, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "疎通確認"}},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if resp.Content != "" {
		t.Fatalf("content = %q, want reasoning-only leak removed", resp.Content)
	}
}

func TestOpenAIProviderGenerate_WithSystemPrompt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		// メッセージリストにsystemメッセージが含まれているか確認
		messages, ok := reqBody["messages"].([]interface{})
		if !ok || len(messages) == 0 {
			t.Error("Request should contain messages")
		}

		firstMsg := messages[0].(map[string]interface{})
		if firstMsg["role"] != "system" {
			t.Errorf("First message should be system, got '%v'", firstMsg["role"])
		}

		if firstMsg["content"] != "You are a helpful assistant" {
			t.Errorf("Expected system content 'You are a helpful assistant', got '%v'", firstMsg["content"])
		}

		response := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "System prompt applied",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]interface{}{"total_tokens": 15},
		}

		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-api-key", "gpt-4")
	provider.SetBaseURL(server.URL)

	req := llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "テスト"},
		},
		SystemPrompt: "You are a helpful assistant",
		MaxTokens:    1000,
	}

	_, err := provider.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate with system prompt failed: %v", err)
	}
}

func TestConvertMessagesAggregatesSystemMessagesAtHead(t *testing.T) {
	provider := NewOpenAIProvider("test-api-key", "gpt-4")

	got := provider.convertMessages(llm.GenerateRequest{
		SystemPrompt: "base system",
		Messages: []llm.Message{
			{Role: "user", Content: "first user"},
			{Role: "system", Content: "late system"},
			{Role: "assistant", Content: "assistant context"},
			{Role: "system", Content: "another late system"},
			{Role: "user", Content: "second user"},
		},
	})

	if len(got) != 4 {
		t.Fatalf("messages length = %d, want 4: %#v", len(got), got)
	}
	if got[0]["role"] != "system" {
		t.Fatalf("first message role = %v, want system: %#v", got[0]["role"], got)
	}
	content, _ := got[0]["content"].(string)
	for _, want := range []string{"base system", "late system", "another late system"} {
		if !strings.Contains(content, want) {
			t.Fatalf("aggregated system content missing %q:\n%s", want, content)
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i]["role"] == "system" {
			t.Fatalf("message %d should not remain system: %#v", i, got)
		}
	}
	if got[1]["role"] != "user" || got[2]["role"] != "assistant" || got[3]["role"] != "user" {
		t.Fatalf("non-system message order changed: %#v", got)
	}
}

func TestOpenAIProviderGenerate_MultipleMessages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		messages, ok := reqBody["messages"].([]interface{})
		if !ok || len(messages) != 3 { // user, assistant, user
			t.Errorf("Expected 3 messages, got %d", len(messages))
		}

		response := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "Multi-turn response",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]interface{}{"total_tokens": 50},
		}

		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-api-key", "gpt-4")
	provider.SetBaseURL(server.URL)

	req := llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "こんにちは"},
			{Role: "assistant", Content: "こんにちは！"},
			{Role: "user", Content: "元気ですか？"},
		},
	}

	_, err := provider.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate with multiple messages failed: %v", err)
	}
}

func TestOpenAIProviderGenerate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		response := map[string]interface{}{
			"error": map[string]interface{}{
				"message": "Rate limit exceeded",
				"type":    "rate_limit_error",
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-api-key", "gpt-4")
	provider.SetBaseURL(server.URL)

	req := llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "テスト"},
		},
	}

	_, err := provider.Generate(context.Background(), req)
	if err == nil {
		t.Error("Expected error when API returns rate limit error")
	}
}

// --- Chat (tool calling) テスト ---

func TestChat_WithToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("expected path /v1/chat/completions, got %s", r.URL.Path)
		}

		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		// tools が送信されていることを確認
		tools, ok := reqBody["tools"].([]interface{})
		if !ok || len(tools) == 0 {
			t.Error("expected tools in request")
		}

		response := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role": "assistant",
						"tool_calls": []map[string]interface{}{
							{
								"id":   "call_abc123",
								"type": "function",
								"function": map[string]interface{}{
									"name":      "web_search",
									"arguments": `{"query":"RenCrow"}`,
								},
							},
						},
					},
					"finish_reason": "tool_calls",
				},
			},
			"usage": map[string]interface{}{"total_tokens": 50},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-api-key", "gpt-4")
	provider.SetBaseURL(server.URL)

	resp, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: "user", Content: "RenCrowを検索して"},
		},
		Tools: []llm.ToolDefinition{
			{
				Type: "function",
				Function: llm.ToolFunctionDef{
					Name:        "web_search",
					Description: "Web検索を実行",
					Parameters:  map[string]any{"type": "object"},
				},
			},
		},
	})

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("expected finish_reason=tool_calls, got %s", resp.FinishReason)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.Message.ToolCalls))
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "call_abc123" {
		t.Errorf("expected ID=call_abc123, got %s", tc.ID)
	}
	if tc.Function.Name != "web_search" {
		t.Errorf("expected tool name=web_search, got %s", tc.Function.Name)
	}
	if tc.Function.Arguments["query"] != "RenCrow" {
		t.Errorf("expected query=RenCrow, got %v", tc.Function.Arguments["query"])
	}
}

func TestChat_WithoutToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "こんにちは！",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]interface{}{"total_tokens": 10},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-api-key", "gpt-4")
	provider.SetBaseURL(server.URL)

	resp, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: "user", Content: "こんにちは"},
		},
	})

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("expected finish_reason=stop, got %s", resp.FinishReason)
	}
	if resp.Message.Content != "こんにちは！" {
		t.Errorf("expected content=こんにちは！, got %s", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(resp.Message.ToolCalls))
	}
}

func TestChat_LocalCompatibleSendsThinkingBridgeFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if reqBody["parse_reasoning"] != true {
			t.Fatalf("expected parse_reasoning=true, got %v", reqBody["parse_reasoning"])
		}
		if reqBody["include_reasoning"] != false {
			t.Fatalf("expected include_reasoning=false, got %v", reqBody["include_reasoning"])
		}
		if reqBody["separate_reasoning"] != true {
			t.Fatalf("expected separate_reasoning=true, got %v", reqBody["separate_reasoning"])
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message":       map[string]interface{}{"role": "assistant", "content": "本文", "reasoning_content": "hidden"},
					"finish_reason": "stop",
				},
			},
		})
	}))
	defer server.Close()

	provider := NewOpenAIProviderWithOptions("", "Worker", server.URL, 0)
	resp, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if resp.Message.Content != "本文" {
		t.Fatalf("content = %q, want visible content only", resp.Message.Content)
	}
}

func TestChat_ToolResultRoundtrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		msgs := reqBody["messages"].([]interface{})
		// system, user, assistant(tool_calls), tool, の4メッセージを期待
		if len(msgs) != 4 {
			t.Errorf("expected 4 messages, got %d", len(msgs))
		}

		// tool メッセージの検証
		toolMsg := msgs[3].(map[string]interface{})
		if toolMsg["role"] != "tool" {
			t.Errorf("expected role=tool, got %v", toolMsg["role"])
		}
		if toolMsg["tool_call_id"] != "call_1" {
			t.Errorf("expected tool_call_id=call_1, got %v", toolMsg["tool_call_id"])
		}

		response := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "検索結果はこちらです。",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]interface{}{"total_tokens": 30},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-api-key", "gpt-4")
	provider.SetBaseURL(server.URL)

	resp, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "検索して"},
			{Role: "assistant", ToolCalls: []llm.ToolCall{
				{ID: "call_1", Function: llm.ToolCallFunction{Name: "web_search", Arguments: map[string]any{"query": "test"}}},
			}},
			{Role: "tool", Content: "result data", ToolCallID: "call_1"},
		},
	})

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if resp.Message.Content != "検索結果はこちらです。" {
		t.Errorf("expected final answer, got %s", resp.Message.Content)
	}
}

func TestChat_ErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"Rate limit exceeded"}}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-api-key", "gpt-4")
	provider.SetBaseURL(server.URL)

	_, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: "user", Content: "test"}},
	})

	if err == nil {
		t.Error("expected error for 429 response")
	}
}

func TestOpenAIProviderGenerate_InvalidAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		response := map[string]interface{}{
			"error": map[string]interface{}{
				"message": "Incorrect API key provided",
				"type":    "invalid_request_error",
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("invalid-key", "gpt-4")
	provider.SetBaseURL(server.URL)

	req := llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "テスト"},
		},
	}

	_, err := provider.Generate(context.Background(), req)
	if err == nil {
		t.Error("Expected error for invalid API key")
	}
}
