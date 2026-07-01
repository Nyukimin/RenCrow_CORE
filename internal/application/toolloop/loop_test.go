package toolloop

import (
	"context"
	"fmt"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
)

// --- モック ---

type mockToolCallingProvider struct {
	responses []llm.ChatResponse
	callIndex int
}

func (m *mockToolCallingProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	return llm.GenerateResponse{}, fmt.Errorf("not implemented")
}

func (m *mockToolCallingProvider) Name() string { return "mock" }

func (m *mockToolCallingProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	if m.callIndex >= len(m.responses) {
		return llm.ChatResponse{}, fmt.Errorf("no more mock responses (called %d times)", m.callIndex+1)
	}
	resp := m.responses[m.callIndex]
	m.callIndex++
	return resp, nil
}

type mockRunnerV2 struct {
	results map[string]*tool.ToolResponse
}

func (m *mockRunnerV2) ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
	if r, ok := m.results[toolName]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("unknown tool: %s", toolName)
}

func (m *mockRunnerV2) ListTools(ctx context.Context) ([]tool.ToolMetadata, error) {
	return nil, nil
}

// --- テスト ---

func TestRun_DirectAnswer(t *testing.T) {
	provider := &mockToolCallingProvider{
		responses: []llm.ChatResponse{
			{
				Message:      llm.ChatMessage{Role: "assistant", Content: "直接回答です"},
				FinishReason: "stop",
			},
		},
	}

	result, err := Run(context.Background(), provider, &mockRunnerV2{}, nil,
		[]llm.ChatMessage{{Role: "user", Content: "こんにちは"}},
		Config{MaxIterations: 10})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "直接回答です" {
		t.Errorf("expected '直接回答です', got '%s'", result)
	}
}

func TestRun_SingleToolCall(t *testing.T) {
	provider := &mockToolCallingProvider{
		responses: []llm.ChatResponse{
			// 1回目: tool call
			{
				Message: llm.ChatMessage{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{
							ID: "call_1",
							Function: llm.ToolCallFunction{
								Name:      "web_search",
								Arguments: map[string]any{"query": "RenCrow"},
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
			// 2回目: 最終応答
			{
				Message:      llm.ChatMessage{Role: "assistant", Content: "検索結果: RenCrowはAIアシスタントです"},
				FinishReason: "stop",
			},
		},
	}

	runner := &mockRunnerV2{
		results: map[string]*tool.ToolResponse{
			"web_search": tool.NewSuccess("RenCrow is an AI assistant"),
		},
	}

	result, err := Run(context.Background(), provider, runner, nil,
		[]llm.ChatMessage{{Role: "user", Content: "RenCrowを検索して"}},
		Config{MaxIterations: 10})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "検索結果: RenCrowはAIアシスタントです" {
		t.Errorf("unexpected result: %s", result)
	}
	if provider.callIndex != 2 {
		t.Errorf("expected 2 Chat calls, got %d", provider.callIndex)
	}
}

func TestRun_MultipleIterations(t *testing.T) {
	provider := &mockToolCallingProvider{
		responses: []llm.ChatResponse{
			// 1回目: file_read
			{
				Message: llm.ChatMessage{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{ID: "c1", Function: llm.ToolCallFunction{Name: "file_read", Arguments: map[string]any{"path": "/tmp/a"}}},
					},
				},
				FinishReason: "tool_calls",
			},
			// 2回目: file_write
			{
				Message: llm.ChatMessage{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{ID: "c2", Function: llm.ToolCallFunction{Name: "file_write", Arguments: map[string]any{"path": "/tmp/b", "content": "hello"}}},
					},
				},
				FinishReason: "tool_calls",
			},
			// 3回目: 完了
			{
				Message:      llm.ChatMessage{Role: "assistant", Content: "完了しました"},
				FinishReason: "stop",
			},
		},
	}

	runner := &mockRunnerV2{
		results: map[string]*tool.ToolResponse{
			"file_read":  tool.NewSuccess("file content"),
			"file_write": tool.NewSuccess("written"),
		},
	}

	result, err := Run(context.Background(), provider, runner, nil,
		[]llm.ChatMessage{{Role: "user", Content: "ファイル操作して"}},
		Config{MaxIterations: 10})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "完了しました" {
		t.Errorf("unexpected result: %s", result)
	}
	if provider.callIndex != 3 {
		t.Errorf("expected 3 Chat calls, got %d", provider.callIndex)
	}
}

func TestRun_MaxIterationsExceeded(t *testing.T) {
	// 常に tool_call を返し続ける
	provider := &mockToolCallingProvider{
		responses: []llm.ChatResponse{
			{
				Message: llm.ChatMessage{
					Role:    "assistant",
					Content: "途中結果",
					ToolCalls: []llm.ToolCall{
						{ID: "c1", Function: llm.ToolCallFunction{Name: "web_search", Arguments: map[string]any{"query": "a"}}},
					},
				},
				FinishReason: "tool_calls",
			},
			{
				Message: llm.ChatMessage{
					Role:    "assistant",
					Content: "まだ途中",
					ToolCalls: []llm.ToolCall{
						{ID: "c2", Function: llm.ToolCallFunction{Name: "web_search", Arguments: map[string]any{"query": "b"}}},
					},
				},
				FinishReason: "tool_calls",
			},
		},
	}

	runner := &mockRunnerV2{
		results: map[string]*tool.ToolResponse{
			"web_search": tool.NewSuccess("result"),
		},
	}

	result, err := Run(context.Background(), provider, runner, nil,
		[]llm.ChatMessage{{Role: "user", Content: "test"}},
		Config{MaxIterations: 2})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// MaxIterations超過時は最後のassistant contentを返す
	if result != "まだ途中" {
		t.Errorf("expected 'まだ途中', got '%s'", result)
	}
}

func TestRun_ToolExecutionError(t *testing.T) {
	provider := &mockToolCallingProvider{
		responses: []llm.ChatResponse{
			// 1回目: 未知のツール呼び出し
			{
				Message: llm.ChatMessage{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{ID: "c1", Function: llm.ToolCallFunction{Name: "nonexistent", Arguments: map[string]any{}}},
					},
				},
				FinishReason: "tool_calls",
			},
			// 2回目: エラーフィードバックを受けて通常応答
			{
				Message:      llm.ChatMessage{Role: "assistant", Content: "ツールが見つかりませんでした"},
				FinishReason: "stop",
			},
		},
	}

	runner := &mockRunnerV2{results: map[string]*tool.ToolResponse{}}

	result, err := Run(context.Background(), provider, runner, nil,
		[]llm.ChatMessage{{Role: "user", Content: "test"}},
		Config{MaxIterations: 10})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ツールが見つかりませんでした" {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestRun_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 即キャンセル

	provider := &mockToolCallingProvider{
		responses: []llm.ChatResponse{
			{Message: llm.ChatMessage{Role: "assistant", Content: "should not reach"}, FinishReason: "stop"},
		},
	}

	_, err := Run(ctx, provider, &mockRunnerV2{}, nil,
		[]llm.ChatMessage{{Role: "user", Content: "test"}},
		Config{MaxIterations: 10})

	if err == nil {
		t.Fatal("expected context cancelled error")
	}
}

func TestRun_DefaultMaxIterations(t *testing.T) {
	cfg := Config{} // MaxIterations = 0
	if cfg.maxIterations() != 10 {
		t.Errorf("default maxIterations should be 10, got %d", cfg.maxIterations())
	}

	cfg2 := Config{MaxIterations: 5}
	if cfg2.maxIterations() != 5 {
		t.Errorf("maxIterations should be 5, got %d", cfg2.maxIterations())
	}
}
