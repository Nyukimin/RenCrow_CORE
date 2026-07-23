package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func newLoadedModelServer(t *testing.T, model string, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ps" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{
					{"name": model, "context_length": 8192},
				},
			})
			return
		}
		handler(w, r)
	}))
}

func TestNewOllamaProvider(t *testing.T) {
	provider := NewOllamaProvider("http://localhost:11434", "test-model")

	if provider == nil {
		t.Fatal("NewOllamaProvider should not return nil")
	}

	if provider.Name() != "ollama-test-model" {
		t.Errorf("Expected name 'ollama-test-model', got '%s'", provider.Name())
	}
}

func TestOllamaProviderGenerate_Success(t *testing.T) {
	// モックOllamaサーバー
	server := newLoadedModelServer(t, "test-model", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("Expected path '/api/generate', got '%s'", r.URL.Path)
		}

		if r.Method != "POST" {
			t.Errorf("Expected POST method, got '%s'", r.Method)
		}

		// レスポンス
		response := map[string]interface{}{
			"response": "こんにちは！何かお手伝いできますか？",
			"done":     true,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	})
	defer server.Close()

	provider := NewOllamaProvider(server.URL, "test-model")

	req := llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "こんにちは"},
		},
		MaxTokens:   100,
		Temperature: 0.7,
	}

	resp, err := provider.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if resp.Content != "こんにちは！何かお手伝いできますか？" {
		t.Errorf("Expected response content, got '%s'", resp.Content)
	}

	if resp.FinishReason != "stop" {
		t.Errorf("Expected finish reason 'stop', got '%s'", resp.FinishReason)
	}
}

func TestOllamaProviderGenerate_WithSystemPrompt(t *testing.T) {
	server := newLoadedModelServer(t, "test-model", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Fatalf("expected path '/api/generate', got '%s'", r.URL.Path)
		}
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		// システムプロンプトが含まれているか確認
		prompt, ok := reqBody["prompt"].(string)
		if !ok {
			t.Error("Request should contain prompt")
		}

		if prompt == "" {
			t.Error("Prompt should not be empty")
		}

		response := map[string]interface{}{
			"response": "System prompt applied",
			"done":     true,
		}

		json.NewEncoder(w).Encode(response)
	})
	defer server.Close()

	provider := NewOllamaProvider(server.URL, "test-model")

	req := llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "テスト"},
		},
		SystemPrompt: "You are a helpful assistant",
		MaxTokens:    100,
		Temperature:  0.7,
	}

	_, err := provider.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate with system prompt failed: %v", err)
	}
}

func TestOllamaProviderGenerate_SendsNumCtxWhenConfigured(t *testing.T) {
	server := newLoadedModelServer(t, "test-model", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Fatalf("expected path '/api/generate', got '%s'", r.URL.Path)
		}
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		options, ok := reqBody["options"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected options in request: %#v", reqBody)
		}
		if got := int(options["num_ctx"].(float64)); got != 32768 {
			t.Fatalf("expected num_ctx=32768, got %v", options["num_ctx"])
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"response": "ok",
			"done":     true,
		})
	})
	defer server.Close()

	provider := NewOllamaProviderWithNumCtx(server.URL, "test-model", 32768)
	_, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages:  []llm.Message{{Role: "user", Content: "テスト"}},
		MaxTokens: 16,
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
}

func TestOllamaProviderGenerate_PreservesRoleThinkingPolicy(t *testing.T) {
	tests := []struct {
		name        string
		options     map[string]any
		wantThink   any
		wantPresent bool
	}{
		{name: "Worker inherits backend default", wantPresent: false},
		{name: "Worker can enable thinking", options: map[string]any{"think": true}, wantThink: true, wantPresent: true},
		{name: "Chat policy disables thinking", options: map[string]any{"think": false}, wantThink: false, wantPresent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured map[string]any
			server := newLoadedModelServer(t, "test-model", func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"response": "ok", "done": true})
			})
			defer server.Close()

			provider := NewOllamaProvider(server.URL, "test-model")
			_, err := provider.Generate(context.Background(), llm.GenerateRequest{
				Messages:        []llm.Message{{Role: "user", Content: "test"}},
				ProviderOptions: tt.options,
			})
			if err != nil {
				t.Fatalf("Generate failed: %v", err)
			}
			got, present := captured["think"]
			if present != tt.wantPresent || got != tt.wantThink {
				t.Fatalf("think = %#v, present=%v; want %#v, present=%v; payload=%#v", got, present, tt.wantThink, tt.wantPresent, captured)
			}
		})
	}
}

func TestOllamaProviderGenerate_ServerError(t *testing.T) {
	server := newLoadedModelServer(t, "test-model", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Fatalf("expected path '/api/generate', got '%s'", r.URL.Path)
		}
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal server error"))
	})
	defer server.Close()

	provider := NewOllamaProvider(server.URL, "test-model")

	req := llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "テスト"},
		},
	}

	_, err := provider.Generate(context.Background(), req)
	if err == nil {
		t.Error("Expected error when server returns 500")
	}
}

func TestOllamaProviderGenerate_Timeout(t *testing.T) {
	releaseHandler := make(chan struct{})
	server := newLoadedModelServer(t, "test-model", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Fatalf("expected path '/api/generate', got '%s'", r.URL.Path)
		}
		// タイムアウトをシミュレート（レスポンスを返さない）
		select {
		case <-r.Context().Done():
		case <-releaseHandler:
		}
	})
	t.Cleanup(func() {
		close(releaseHandler)
		server.Close()
	})

	provider := NewOllamaProvider(server.URL, "test-model")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	req := llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "テスト"},
		},
	}

	_, err := provider.Generate(ctx, req)
	if err == nil {
		t.Error("Expected timeout error")
	}
}

func TestOllamaProviderGenerate_MultipleMessages(t *testing.T) {
	server := newLoadedModelServer(t, "test-model", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Fatalf("expected path '/api/generate', got '%s'", r.URL.Path)
		}
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		// プロンプトに複数のメッセージが含まれているか確認
		prompt, ok := reqBody["prompt"].(string)
		if !ok || prompt == "" {
			t.Error("Prompt should contain multiple messages")
		}

		response := map[string]interface{}{
			"response": "Multi-turn conversation response",
			"done":     true,
		}

		json.NewEncoder(w).Encode(response)
	})
	defer server.Close()

	provider := NewOllamaProvider(server.URL, "test-model")

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

// --- Chat (tool calling) テスト ---

func TestChat_WithToolCalls(t *testing.T) {
	server := newLoadedModelServer(t, "test-model", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("expected path /api/chat, got %s", r.URL.Path)
		}

		resp := ollamaChatResponse{
			Model: "test-model",
			Message: ollamaChatMessage{
				Role: "assistant",
				ToolCalls: []ollamaToolCall{
					{
						Function: ollamaToolCallFunction{
							Name:      "web_search",
							Arguments: map[string]any{"query": "RenCrow"},
						},
					},
				},
			},
			Done: true,
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer server.Close()

	provider := NewOllamaProvider(server.URL, "test-model")
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
	if tc.Function.Name != "web_search" {
		t.Errorf("expected tool name=web_search, got %s", tc.Function.Name)
	}
	if tc.Function.Arguments["query"] != "RenCrow" {
		t.Errorf("expected query=RenCrow, got %v", tc.Function.Arguments["query"])
	}
}

func TestChat_WithoutToolCalls(t *testing.T) {
	server := newLoadedModelServer(t, "test-model", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("expected path '/api/chat', got '%s'", r.URL.Path)
		}
		resp := ollamaChatResponse{
			Model: "test-model",
			Message: ollamaChatMessage{
				Role:    "assistant",
				Content: "こんにちは！",
			},
			Done: true,
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer server.Close()

	provider := NewOllamaProvider(server.URL, "test-model")
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

func TestChat_SendsNumCtxWhenConfigured(t *testing.T) {
	server := newLoadedModelServer(t, "test-model", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("expected path '/api/chat', got '%s'", r.URL.Path)
		}
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		options, ok := reqBody["options"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected options in request: %#v", reqBody)
		}
		if got := int(options["num_ctx"].(float64)); got != 16384 {
			t.Fatalf("expected num_ctx=16384, got %v", options["num_ctx"])
		}

		resp := ollamaChatResponse{
			Model: "test-model",
			Message: ollamaChatMessage{
				Role:    "assistant",
				Content: "こんにちは！",
			},
			Done: true,
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer server.Close()

	provider := NewOllamaProviderWithNumCtx(server.URL, "test-model", 16384)
	_, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: "user", Content: "こんにちは"},
		},
	})
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
}

func TestChat_MultipleToolCalls(t *testing.T) {
	server := newLoadedModelServer(t, "test-model", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("expected path '/api/chat', got '%s'", r.URL.Path)
		}
		resp := ollamaChatResponse{
			Model: "test-model",
			Message: ollamaChatMessage{
				Role: "assistant",
				ToolCalls: []ollamaToolCall{
					{Function: ollamaToolCallFunction{Name: "file_read", Arguments: map[string]any{"path": "/tmp/a.txt"}}},
					{Function: ollamaToolCallFunction{Name: "file_read", Arguments: map[string]any{"path": "/tmp/b.txt"}}},
				},
			},
			Done: true,
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer server.Close()

	provider := NewOllamaProvider(server.URL, "test-model")
	resp, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: "user", Content: "2ファイル読んで"}},
	})

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if len(resp.Message.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(resp.Message.ToolCalls))
	}
	// IDが一意であること
	if resp.Message.ToolCalls[0].ID == resp.Message.ToolCalls[1].ID {
		t.Error("tool call IDs should be unique")
	}
}

func TestChat_ErrorResponse(t *testing.T) {
	server := newLoadedModelServer(t, "test-model", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("expected path '/api/chat', got '%s'", r.URL.Path)
		}
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("model not found"))
	})
	defer server.Close()

	provider := NewOllamaProvider(server.URL, "test-model")
	_, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: "user", Content: "test"}},
	})

	if err == nil {
		t.Error("expected error for 500 response")
	}
}
