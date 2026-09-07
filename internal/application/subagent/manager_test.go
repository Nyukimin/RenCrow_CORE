package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/toolloop"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	actionstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// --- モック ---

type mockProvider struct {
	responses []llm.ChatResponse
	callIndex int
	lastReq   llm.ChatRequest
	contexts  []context.Context
}

func (m *mockProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	return llm.GenerateResponse{}, fmt.Errorf("not implemented")
}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	m.contexts = append(m.contexts, ctx)
	m.lastReq = req
	if m.callIndex >= len(m.responses) {
		return llm.ChatResponse{}, fmt.Errorf("no more responses")
	}
	resp := m.responses[m.callIndex]
	m.callIndex++
	return resp, nil
}

type mockRunner struct {
	results  map[string]*tool.ToolResponse
	contexts []context.Context
}

func (m *mockRunner) ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
	m.contexts = append(m.contexts, ctx)
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
	events []modulecore.EventEnvelope
}

func (m *mockSuperAgentRecorder) SaveSubagentTask(_ context.Context, item domainsuperagent.SubagentTask) error {
	if err := domainsuperagent.ValidateSubagentTask(item); err != nil {
		return err
	}
	m.tasks = append(m.tasks, item)
	return nil
}

func (m *mockSuperAgentRecorder) Append(_ context.Context, item modulecore.EventEnvelope) error {
	if err := modulecore.ValidateEventEnvelope(item); err != nil {
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

func TestRunSync_PreservesToolExecutionScopeThroughDelegation(t *testing.T) {
	provider := &mockProvider{
		responses: []llm.ChatResponse{
			{
				Message: llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{
					{ID: "scope-1", Function: llm.ToolCallFunction{Name: "delegated_tool", Arguments: map[string]any{}}},
				}},
				FinishReason: "tool_calls",
			},
			{Message: llm.ChatMessage{Role: "assistant", Content: "delegated"}, FinishReason: "stop"},
		},
	}
	runner := &mockRunner{results: map[string]*tool.ToolResponse{"delegated_tool": tool.NewSuccess("ok")}}
	scope, err := tool.NewToolExecutionScope(
		"delegated-request",
		tool.ActorKindAgent,
		"mio",
		"user-a",
		[]string{tool.DataScopeUser},
		tool.AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope() error = %v", err)
	}
	mgr := NewManager(provider, runner, nil, toolloop.Config{MaxIterations: 3})
	if _, err := mgr.RunSync(tool.WithToolExecutionScope(context.Background(), scope), agent.SubagentTask{
		AgentName: "worker", Instruction: "delegated search",
	}); err != nil {
		t.Fatalf("RunSync() error = %v", err)
	}
	if len(runner.contexts) != 1 {
		t.Fatalf("delegated tool contexts = %d, want 1", len(runner.contexts))
	}
	got, ok := tool.ToolExecutionScopeFromContext(runner.contexts[0])
	if !ok || got.RequestID != scope.RequestID || got.AuthenticatedUserID != scope.AuthenticatedUserID || got.ActorID != scope.ActorID {
		t.Fatalf("delegated scope = %#v, found=%t; want %#v", got, ok, scope)
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
	store, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(provider, &mockRunner{}, nil, toolloop.Config{MaxIterations: 10, Actions: actionmanager.New(store)}, WithSuperAgentRecorder(recorder))
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	traceID := modulecore.NewTraceID()
	causationEventID := modulecore.NewEventID()
	ctx := WithSuperAgentRuntime(
		context.Background(),
		taskID,
		runID,
		"shiro",
		traceID,
		causationEventID,
		[]string{"session:s1", "route:CHAT"},
		[]string{"readFile"},
		"return summary",
	)
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
	if recorder.tasks[0].TaskID != taskID || recorder.tasks[1].TaskID != taskID || recorder.tasks[0].RunID != runID || recorder.tasks[1].RunID != runID || recorder.tasks[0].ActorID != "shiro" || recorder.tasks[1].ActorID != "shiro" || recorder.tasks[0].Scope[0] != "session:s1" {
		t.Fatalf("unexpected task linkage: %#v", recorder.tasks[0])
	}
	if len(recorder.events) != 2 || recorder.events[0].EventType != "subagent.started" || recorder.events[1].EventType != "subagent.completed" || recorder.events[1].CausationEventID != recorder.events[0].EventID {
		t.Fatalf("unexpected trace events: %#v", recorder.events)
	}
	for _, event := range recorder.events {
		if event.TraceID != traceID || event.TaskID != taskID || event.RunID != runID || event.ActorKind != "agent" || event.ActorID != "shiro" {
			t.Fatalf("event identity = %#v, want task=%s run=%s trace=%s actor=shiro", event, taskID, runID, traceID)
		}
		for _, key := range []string{"task_id", "run_id", "actor_id", "actor_label", "subagent_id", "task_reference", "run_reference"} {
			if _, ok := event.Payload[key]; ok {
				t.Fatalf("event payload duplicated identity %q: %#v", key, event.Payload)
			}
		}
	}
	if recorder.events[0].CausationEventID != causationEventID {
		t.Fatalf("start event causation = %s, want %s", recorder.events[0].CausationEventID, causationEventID)
	}
}

func TestRunSync_SuperAgentRecorderRequiresCanonicalRuntimeContext(t *testing.T) {
	provider := &mockProvider{
		responses: []llm.ChatResponse{
			{
				Message:      llm.ChatMessage{Role: "assistant", Content: "done"},
				FinishReason: "stop",
			},
		},
	}
	recorder := &mockSuperAgentRecorder{}
	runner := &mockRunner{}
	mgr := NewManager(provider, runner, nil, toolloop.Config{MaxIterations: 10}, WithSuperAgentRecorder(recorder))
	if _, err := mgr.RunSync(context.Background(), agent.SubagentTask{AgentName: "worker", Instruction: "do something"}); err == nil {
		t.Fatal("expected missing canonical runtime context error")
	}
	if provider.callIndex != 0 || len(runner.contexts) != 0 || len(recorder.tasks) != 0 || len(recorder.events) != 0 {
		t.Fatalf("recorder validation must fail before ToolLoop: provider_calls=%d tool_calls=%d tasks=%#v events=%#v", provider.callIndex, len(runner.contexts), recorder.tasks, recorder.events)
	}
}

func TestRunSync_SuperAgentRecorderRejectsInvalidCanonicalRuntimeContextBeforeToolLoop(t *testing.T) {
	validTaskID := modulecore.NewTaskID()
	validRunID := modulecore.NewRunID()
	validTraceID := modulecore.NewTraceID()
	validCausationEventID := modulecore.NewEventID()
	tests := []struct {
		name      string
		taskID    modulecore.TaskID
		runID     modulecore.RunID
		actorID   string
		traceID   modulecore.TraceID
		causation modulecore.EventID
	}{
		{name: "task", taskID: "legacy-task", runID: validRunID, actorID: "mio", traceID: validTraceID, causation: validCausationEventID},
		{name: "run", taskID: validTaskID, runID: "legacy-run", actorID: "mio", traceID: validTraceID, causation: validCausationEventID},
		{name: "actor", taskID: validTaskID, runID: validRunID, actorID: "worker", traceID: validTraceID, causation: validCausationEventID},
		{name: "non canonical actor spelling", taskID: validTaskID, runID: validRunID, actorID: "Shiro", traceID: validTraceID, causation: validCausationEventID},
		{name: "trace", taskID: validTaskID, runID: validRunID, actorID: "mio", traceID: "legacy-trace", causation: validCausationEventID},
		{name: "causation", taskID: validTaskID, runID: validRunID, actorID: "mio", traceID: validTraceID, causation: "legacy-event"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &mockProvider{responses: []llm.ChatResponse{{Message: llm.ChatMessage{Role: "assistant", Content: "must not run"}, FinishReason: "stop"}}}
			runner := &mockRunner{}
			recorder := &mockSuperAgentRecorder{}
			mgr := NewManager(provider, runner, nil, toolloop.Config{MaxIterations: 10}, WithSuperAgentRecorder(recorder))
			ctx := WithSuperAgentRuntime(context.Background(), tt.taskID, tt.runID, tt.actorID, tt.traceID, tt.causation, nil, nil, "")
			if _, err := mgr.RunSync(ctx, agent.SubagentTask{AgentName: "worker", Instruction: "do something"}); err == nil {
				t.Fatal("expected invalid canonical runtime context error")
			}
			if provider.callIndex != 0 || len(runner.contexts) != 0 || len(recorder.tasks) != 0 || len(recorder.events) != 0 {
				t.Fatalf("invalid context must fail before ToolLoop: provider_calls=%d tool_calls=%d tasks=%#v events=%#v", provider.callIndex, len(runner.contexts), recorder.tasks, recorder.events)
			}
		})
	}
}

func TestRunSyncBindsSuperAgentRuntimeIdentityThroughToolLoop(t *testing.T) {
	for _, existing := range []struct {
		name string
		bind bool
	}{
		{name: "runtime-only"},
		{name: "exact-existing", bind: true},
	} {
		for _, withRecorder := range []bool{false, true} {
			existing, withRecorder := existing, withRecorder
			t.Run(existing.name+"/recorder="+fmt.Sprint(withRecorder), func(t *testing.T) {
				taskID := modulecore.NewTaskID()
				runID := modulecore.NewRunID()
				traceID := modulecore.NewTraceID()
				causationEventID := modulecore.NewEventID()
				base := context.Background()
				if existing.bind {
					var err error
					base, err = domainexecution.WithIdentity(base, taskID, runID, traceID)
					if err != nil {
						t.Fatal(err)
					}
				}
				ctx := WithSuperAgentRuntime(base, taskID, runID, "shiro", traceID, causationEventID, nil, nil, "return summary")

				provider := &mockProvider{responses: superAgentToolLoopResponses()}
				runner := &mockRunner{results: map[string]*tool.ToolResponse{"delegated_tool": tool.NewSuccess("tool result")}}
				store, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
				if err != nil {
					t.Fatal(err)
				}
				actions := actionmanager.New(store)
				var recorder *mockSuperAgentRecorder
				opts := make([]ManagerOption, 0, 1)
				if withRecorder {
					recorder = &mockSuperAgentRecorder{}
					opts = append(opts, WithSuperAgentRecorder(recorder))
				}
				mgr := NewManager(provider, runner, nil, toolloop.Config{MaxIterations: 3, Actions: actions}, opts...)

				result, err := mgr.RunSync(ctx, agent.SubagentTask{AgentName: "worker", Instruction: "delegate the search"})
				if err != nil {
					t.Fatalf("RunSync: %v", err)
				}
				if result.Output != "done" {
					t.Fatalf("result output = %q", result.Output)
				}
				if len(provider.contexts) != 2 {
					t.Fatalf("provider contexts = %d, want 2", len(provider.contexts))
				}
				for _, observed := range provider.contexts {
					assertSuperAgentExecutionIdentity(t, observed, taskID, runID, traceID)
				}
				if len(runner.contexts) != 1 {
					t.Fatalf("tool contexts = %d, want 1", len(runner.contexts))
				}
				assertSuperAgentExecutionIdentity(t, runner.contexts[0], taskID, runID, traceID)
				stored, err := actions.ListActions(context.Background(), domainaction.Filter{TaskID: taskID, RunID: runID})
				if err != nil {
					t.Fatalf("ListActions: %v", err)
				}
				if len(stored) != 1 || stored[0].TaskID != taskID || stored[0].RunID != runID {
					t.Fatalf("stored actions = %#v", stored)
				}
				if withRecorder {
					if len(recorder.tasks) != 2 || len(recorder.events) != 2 {
						t.Fatalf("recorder entries: tasks=%d events=%d", len(recorder.tasks), len(recorder.events))
					}
					for _, event := range recorder.events {
						if event.TaskID != taskID || event.RunID != runID || event.TraceID != traceID {
							t.Fatalf("recorder event identity = %#v", event)
						}
					}
				} else if recorder != nil {
					t.Fatal("recorder unexpectedly configured")
				}
			})
		}
	}
}

func TestRunSyncRejectsExistingExecutionIdentityMismatchBeforeSideEffects(t *testing.T) {
	boundTaskID := modulecore.NewTaskID()
	boundRunID := modulecore.NewRunID()
	boundTraceID := modulecore.NewTraceID()
	validCausationEventID := modulecore.NewEventID()
	for _, tc := range []struct {
		name  string
		task  modulecore.TaskID
		run   modulecore.RunID
		trace modulecore.TraceID
	}{
		{name: "task", task: modulecore.NewTaskID(), run: boundRunID, trace: boundTraceID},
		{name: "run", task: boundTaskID, run: modulecore.NewRunID(), trace: boundTraceID},
		{name: "trace", task: boundTaskID, run: boundRunID, trace: modulecore.NewTraceID()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, err := domainexecution.WithIdentity(context.Background(), boundTaskID, boundRunID, boundTraceID)
			if err != nil {
				t.Fatal(err)
			}
			ctx := WithSuperAgentRuntime(base, tc.task, tc.run, "shiro", tc.trace, validCausationEventID, nil, nil, "return summary")
			provider := &mockProvider{responses: superAgentToolLoopResponses()}
			runner := &mockRunner{results: map[string]*tool.ToolResponse{"delegated_tool": tool.NewSuccess("must not run")}}
			store, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
			if err != nil {
				t.Fatal(err)
			}
			actions := actionmanager.New(store)
			recorder := &mockSuperAgentRecorder{}
			mgr := NewManager(provider, runner, nil, toolloop.Config{MaxIterations: 3, Actions: actions}, WithSuperAgentRecorder(recorder))

			if _, err := mgr.RunSync(ctx, agent.SubagentTask{AgentName: "worker", Instruction: "must not run"}); err == nil {
				t.Fatal("expected existing execution identity mismatch")
			}
			if provider.callIndex != 0 || len(provider.contexts) != 0 || len(runner.contexts) != 0 {
				t.Fatalf("mismatch reached execution: provider_calls=%d provider_contexts=%d tool_calls=%d", provider.callIndex, len(provider.contexts), len(runner.contexts))
			}
			if len(recorder.tasks) != 0 || len(recorder.events) != 0 {
				t.Fatalf("mismatch created recorder entries: tasks=%#v events=%#v", recorder.tasks, recorder.events)
			}
			stored, err := actions.ListActions(context.Background(), domainaction.Filter{TaskID: tc.task, RunID: tc.run})
			if err != nil {
				t.Fatalf("ListActions: %v", err)
			}
			if len(stored) != 0 {
				t.Fatalf("mismatch created actions: %#v", stored)
			}
		})
	}
}

func TestRunSyncRejectsInvalidSuperAgentRuntimeTraceWithoutRecorder(t *testing.T) {
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	ctx := WithSuperAgentRuntime(context.Background(), taskID, runID, "shiro", "legacy-trace", modulecore.NewEventID(), nil, nil, "return summary")
	provider := &mockProvider{responses: superAgentToolLoopResponses()}
	runner := &mockRunner{results: map[string]*tool.ToolResponse{"delegated_tool": tool.NewSuccess("must not run")}}
	store, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	actions := actionmanager.New(store)
	mgr := NewManager(provider, runner, nil, toolloop.Config{MaxIterations: 3, Actions: actions})

	if _, err := mgr.RunSync(ctx, agent.SubagentTask{AgentName: "worker", Instruction: "must not run"}); err == nil {
		t.Fatal("expected invalid runtime trace error")
	}
	if provider.callIndex != 0 || len(provider.contexts) != 0 || len(runner.contexts) != 0 {
		t.Fatalf("invalid runtime trace reached execution: provider_calls=%d provider_contexts=%d tool_calls=%d", provider.callIndex, len(provider.contexts), len(runner.contexts))
	}
	stored, err := actions.ListActions(context.Background(), domainaction.Filter{TaskID: taskID, RunID: runID})
	if err != nil {
		t.Fatalf("ListActions: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("invalid runtime trace created actions: %#v", stored)
	}
}

func TestRunSyncNilContextReturnsError(t *testing.T) {
	provider := &mockProvider{responses: []llm.ChatResponse{{Message: llm.ChatMessage{Role: "assistant", Content: "must not run"}, FinishReason: "stop"}}}
	mgr := NewManager(provider, &mockRunner{}, nil, toolloop.Config{MaxIterations: 1})
	var runErr error
	var panicValue any
	func() {
		defer func() {
			panicValue = recover()
		}()
		_, runErr = mgr.RunSync(nil, agent.SubagentTask{AgentName: "worker", Instruction: "must not run"})
	}()
	if panicValue != nil {
		t.Fatalf("RunSync panicked for nil context: %v", panicValue)
	}
	if runErr == nil {
		t.Fatal("expected nil context error")
	}
	if provider.callIndex != 0 {
		t.Fatalf("nil context reached provider: %d calls", provider.callIndex)
	}
}

func superAgentToolLoopResponses() []llm.ChatResponse {
	return []llm.ChatResponse{
		{
			Message: llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{
				{ID: "delegated-call", Function: llm.ToolCallFunction{Name: "delegated_tool", Arguments: map[string]any{}}},
			}},
			FinishReason: "tool_calls",
		},
		{Message: llm.ChatMessage{Role: "assistant", Content: "done"}, FinishReason: "stop"},
	}
}

func assertSuperAgentExecutionIdentity(t *testing.T, ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, traceID modulecore.TraceID) {
	t.Helper()
	got, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		t.Fatalf("IdentityFromContext: %v", err)
	}
	if got.TaskID != taskID || got.RunID != runID || got.TraceID != traceID {
		t.Fatalf("execution identity = %#v, want task=%s run=%s trace=%s", got, taskID, runID, traceID)
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

func TestMergeToolDefs_WithRegistry_MergesRegisteredTools(t *testing.T) {
	baseDefs := []llm.ToolDefinition{
		{Type: "function", Function: llm.ToolFunctionDef{Name: "shell"}},
	}
	registry := &mockRegistry{
		entries: map[string]capability.ToolEntry{
			"custom_tool": {
				Name:        "custom_tool",
				Description: "a custom tool",
				SchemaJSON:  makeSchemaJSON(t, "custom_tool", "a custom tool"),
				CreatedAt:   time.Now(),
			},
			"legacy_tool": {
				Name:        "legacy_tool",
				Description: "legacy description",
				SchemaJSON:  `{"type":"object","properties":{"query":{"type":"string"}}}`,
				CreatedAt:   time.Now(),
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
	if !names["custom_tool"] || !names["legacy_tool"] {
		t.Errorf("expected custom_tool and legacy_tool in merged defs: %v", names)
	}
	for _, definition := range merged {
		if definition.Function.Name == "legacy_tool" {
			wantParameters := map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}
			if definition.Type != "function" || definition.Function.Description != "legacy description" || !reflect.DeepEqual(definition.Function.Parameters, wantParameters) {
				t.Fatalf("legacy definition = %+v, want trusted named function", definition)
			}
		}
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

func TestMergeToolDefs_InvalidOrMismatchedDefinitionsSkipped(t *testing.T) {
	baseDefs := []llm.ToolDefinition{{Type: "function", Function: llm.ToolFunctionDef{Name: "shell"}}}
	registry := &mockRegistry{entries: map[string]capability.ToolEntry{
		"broken_json":    {Name: "broken_json", Description: "broken", SchemaJSON: "not valid json"},
		"wrong_type":     {Name: "wrong_type", Description: "wrong", SchemaJSON: `{"type":"object","function":{"name":"wrong_type","description":"wrong","parameters":{}}}`},
		"wrong_name":     {Name: "wrong_name", Description: "wrong", SchemaJSON: `{"type":"function","function":{"name":"other","description":"wrong","parameters":{}}}`},
		"partial":        {Name: "partial", Description: "partial", SchemaJSON: `{"type":"function"}`},
		"nil_parameters": {Name: "nil_parameters", Description: "nil", SchemaJSON: `{"type":"function","function":{"name":"nil_parameters","description":"nil","parameters":null}}`},
		"empty_desc":     {Name: "empty_desc", Description: " ", SchemaJSON: `{"type":"object"}`},
		"empty_name":     {Name: " ", Description: "name", SchemaJSON: `{"type":"object"}`},
	}}
	mgr := NewManager(&mockProvider{}, &mockRunner{}, baseDefs, toolloop.Config{}, WithToolRegistry(registry))
	merged := mgr.mergeToolDefs(context.Background())
	if len(merged) != 1 || merged[0].Function.Name != "shell" {
		t.Fatalf("invalid registry definitions were exposed: %#v", merged)
	}
}
