package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
)

func TestNewClaudeProvider(t *testing.T) {
	provider := NewClaudeProvider("test-api-key", "claude-3-5-sonnet-20241022")

	if provider == nil {
		t.Fatal("NewClaudeProvider should not return nil")
	}

	if provider.Name() != "claude-claude-3-5-sonnet-20241022" {
		t.Errorf("Expected name 'claude-claude-3-5-sonnet-20241022', got '%s'", provider.Name())
	}
}

func TestClaudeProviderGenerate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("Expected path '/v1/messages', got '%s'", r.URL.Path)
		}

		// APIキーヘッダー確認
		apiKey := r.Header.Get("x-api-key")
		if apiKey != "test-api-key" {
			t.Errorf("Expected API key 'test-api-key', got '%s'", apiKey)
		}

		// Anthropic-Versionヘッダー確認
		version := r.Header.Get("anthropic-version")
		if version == "" {
			t.Error("anthropic-version header should be set")
		}

		// レスポンス
		response := map[string]interface{}{
			"id":   "msg_123",
			"type": "message",
			"role": "assistant",
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": "こんにちは！お手伝いします。",
				},
			},
			"stop_reason": "end_turn",
			"usage": map[string]interface{}{
				"input_tokens":  10,
				"output_tokens": 20,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewClaudeProvider("test-api-key", "claude-3-5-sonnet-20241022")
	provider.SetBaseURL(server.URL) // テスト用にベースURLを上書き

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

	if resp.TokensUsed != 30 { // input 10 + output 20
		t.Errorf("Expected 30 tokens used, got %d", resp.TokensUsed)
	}

	if resp.FinishReason != "end_turn" {
		t.Errorf("Expected finish reason 'end_turn', got '%s'", resp.FinishReason)
	}
}

func TestClaudeProviderGenerate_WithSystemPrompt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		// システムプロンプトが含まれているか確認
		system, ok := reqBody["system"].(string)
		if !ok || system == "" {
			t.Error("Request should contain system prompt")
		}

		if system != "You are a helpful assistant" {
			t.Errorf("Expected system prompt 'You are a helpful assistant', got '%s'", system)
		}

		response := map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": "System prompt applied"},
			},
			"stop_reason": "end_turn",
			"usage":       map[string]interface{}{"input_tokens": 10, "output_tokens": 5},
		}

		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewClaudeProvider("test-api-key", "claude-3-5-sonnet-20241022")
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

func TestClaudeProviderGenerate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		response := map[string]interface{}{
			"type": "error",
			"error": map[string]interface{}{
				"type":    "rate_limit_error",
				"message": "Rate limit exceeded",
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewClaudeProvider("test-api-key", "claude-3-5-sonnet-20241022")
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
		if r.URL.Path != "/v1/messages" {
			t.Errorf("expected path /v1/messages, got %s", r.URL.Path)
		}

		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		// tools が送信されていることを確認
		tools, ok := reqBody["tools"].([]interface{})
		if !ok || len(tools) == 0 {
			t.Error("expected tools in request")
		}
		// Claude形式: name + input_schema
		tool0 := tools[0].(map[string]interface{})
		if _, ok := tool0["input_schema"]; !ok {
			t.Error("expected input_schema in tool definition (Claude format)")
		}

		// Claude形式のレスポンス: tool_use content block
		response := map[string]interface{}{
			"id":   "msg_123",
			"type": "message",
			"role": "assistant",
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": "検索します。",
				},
				{
					"type":  "tool_use",
					"id":    "toolu_abc123",
					"name":  "web_search",
					"input": map[string]interface{}{"query": "RenCrow"},
				},
			},
			"stop_reason": "tool_use",
			"usage":       map[string]interface{}{"input_tokens": 20, "output_tokens": 30},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewClaudeProvider("test-api-key", "claude-sonnet-4-5-20250929")
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
	// Claude の "tool_use" → ドメイン "tool_calls" に変換確認
	if resp.FinishReason != "tool_calls" {
		t.Errorf("expected finish_reason=tool_calls, got %s", resp.FinishReason)
	}
	if resp.Message.Content != "検索します。" {
		t.Errorf("expected text content, got %s", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.Message.ToolCalls))
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "toolu_abc123" {
		t.Errorf("expected ID=toolu_abc123, got %s", tc.ID)
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
			"content": []map[string]interface{}{
				{"type": "text", "text": "こんにちは！"},
			},
			"stop_reason": "end_turn",
			"usage":       map[string]interface{}{"input_tokens": 5, "output_tokens": 10},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewClaudeProvider("test-api-key", "claude-sonnet-4-5-20250929")
	provider.SetBaseURL(server.URL)

	resp, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: "user", Content: "こんにちは"},
		},
	})

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if resp.FinishReason != "end_turn" {
		t.Errorf("expected finish_reason=end_turn, got %s", resp.FinishReason)
	}
	if resp.Message.Content != "こんにちは！" {
		t.Errorf("expected content=こんにちは！, got %s", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(resp.Message.ToolCalls))
	}
}

func TestChat_ToolResultRoundtrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		// system がトップレベルに抽出されていることを確認
		if reqBody["system"] != "You are helpful." {
			t.Errorf("expected system at top level, got %v", reqBody["system"])
		}

		msgs := reqBody["messages"].([]interface{})
		// user, assistant(tool_use), user(tool_result) の3メッセージ（system除外）
		if len(msgs) != 3 {
			t.Errorf("expected 3 messages (system excluded), got %d", len(msgs))
		}

		// tool_result は user メッセージ内の content block であることを確認
		toolResultMsg := msgs[2].(map[string]interface{})
		if toolResultMsg["role"] != "user" {
			t.Errorf("expected tool_result wrapped in user msg, got role=%v", toolResultMsg["role"])
		}

		response := map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": "検索結果はこちらです。"},
			},
			"stop_reason": "end_turn",
			"usage":       map[string]interface{}{"input_tokens": 50, "output_tokens": 20},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewClaudeProvider("test-api-key", "claude-sonnet-4-5-20250929")
	provider.SetBaseURL(server.URL)

	resp, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "検索して"},
			{Role: "assistant", ToolCalls: []llm.ToolCall{
				{ID: "toolu_1", Function: llm.ToolCallFunction{Name: "web_search", Arguments: map[string]any{"query": "test"}}},
			}},
			{Role: "tool", Content: "result data", ToolCallID: "toolu_1"},
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
		w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Rate limit"}}`))
	}))
	defer server.Close()

	provider := NewClaudeProvider("test-api-key", "claude-sonnet-4-5-20250929")
	provider.SetBaseURL(server.URL)

	_, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: "user", Content: "test"}},
	})

	if err == nil {
		t.Error("expected error for 429 response")
	}
}

func TestClaudeProviderGenerate_InvalidAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		response := map[string]interface{}{
			"type": "error",
			"error": map[string]interface{}{
				"type":    "authentication_error",
				"message": "Invalid API key",
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewClaudeProvider("invalid-key", "claude-3-5-sonnet-20241022")
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
