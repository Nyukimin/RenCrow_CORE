package deepseek

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
)

func TestNewDeepSeekProvider(t *testing.T) {
	provider := NewDeepSeekProvider("test-api-key", "deepseek-chat")

	if provider == nil {
		t.Fatal("NewDeepSeekProvider should not return nil")
	}

	if provider.Name() != "deepseek-deepseek-chat" {
		t.Errorf("Expected name 'deepseek-deepseek-chat', got '%s'", provider.Name())
	}
}

func TestDeepSeekProviderGenerate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("Expected path '/v1/chat/completions', got '%s'", r.URL.Path)
		}

		// Authorizationヘッダー確認
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-api-key" {
			t.Errorf("Expected 'Bearer test-api-key', got '%s'", auth)
		}

		// レスポンス（OpenAI互換）
		response := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "DeepSeekの応答です。",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]interface{}{
				"total_tokens": 25,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewDeepSeekProvider("test-api-key", "deepseek-chat")
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

	if resp.Content != "DeepSeekの応答です。" {
		t.Errorf("Expected response content, got '%s'", resp.Content)
	}

	if resp.TokensUsed != 25 {
		t.Errorf("Expected 25 tokens used, got %d", resp.TokensUsed)
	}
}

func TestDeepSeekProviderGenerate_WithSystemPrompt(t *testing.T) {
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

		response := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"content": "System prompt applied",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]interface{}{"total_tokens": 10},
		}

		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewDeepSeekProvider("test-api-key", "deepseek-chat")
	provider.SetBaseURL(server.URL)

	req := llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "テスト"},
		},
		SystemPrompt: "You are a code architect",
	}

	_, err := provider.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate with system prompt failed: %v", err)
	}
}

// --- Chat (tool calling) テスト ---

func TestChat_WithToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

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
								"id":   "call_ds_001",
								"type": "function",
								"function": map[string]interface{}{
									"name":      "file_read",
									"arguments": `{"path":"/tmp/test.go"}`,
								},
							},
						},
					},
					"finish_reason": "tool_calls",
				},
			},
			"usage": map[string]interface{}{"total_tokens": 40},
		}

		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewDeepSeekProvider("test-api-key", "deepseek-chat")
	provider.SetBaseURL(server.URL)

	resp, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: "user", Content: "ファイルを読んで"},
		},
		Tools: []llm.ToolDefinition{
			{
				Type: "function",
				Function: llm.ToolFunctionDef{
					Name:        "file_read",
					Description: "ファイル読み取り",
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
	if tc.Function.Name != "file_read" {
		t.Errorf("expected tool name=file_read, got %s", tc.Function.Name)
	}
	if tc.Function.Arguments["path"] != "/tmp/test.go" {
		t.Errorf("expected path=/tmp/test.go, got %v", tc.Function.Arguments["path"])
	}
}

func TestChat_WithoutToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "DeepSeekの応答です。",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]interface{}{"total_tokens": 15},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewDeepSeekProvider("test-api-key", "deepseek-chat")
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
	if resp.Message.Content != "DeepSeekの応答です。" {
		t.Errorf("expected content, got %s", resp.Message.Content)
	}
}

func TestChat_ErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	provider := NewDeepSeekProvider("test-api-key", "deepseek-chat")
	provider.SetBaseURL(server.URL)

	_, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: "user", Content: "test"}},
	})

	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestDeepSeekProviderGenerate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		response := map[string]interface{}{
			"error": map[string]interface{}{
				"message": "Rate limit exceeded",
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewDeepSeekProvider("test-api-key", "deepseek-chat")
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
