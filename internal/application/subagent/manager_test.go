package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/toolloop"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/agent"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/capability"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	domainsuperagent "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/superagent"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
)

// --- モック ---

type mockProvider struct {
	responses []llm.ChatResponse
	callIndex int
	lastReq   llm.ChatRequest
}

func (m *mockProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	return llm.GenerateResponse{}, fmt.Errorf("not implemented")
}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	m.lastReq = req
	if m.callIndex >= len(m.responses) {
		return llm.ChatResponse{}, fmt.Errorf("no more responses")
	}
	resp := m.responses[m.callIndex]
	m.callIndex++
	return resp, nil
}

type mockRunner struct {
	results map[string]*tool.ToolResponse
}

func (m *mockRunner) ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
	if r, ok := m.results[toolName]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("unknown tool: %s", toolName)
}

func (m *mockRunner) ListTools(ctx context.Context) ([]tool.ToolMetadata, error) {
	return nil, nil
}

type mockSuperAgentRecorder struct {
	tasks  []domainsuperagent.SubagentTask
	events []domainsuperagent.TraceEvent
}

func (m *mockSuperAgentRecorder) SaveSubagentTask(_ context.Context, item domainsuperagent.SubagentTask) error {
	if err := domainsuperagent.ValidateSubagentTask(item); err != nil {
		return err
	}
	m.tasks = append(m.tasks, item)
	return nil
}

func (m *mockSuperAgentRecorder) SaveTraceEvent(_ context.Context, item domainsuperagent.TraceEvent) error {
	if err := domainsuperagent.ValidateTraceEvent(item); err != nil {
		return err
	}
	m.events = append(m.events, item)
	return nil
}

// --- テスト ---

func TestRunSync_Success(t *testing.T) {
	provider := &mockProvider{
		responses: []llm.ChatResponse{
			{
				Message: llm.ChatMessage{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{ID: "c1", Function: llm.ToolCallFunction{Name: "web_search", Arguments: map[string]any{"query": "test"}}},
					},
				},
				FinishReason: "tool_calls",
			},
			{
				Message:      llm.ChatMessage{Role: "assistant", Content: "検索完了しました"},
				FinishReason: "stop",
			},
		},
	}

	runner := &mockRunner{
		results: map[string]*tool.ToolResponse{
			"web_search": tool.NewSuccess("search result"),
		},
	}

	mgr := NewManager(provider, runner, nil, toolloop.Config{MaxIterations: 10})
	result, err := mgr.RunSync(context.Background(), agent.SubagentTask{
		AgentName:   "worker",
		Instruction: "testを検索して",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.AgentName != "worker" {
		t.Errorf("expected agent name 'worker', got '%s'", result.AgentName)
	}
	if result.Output != "検索完了しました" {
		t.Errorf("expected output '検索完了しました', got '%s'", result.Output)
	}
}

func TestRunSync_WithSystemPrompt(t *testing.T) {
	provider := &mockProvider{
		responses: []llm.ChatResponse{
			{
				Message:      llm.ChatMessage{Role: "assistant", Content: "done"},
				FinishReason: "stop",
			},
		},
	}

	mgr := NewManager(provider, &mockRunner{}, nil, toolloop.Config{MaxIterations: 10})
	_, err := mgr.RunSync(context.Background(), agent.SubagentTask{
		AgentName:    "worker",
		Instruction:  "do something",
		SystemPrompt: "You are a custom agent.",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Chat に渡されたメッセージの先頭が custom system prompt であること
	if len(provider.lastReq.Messages) < 1 {
		t.Fatal("expected at least 1 message")
	}
	if provider.lastReq.Messages[0].Content != "You are a custom agent." {
		t.Errorf("expected custom system prompt, got '%s'", provider.lastReq.Messages[0].Content)
	}
}

func TestRunSync_RecordsSuperAgentSubagentTask(t *testing.T) {
	provider := &mockProvider{
		responses: []llm.ChatResponse{
			{
				Message:      llm.ChatMessage{Role: "assistant", Content: "done"},
				FinishReason: "stop",
			},
		},
	}
	recorder := &mockSuperAgentRecorder{}
	mgr := NewManager(provider, &mockRunner{}, nil, toolloop.Config{MaxIterations: 10}, WithSuperAgentRecorder(recorder))
	ctx := WithSuperAgentRuntime(context.Background(), "run_lead_1", []string{"session:s1", "route:CHAT"}, []string{"readFile"}, "return summary")
	result, err := mgr.RunSync(ctx, agent.SubagentTask{
		AgentName:   "worker",
		Instruction: "do something",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output != "done" {
		t.Fatalf("RunSync() output = %q", result.Output)
	}
	if len(recorder.tasks) != 2 {
		t.Fatalf("expected start and completed tasks, got %#v", recorder.tasks)
	}
	if recorder.tasks[0].Status != "running" || recorder.tasks[1].Status != "completed" {
		t.Fatalf("unexpected task statuses: %#v", recorder.tasks)
	}
	if recorder.tasks[0].ParentRunID != "run_lead_1" || recorder.tasks[0].Scope[0] != "session:s1" {
		t.Fatalf("unexpected task linkage: %#v", recorder.tasks[0])
	}
	if len(recorder.events) != 2 || recorder.events[0].EventType != "subagent_started" || recorder.events[1].EventType != "subagent_completed" {
		t.Fatalf("unexpected trace events: %#v", recorder.events)
	}
}

func TestRunSync_SuperAgentRecorderWithoutParentRunDoesNothing(t *testing.T) {
	provider := &mockProvider{
		responses: []llm.ChatResponse{
			{
				Message:      llm.ChatMessage{Role: "assistant", Content: "done"},
				FinishReason: "stop",
			},
		},
	}
	recorder := &mockSuperAgentRecorder{}
	mgr := NewManager(provider, &mockRunner{}, nil, toolloop.Config{MaxIterations: 10}, WithSuperAgentRecorder(recorder))
	if _, err := mgr.RunSync(context.Background(), agent.SubagentTask{AgentName: "worker", Instruction: "do something"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recorder.tasks) != 0 || len(recorder.events) != 0 {
		t.Fatalf("expected no superagent records without parent run, got tasks=%#v events=%#v", recorder.tasks, recorder.events)
	}
}

func TestRunSync_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	provider := &mockProvider{
		responses: []llm.ChatResponse{
			{Message: llm.ChatMessage{Role: "assistant", Content: "x"}, FinishReason: "stop"},
		},
	}

	mgr := NewManager(provider, &mockRunner{}, nil, toolloop.Config{MaxIterations: 10})
	_, err := mgr.RunSync(ctx, agent.SubagentTask{
		AgentName:   "worker",
		Instruction: "test",
	})

	if err == nil {
		t.Fatal("expected context cancelled error")
	}
}

func TestRunSync_EmptyInstruction(t *testing.T) {
	mgr := NewManager(nil, nil, nil, toolloop.Config{})
	_, err := mgr.RunSync(context.Background(), agent.SubagentTask{
		AgentName:   "worker",
		Instruction: "",
	})

	if err == nil {
		t.Fatal("expected error for empty instruction")
	}
}

// --- ToolRegistry モック ---

type mockRegistry struct {
	entries map[string]capability.ToolEntry
}

func (r *mockRegistry) Register(ctx context.Context, entry capability.ToolEntry) error {
	r.entries[entry.Name] = entry
	return nil
}

func (r *mockRegistry) ListForPlatform(ctx context.Context, platform string) ([]capability.ToolEntry, error) {
	var result []capability.ToolEntry
	for _, e := range r.entries {
		result = append(result, e)
	}
	return result, nil
}

func (r *mockRegistry) Get(ctx context.Context, name string) (capability.ToolEntry, error) {
	e, ok := r.entries[name]
	if !ok {
		return capability.ToolEntry{}, fmt.Errorf("not found: %s", name)
	}
	return e, nil
}

func (r *mockRegistry) Close() error { return nil }

func makeSchemaJSON(t *testing.T, name, description string) string {
	t.Helper()
	toolDef := llm.ToolDefinition{
		Type: "function",
		Function: llm.ToolFunctionDef{
			Name:        name,
			Description: description,
			Parameters:  map[string]any{"type": "object"},
		},
	}
	b, err := json.Marshal(toolDef)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMergeToolDefs_NilRegistry_ReturnsBaseDefs(t *testing.T) {
	baseDefs := []llm.ToolDefinition{
		{Type: "function", Function: llm.ToolFunctionDef{Name: "shell"}},
	}
	mgr := NewManager(&mockProvider{}, &mockRunner{}, baseDefs, toolloop.Config{})

	merged := mgr.mergeToolDefs(context.Background())
	if len(merged) != 1 || merged[0].Function.Name != "shell" {
		t.Errorf("expected only base defs, got %v", merged)
	}
}

func TestMergeToolDefs_WithRegistry_MergesApprovedTools(t *testing.T) {
	baseDefs := []llm.ToolDefinition{
		{Type: "function", Function: llm.ToolFunctionDef{Name: "shell"}},
	}
	registry := &mockRegistry{
		entries: map[string]capability.ToolEntry{
			"custom_tool": {
				Name:       "custom_tool",
				SchemaJSON: makeSchemaJSON(t, "custom_tool", "a custom tool"),
				CreatedAt:  time.Now(),
			},
		},
	}
	mgr := NewManager(&mockProvider{}, &mockRunner{}, baseDefs, toolloop.Config{}, WithToolRegistry(registry))

	merged := mgr.mergeToolDefs(context.Background())
	names := make(map[string]bool)
	for _, d := range merged {
		names[d.Function.Name] = true
	}
	if !names["shell"] {
		t.Error("expected 'shell' in merged defs")
	}
	if !names["custom_tool"] {
		t.Error("expected 'custom_tool' in merged defs")
	}
}

func TestMergeToolDefs_Dedup_BaseToolWins(t *testing.T) {
	baseDefs := []llm.ToolDefinition{
		{Type: "function", Function: llm.ToolFunctionDef{Name: "shell", Description: "base shell"}},
	}
	registry := &mockRegistry{
		entries: map[string]capability.ToolEntry{
			"shell": {
				Name:       "shell",
				SchemaJSON: makeSchemaJSON(t, "shell", "registry shell"),
				CreatedAt:  time.Now(),
			},
		},
	}
	mgr := NewManager(&mockProvider{}, &mockRunner{}, baseDefs, toolloop.Config{}, WithToolRegistry(registry))

	merged := mgr.mergeToolDefs(context.Background())
	if len(merged) != 1 {
		t.Errorf("expected 1 tool after dedup, got %d", len(merged))
	}
	if merged[0].Function.Description != "base shell" {
		t.Errorf("expected base tool to win, got description: %q", merged[0].Function.Description)
	}
}

func TestMergeToolDefs_InvalidSchemaJSON_Skipped(t *testing.T) {
	baseDefs := []llm.ToolDefinition{
		{Type: "function", Function: llm.ToolFunctionDef{Name: "shell"}},
	}
	registry := &mockRegistry{
		entries: map[string]capability.ToolEntry{
			"broken_tool": {
				Name:       "broken_tool",
				SchemaJSON: "not valid json",
				CreatedAt:  time.Now(),
			},
		},
	}
	mgr := NewManager(&mockProvider{}, &mockRunner{}, baseDefs, toolloop.Config{}, WithToolRegistry(registry))

	merged := mgr.mergeToolDefs(context.Background())
	if len(merged) != 1 {
		t.Errorf("expected broken tool to be skipped, got %d tools", len(merged))
	}
}
