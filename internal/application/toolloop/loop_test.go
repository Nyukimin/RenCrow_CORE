package toolloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	actionstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// --- モック ---

type mockToolCallingProvider struct {
	responses []llm.ChatResponse
	callIndex int
	requests  []llm.ChatRequest
}

func (m *mockToolCallingProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	return llm.GenerateResponse{}, fmt.Errorf("not implemented")
}

func (m *mockToolCallingProvider) Name() string { return "mock" }

func (m *mockToolCallingProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	m.requests = append(m.requests, req)
	if m.callIndex >= len(m.responses) {
		return llm.ChatResponse{}, fmt.Errorf("no more mock responses (called %d times)", m.callIndex+1)
	}
	resp := m.responses[m.callIndex]
	m.callIndex++
	return resp, nil
}

func TestRun_UsesLowReasoningOnEveryIteration(t *testing.T) {
	provider := &mockToolCallingProvider{responses: []llm.ChatResponse{
		{Message: llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call-1", Function: llm.ToolCallFunction{Name: "test", Arguments: map[string]any{}}}}}, FinishReason: "tool_calls"},
		{Message: llm.ChatMessage{Role: "assistant", Content: "done"}, FinishReason: "stop"},
	}}
	runner := &mockRunnerV2{results: map[string]*tool.ToolResponse{"test": tool.NewSuccess("ok")}}

	if _, err := Run(context.Background(), provider, runner, nil, []llm.ChatMessage{{Role: "user", Content: "run"}}, Config{}); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(provider.requests))
	}
	for i, request := range provider.requests {
		if request.ReasoningEffort != llm.ReasoningEffortLow {
			t.Errorf("request %d reasoning effort = %q, want %q", i, request.ReasoningEffort, llm.ReasoningEffortLow)
		}
		if request.MaxTokens != 4096 {
			t.Errorf("request %d max tokens = %d, want 4096", i, request.MaxTokens)
		}
	}
}

func TestRun_UsesConfiguredMaxTokensOnEveryIteration(t *testing.T) {
	provider := &mockToolCallingProvider{responses: []llm.ChatResponse{
		{Message: llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call-1", Function: llm.ToolCallFunction{Name: "test", Arguments: map[string]any{}}}}}, FinishReason: "tool_calls"},
		{Message: llm.ChatMessage{Role: "assistant", Content: "done"}, FinishReason: "stop"},
	}}
	runner := &mockRunnerV2{results: map[string]*tool.ToolResponse{"test": tool.NewSuccess("ok")}}

	if _, err := Run(context.Background(), provider, runner, nil, []llm.ChatMessage{{Role: "user", Content: "run"}}, Config{MaxTokens: 1234}); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(provider.requests))
	}
	for i, request := range provider.requests {
		if request.MaxTokens != 1234 {
			t.Errorf("request %d max tokens = %d, want 1234", i, request.MaxTokens)
		}
		if request.ReasoningEffort != llm.ReasoningEffortLow {
			t.Errorf("request %d reasoning effort = %q, want %q", i, request.ReasoningEffort, llm.ReasoningEffortLow)
		}
	}
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
	if got := provider.requests[1].Messages[len(provider.requests[1].Messages)-1].Content; got != "RenCrow is an AI assistant" {
		t.Errorf("string tool result content = %q, want unchanged result", got)
	}
}

func TestRun_StructuredToolResultUsesCanonicalJSONForNextIteration(t *testing.T) {
	type dataWriteReceipt struct {
		OwnerRoute string `json:"owner_route"`
		AuditRef   string `json:"audit_ref"`
		RequestID  string `json:"-"`
	}
	provider := &mockToolCallingProvider{responses: []llm.ChatResponse{
		{
			Message: llm.ChatMessage{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{{
					ID:       "call-data-write",
					Function: llm.ToolCallFunction{Name: "data.write", Arguments: map[string]any{}},
				}},
			},
			FinishReason: "tool_calls",
		},
		{Message: llm.ChatMessage{Role: "assistant", Content: "done"}, FinishReason: "stop"},
	}}
	runner := &mockRunnerV2{results: map[string]*tool.ToolResponse{
		"data.write": tool.NewSuccess(dataWriteReceipt{
			OwnerRoute: "browser_trace_to_api/review_candidate",
			AuditRef:   "browser-validation/sha256:f662",
			RequestID:  "internal-request-id",
		}),
	}}

	if _, err := Run(context.Background(), provider, runner, nil, []llm.ChatMessage{{Role: "user", Content: "review"}}, Config{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(provider.requests))
	}
	messages := provider.requests[1].Messages
	if len(messages) == 0 || messages[len(messages)-1].Role != "tool" {
		t.Fatalf("second request messages = %#v, want trailing tool message", messages)
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(messages[len(messages)-1].Content), &content); err != nil {
		t.Fatalf("tool content = %q is not JSON: %v", messages[len(messages)-1].Content, err)
	}
	if content["owner_route"] != "browser_trace_to_api/review_candidate" || content["audit_ref"] != "browser-validation/sha256:f662" {
		t.Fatalf("structured tool content = %#v, want named owner_route/audit_ref", content)
	}
	if _, found := content["request_id"]; found {
		t.Fatalf("structured tool content leaked hidden request_id: %#v", content)
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

func TestRun_StopsBeforeRepeatingIdenticalFailedToolCall(t *testing.T) {
	provider := &mockToolCallingProvider{responses: []llm.ChatResponse{
		{
			Message: llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{
				ID: "c1", Function: llm.ToolCallFunction{Name: "person_related_catalog.collect", Arguments: map[string]any{"person_name": "Unknown", "category": "music"}},
			}}},
			FinishReason: "tool_calls",
		},
		{
			Message: llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{
				ID: "c2", Function: llm.ToolCallFunction{Name: "person_related_catalog.collect", Arguments: map[string]any{"category": "music", "person_name": "Unknown"}},
			}}},
			FinishReason: "tool_calls",
		},
	}}
	runner := &countingErrorRunner{}

	result, err := Run(context.Background(), provider, runner, nil,
		[]llm.ChatMessage{{Role: "user", Content: "collect"}}, Config{MaxIterations: 10})

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.HasPrefix(result, "blocked:") || !strings.Contains(result, "person_related_catalog.collect") {
		t.Fatalf("result = %q, want deterministic blocked result", result)
	}
	if provider.callIndex != 2 || runner.calls != 1 {
		t.Fatalf("provider calls=%d tool calls=%d, want 2/1", provider.callIndex, runner.calls)
	}
}

type countingErrorRunner struct{ calls int }

func (r *countingErrorRunner) ExecuteV2(context.Context, string, map[string]any) (*tool.ToolResponse, error) {
	r.calls++
	return tool.NewError(tool.ErrNotFound, "person not found", nil), nil
}

func (*countingErrorRunner) ListTools(context.Context) ([]tool.ToolMetadata, error) { return nil, nil }

func TestRun_RejectsPartialIdentity(t *testing.T) {
	_, err := Run(context.Background(), &mockToolCallingProvider{}, &mockRunnerV2{}, nil,
		[]llm.ChatMessage{{Role: "user", Content: "test"}},
		Config{TaskID: modulecore.NewTaskID()})
	if err == nil || !strings.Contains(err.Error(), "task_id and run_id") {
		t.Fatalf("Run() error = %v, want paired identity error", err)
	}
}

func TestRun_AcceptsEnclosingIdentity(t *testing.T) {
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	ctx, err := domainexecution.WithIdentity(context.Background(), taskID, runID, modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	store, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	actions := actionmanager.New(store)
	provider := &mockToolCallingProvider{responses: []llm.ChatResponse{
		{Message: llm.ChatMessage{Role: "assistant", Content: "done"}, FinishReason: "stop"},
	}}
	if _, err := Run(ctx, provider, &mockRunnerV2{}, nil,
		[]llm.ChatMessage{{Role: "user", Content: "test"}},
		Config{TaskID: taskID, RunID: runID, Actions: actions}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRun_RequiresActionsWhenIdentitySet(t *testing.T) {
	_, err := Run(context.Background(), &mockToolCallingProvider{}, &mockRunnerV2{}, nil,
		[]llm.ChatMessage{{Role: "user", Content: "test"}},
		Config{TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID()})
	if err == nil || !strings.Contains(err.Error(), "actions manager is required") {
		t.Fatalf("Run() error = %v, want actions manager requirement", err)
	}
}

func TestRun_CreatesDistinctActionsForSameToolNameCalls(t *testing.T) {
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	ctx, err := domainexecution.WithIdentity(context.Background(), taskID, runID, modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	store, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	actions := actionmanager.New(store)
	spy := &actionAttemptSpyRunner{results: map[string]*tool.ToolResponse{
		"web_search": tool.NewSuccess("ok"),
	}}
	provider := &mockToolCallingProvider{responses: []llm.ChatResponse{
		{
			Message: llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{
				ID: "provider-call-1", Function: llm.ToolCallFunction{Name: "web_search", Arguments: map[string]any{"query": "first"}},
			}}},
			FinishReason: "tool_calls",
		},
		{
			Message: llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{
				ID: "provider-call-2", Function: llm.ToolCallFunction{Name: "web_search", Arguments: map[string]any{"query": "second"}},
			}}},
			FinishReason: "tool_calls",
		},
		{Message: llm.ChatMessage{Role: "assistant", Content: "done"}, FinishReason: "stop"},
	}}

	if _, err := Run(ctx, provider, spy, nil,
		[]llm.ChatMessage{{Role: "user", Content: "search twice"}},
		Config{TaskID: taskID, RunID: runID, Actions: actions}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(spy.observations) != 2 {
		t.Fatalf("observations = %d, want 2", len(spy.observations))
	}
	if spy.observations[0].ActionID == spy.observations[1].ActionID {
		t.Fatalf("ActionID reused: %s", spy.observations[0].ActionID)
	}
	if spy.observations[0].AttemptID == spy.observations[1].AttemptID {
		t.Fatalf("AttemptID reused: %s", spy.observations[0].AttemptID)
	}
	if err := spy.observations[0].ActionID.Validate(); err != nil {
		t.Fatalf("action id: %v", err)
	}
	actionsFound, err := actions.ListActions(ctx, domainaction.Filter{TaskID: taskID, RunID: runID})
	if err != nil {
		t.Fatalf("ListActions() error = %v", err)
	}
	if len(actionsFound) != 2 {
		t.Fatalf("actions = %d, want 2", len(actionsFound))
	}
	for i, observation := range spy.observations {
		if observation.ActionID == "" || observation.AttemptID == "" {
			t.Fatalf("observation %d has incomplete binding: %#v", i, observation)
		}
		if action, err := actions.GetAction(ctx, observation.ActionID); err != nil {
			t.Fatalf("GetAction(%s) error = %v", observation.ActionID, err)
		} else if action.TaskID != taskID || action.RunID != runID {
			t.Fatalf("action %s owner = task=%s run=%s, want task=%s run=%s", action.ActionID, action.TaskID, action.RunID, taskID, runID)
		}
		attempts, err := actions.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: observation.ActionID})
		if err != nil {
			t.Fatalf("ListAttempts(%s) error = %v", observation.ActionID, err)
		}
		if len(attempts) != 1 {
			t.Fatalf("action %s attempts = %d, want one first attempt", observation.ActionID, len(attempts))
		}
		if attempts[0].AttemptID != observation.AttemptID || attempts[0].StartReason != domainaction.AttemptStartReasonFirst || attempts[0].Status != domainaction.AttemptStatusSucceeded {
			t.Fatalf("action %s attempt = %#v, want bound first attempt %s", observation.ActionID, attempts[0], observation.AttemptID)
		}
	}
	for _, observation := range spy.observations {
		action, err := actions.GetAction(ctx, observation.ActionID)
		if err != nil {
			t.Fatalf("GetAction(%s) error = %v", observation.ActionID, err)
		}
		if action.Status != domainaction.StatusSucceeded || action.Summary != "tool succeeded" {
			t.Fatalf("action %s status/summary = %s/%q, want succeeded/tool succeeded", action.ActionID, action.Status, action.Summary)
		}
	}

	toolMessages := make([]llm.ChatMessage, 0, 2)
	for _, request := range provider.requests[1:] {
		last := request.Messages[len(request.Messages)-1]
		if last.Role == "tool" {
			toolMessages = append(toolMessages, last)
		}
	}
	if len(toolMessages) != 2 {
		t.Fatalf("tool messages = %d, want 2", len(toolMessages))
	}
	if toolMessages[0].ProviderToolCallID != "provider-call-1" || toolMessages[1].ProviderToolCallID != "provider-call-2" {
		t.Fatalf("tool message ProviderToolCallIDs = %q and %q, want provider ids preserved", toolMessages[0].ProviderToolCallID, toolMessages[1].ProviderToolCallID)
	}
	if toolMessages[0].ProviderToolCallID == string(spy.observations[0].ActionID) {
		t.Fatal("tool message must not use ActionID as ProviderToolCallID")
	}
}

func TestRun_FailedToolResultTerminalizesActionAndAttempt(t *testing.T) {
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	ctx, err := domainexecution.WithIdentity(context.Background(), taskID, runID, modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	store, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	actions := actionmanager.New(store)
	spy := &actionAttemptSpyRunner{results: map[string]*tool.ToolResponse{
		"web_search": tool.NewError(tool.ErrInternalError, "tool failed", nil),
	}}
	provider := &mockToolCallingProvider{responses: []llm.ChatResponse{
		{Message: llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{
			ID: "provider-call-failed", Function: llm.ToolCallFunction{Name: "web_search", Arguments: map[string]any{"query": "failure"}},
		}}}, FinishReason: "tool_calls"},
		{Message: llm.ChatMessage{Role: "assistant", Content: "recovered"}, FinishReason: "stop"},
	}}

	result, err := Run(ctx, provider, spy, nil, []llm.ChatMessage{{Role: "user", Content: "search"}}, Config{
		TaskID:  taskID,
		RunID:   runID,
		Actions: actions,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result != "recovered" || provider.callIndex != 2 {
		t.Fatalf("result/provider calls = %q/%d, want recovered/2", result, provider.callIndex)
	}
	if len(spy.observations) != 1 {
		t.Fatalf("observations = %d, want 1", len(spy.observations))
	}
	action, err := actions.GetAction(ctx, spy.observations[0].ActionID)
	if err != nil {
		t.Fatalf("GetAction() error = %v", err)
	}
	if action.Status != domainaction.StatusFailed || action.Summary != "tool failed" {
		t.Fatalf("action status/summary = %s/%q, want failed/tool failed", action.Status, action.Summary)
	}
	attempts, err := actions.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: spy.observations[0].ActionID})
	if err != nil {
		t.Fatalf("ListAttempts() error = %v", err)
	}
	if len(attempts) != 1 || attempts[0].Status != domainaction.AttemptStatusFailed || attempts[0].Summary != "tool failed" {
		t.Fatalf("attempts = %#v, want one failed/tool failed attempt", attempts)
	}
}

func TestRun_ToolAttemptCompletionFailureStopsBeforeNextModel(t *testing.T) {
	originalErr := errors.New("tool execution failed")
	tests := []struct {
		name       string
		response   *tool.ToolResponse
		toolErr    error
		wantOrigin bool
	}{
		{name: "success response", response: tool.NewSuccess("result")},
		{name: "tool error", response: tool.NewSuccess("ignored"), toolErr: originalErr, wantOrigin: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
			ctx, err := domainexecution.WithIdentity(context.Background(), taskID, runID, modulecore.NewTraceID())
			if err != nil {
				t.Fatal(err)
			}
			baseStore, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
			if err != nil {
				t.Fatal(err)
			}
			saveErr := errors.New("completion save failed")
			store := &failOnAttemptSaveStore{Store: baseStore, failAfter: 1, err: saveErr}
			actions := actionmanager.New(store)
			spy := &actionAttemptSpyRunner{
				results:    map[string]*tool.ToolResponse{"web_search": tt.response},
				toolErrors: map[string]error{"web_search": tt.toolErr},
			}
			provider := &mockToolCallingProvider{responses: []llm.ChatResponse{
				{Message: llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{
					ID: "provider-call-save-failure", Function: llm.ToolCallFunction{Name: "web_search", Arguments: map[string]any{"query": "save failure"}},
				}}}, FinishReason: "tool_calls"},
				{Message: llm.ChatMessage{Role: "assistant", Content: "must not reach"}, FinishReason: "stop"},
			}}

			_, runErr := Run(ctx, provider, spy, nil, []llm.ChatMessage{{Role: "user", Content: "search"}}, Config{
				TaskID:  taskID,
				RunID:   runID,
				Actions: actions,
			})
			if runErr == nil {
				t.Fatal("Run() unexpectedly continued after completion save failure")
			}
			if !errors.Is(runErr, saveErr) {
				t.Fatalf("Run() error = %v, want completion save error", runErr)
			}
			if tt.wantOrigin && !errors.Is(runErr, originalErr) {
				t.Fatalf("Run() error = %v, want original tool error", runErr)
			}
			if !strings.Contains(runErr.Error(), "complete tool attempt") {
				t.Fatalf("Run() error = %v, want completion context", runErr)
			}
			if provider.callIndex != 1 {
				t.Fatalf("provider calls = %d, want 1", provider.callIndex)
			}
		})
	}
}

type actionAttemptSpyRunner struct {
	results      map[string]*tool.ToolResponse
	toolErrors   map[string]error
	observations []domainexecution.BoundActionAttempt
}

func (s *actionAttemptSpyRunner) ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
	if actionID, attemptID, ok := domainexecution.BoundActionAttemptFromContext(ctx); ok {
		s.observations = append(s.observations, domainexecution.BoundActionAttempt{
			ActionID:  actionID,
			AttemptID: attemptID,
		})
	}
	r, resultOK := s.results[toolName]
	if toolErr, errOK := s.toolErrors[toolName]; errOK {
		return r, toolErr
	}
	if resultOK {
		return r, nil
	}
	return nil, fmt.Errorf("unknown tool: %s", toolName)
}

func (s *actionAttemptSpyRunner) ListTools(context.Context) ([]tool.ToolMetadata, error) {
	return nil, nil
}

type failOnAttemptSaveStore struct {
	actionmanager.Store
	failAfter int
	saves     int
	err       error
}

func (s *failOnAttemptSaveStore) Transaction(ctx context.Context, callback func(actionmanager.Store) error) error {
	return s.Store.Transaction(ctx, func(tx actionmanager.Store) error {
		return callback(&failOnAttemptSaveTx{Store: tx, owner: s})
	})
}

func (s *failOnAttemptSaveStore) ReadTransaction(ctx context.Context, callback func(actionmanager.Store) error) error {
	return s.Store.ReadTransaction(ctx, callback)
}

type failOnAttemptSaveTx struct {
	actionmanager.Store
	owner *failOnAttemptSaveStore
}

func (s *failOnAttemptSaveTx) SaveAttempt(ctx context.Context, value domainaction.Attempt) error {
	s.owner.saves++
	if s.owner.saves > s.owner.failAfter {
		return s.owner.err
	}
	return s.Store.SaveAttempt(ctx, value)
}

func (s *failOnAttemptSaveTx) Transaction(_ context.Context, callback func(actionmanager.Store) error) error {
	return callback(s)
}

func (s *failOnAttemptSaveTx) ReadTransaction(_ context.Context, callback func(actionmanager.Store) error) error {
	return callback(s)
}

func (s *failOnAttemptSaveStore) SaveAttempt(ctx context.Context, value domainaction.Attempt) error {
	s.saves++
	if s.saves > s.failAfter {
		return s.err
	}
	return s.Store.SaveAttempt(ctx, value)
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
	if cfg.maxTokens() != 4096 {
		t.Errorf("default maxTokens should be 4096, got %d", cfg.maxTokens())
	}

	cfg2 := Config{MaxIterations: 5}
	if cfg2.maxIterations() != 5 {
		t.Errorf("maxIterations should be 5, got %d", cfg2.maxIterations())
	}
	if cfg2.maxTokens() != 4096 {
		t.Errorf("maxTokens should remain 4096 when unspecified, got %d", cfg2.maxTokens())
	}

	cfg3 := Config{MaxTokens: 1234}
	if cfg3.maxTokens() != 1234 {
		t.Errorf("maxTokens should be 1234, got %d", cfg3.maxTokens())
	}
}

func TestRun_RejectsMissingOrMismatchedOwnerBeforeModel(t *testing.T) {
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	store, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	actions := actionmanager.New(store)
	missingTrace, err := domainexecution.WithIdentity(context.Background(), taskID, runID, "")
	if err != nil {
		t.Fatal(err)
	}
	wrongOwner, err := domainexecution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	for name, ctx := range map[string]context.Context{"missing": context.Background(), "trace": missingTrace, "mismatch": wrongOwner} {
		t.Run(name, func(t *testing.T) {
			provider := &mockToolCallingProvider{responses: []llm.ChatResponse{{Message: llm.ChatMessage{Role: "assistant", Content: "done"}}}}
			_, err := Run(ctx, provider, &mockRunnerV2{}, nil, nil, Config{TaskID: taskID, RunID: runID, Actions: actions})
			if err == nil {
				t.Fatal("unowned task loop accepted")
			}
			if len(provider.requests) != 0 {
				t.Fatal("model called before owner admission")
			}
		})
	}
}
