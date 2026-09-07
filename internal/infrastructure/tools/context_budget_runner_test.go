package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type contextBudgetRunnerStub struct {
	resp  *tool.ToolResponse
	calls int
}

func (s *contextBudgetRunnerStub) ExecuteV2(context.Context, string, map[string]any) (*tool.ToolResponse, error) {
	s.calls++
	return s.resp, nil
}

func (s *contextBudgetRunnerStub) ListTools(context.Context) ([]tool.ToolMetadata, error) {
	return []tool.ToolMetadata{{ToolID: "file_read"}}, nil
}

type contextBudgetRecorderStub struct {
	usages    []domainai.ContextUsage
	events    []modulecore.EventEnvelope
	saveCtx   context.Context
	appendCtx context.Context
	err       error
}

func (s *contextBudgetRecorderStub) SaveContextUsage(ctx context.Context, item domainai.ContextUsage) error {
	s.saveCtx = ctx
	if s.err != nil {
		return s.err
	}
	s.usages = append(s.usages, item)
	return nil
}

func (s *contextBudgetRecorderStub) Append(ctx context.Context, item modulecore.EventEnvelope) error {
	s.appendCtx = ctx
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, item)
	return nil
}

func TestContextBudgetRunnerStopsLargeToolResult(t *testing.T) {
	inner := &contextBudgetRunnerStub{resp: tool.NewSuccess(strings.Repeat("a", 400))}
	runner := NewContextBudgetRunner(inner, ContextBudgetRunnerConfig{
		Agent: "Worker",
		Policy: domainai.ContextBudgetPolicy{
			MaxContextTokens: 50,
			WarnAtRatio:      0.8,
			StopAtRatio:      0.95,
		},
	})

	resp, err := runner.ExecuteV2(context.Background(), "file_read", nil)
	if err != nil {
		t.Fatalf("ExecuteV2 returned err: %v", err)
	}
	if resp == nil || !resp.IsError() {
		t.Fatalf("expected context budget error response, got %#v", resp)
	}
	if resp.Error.Message != "tool result exceeds context budget" {
		t.Fatalf("unexpected error message: %q", resp.Error.Message)
	}
	if resp.Error.Details["context_budget_status"] != domainai.ContextBudgetStatusStop {
		t.Fatalf("expected stop metadata, got %#v", resp.Error.Details)
	}
}

func TestContextBudgetRunnerOffloadsStoppedToolResult(t *testing.T) {
	inner := &contextBudgetRunnerStub{resp: tool.NewSuccess(strings.Repeat("a", 400))}
	runner := NewContextBudgetRunner(inner, ContextBudgetRunnerConfig{
		Agent:      "Worker",
		OffloadDir: t.TempDir(),
		Policy: domainai.ContextBudgetPolicy{
			MaxContextTokens: 50,
			WarnAtRatio:      0.8,
			StopAtRatio:      0.95,
		},
	})

	resp, err := runner.ExecuteV2(context.Background(), "file/read", nil)
	if err != nil {
		t.Fatalf("ExecuteV2 returned err: %v", err)
	}
	if resp == nil || !resp.IsError() {
		t.Fatalf("expected context budget error response, got %#v", resp)
	}
	if resp.Error.Details["context_budget_offloaded"] != true {
		t.Fatalf("expected offload metadata, got %#v", resp.Error.Details)
	}
	path, ok := resp.Error.Details["context_budget_offload_path"].(string)
	if !ok || path == "" {
		t.Fatalf("expected offload path metadata, got %#v", resp.Error.Details)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected offloaded file: %v", err)
	}
	if !strings.Contains(string(data), strings.Repeat("a", 20)) {
		t.Fatalf("offloaded file does not contain raw result: %s", string(data))
	}
}

func TestContextBudgetRunnerWarnsAndPreservesToolResult(t *testing.T) {
	inner := &contextBudgetRunnerStub{resp: tool.NewSuccess(strings.Repeat("a", 340))}
	recorder := &contextBudgetRecorderStub{}
	runner := NewContextBudgetRunner(inner, ContextBudgetRunnerConfig{
		Agent:    "Worker",
		Recorder: recorder,
		Policy: domainai.ContextBudgetPolicy{
			MaxContextTokens: 100,
			WarnAtRatio:      0.8,
			StopAtRatio:      0.95,
		},
	})

	resp, err := runner.ExecuteV2(contextBudgetTestContext(t), "file_read", nil)
	if err != nil {
		t.Fatalf("ExecuteV2 returned err: %v", err)
	}
	if resp == nil || resp.IsError() {
		t.Fatalf("expected success response, got %#v", resp)
	}
	if resp.Metadata["context_budget_status"] != domainai.ContextBudgetStatusWarn {
		t.Fatalf("expected warn metadata, got %#v", resp.Metadata)
	}
	if resp.Result == "" {
		t.Fatal("tool result should be preserved on warning")
	}
	if len(recorder.usages) != 1 {
		t.Fatalf("expected context usage to be recorded, got %#v", recorder.usages)
	}
	if len(recorder.events) != 1 || recorder.events[0].EventType != "context_budget_warning" {
		t.Fatalf("expected warning workflow event, got %#v", recorder.events)
	}
	if recorder.events[0].CausationEventID != "" {
		t.Fatalf("context usage record must not be misused as event causation: event=%#v", recorder.events[0])
	}
	if recorder.events[0].Payload["context_usage_record_id"] != recorder.usages[0].EventID {
		t.Fatalf("event payload should identify the context usage record: event=%#v usage=%#v", recorder.events[0], recorder.usages[0])
	}
	if recorder.usages[0].CreatedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("unexpected usage timestamp: %s", recorder.usages[0].CreatedAt)
	}
}

func TestContextBudgetRunnerRecorderFailureStopsExecution(t *testing.T) {
	inner := &contextBudgetRunnerStub{resp: tool.NewSuccess("small result")}
	runner := NewContextBudgetRunner(inner, ContextBudgetRunnerConfig{
		Agent:    "Worker",
		Recorder: &contextBudgetRecorderStub{err: errors.New("ai workflow store unavailable")},
		Policy: domainai.ContextBudgetPolicy{
			MaxContextTokens: 100,
			WarnAtRatio:      0.8,
			StopAtRatio:      0.95,
		},
	})

	_, err := runner.ExecuteV2(contextBudgetTestContext(t), "file_read", nil)
	if err == nil {
		t.Fatal("expected recorder failure")
	}
	if !strings.Contains(err.Error(), "tool context usage save failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func contextBudgetTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, err := execution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	scope, err := tool.NewToolExecutionScope("budget-test", tool.ActorKindAgent, "shiro", "", []string{tool.DataScopePublic}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	return tool.WithToolExecutionScope(ctx, scope)
}

func TestContextBudgetRunnerPreservesExactOwnerLineage(t *testing.T) {
	for _, size := range []int{340, 520} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ctx := contextBudgetTestContext(t)
			actionID, attemptID := modulecore.NewActionID(), modulecore.NewAttemptID()
			ctx, err := execution.WithBoundActionAttempt(ctx, actionID, attemptID)
			if err != nil {
				t.Fatal(err)
			}
			identity, _ := execution.IdentityFromContext(ctx)
			rec := &contextBudgetRecorderStub{}
			runner := NewContextBudgetRunner(&contextBudgetRunnerStub{resp: tool.NewSuccess(strings.Repeat("a", size))}, ContextBudgetRunnerConfig{Agent: "Worker", Recorder: rec, Policy: domainai.ContextBudgetPolicy{MaxContextTokens: 100}})
			if _, err := runner.ExecuteV2(ctx, "file_read", nil); err != nil {
				t.Fatal(err)
			}
			if len(rec.usages) != 1 || len(rec.events) != 1 {
				t.Fatalf("missing records: %#v", rec)
			}
			if rec.saveCtx != ctx || rec.appendCtx != ctx {
				t.Fatal("persistence lost original request context")
			}
			usage, event := rec.usages[0], rec.events[0]
			if usage.TaskID != identity.TaskID || usage.RunID != identity.RunID || event.TaskID != identity.TaskID || event.RunID != identity.RunID || event.TraceID != identity.TraceID || event.ActorKind != "agent" || event.ActorID != "shiro" || event.ActionID != actionID || event.AttemptID != attemptID || event.CausationEventID != "" {
				t.Fatalf("lost owner lineage: %#v %#v", usage, event)
			}
			if err := modulecore.ValidateEventEnvelope(event); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestContextBudgetRunnerRejectsMissingLineageBeforeTool(t *testing.T) {
	missingScope, err := execution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	missingTrace, err := execution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), "")
	if err != nil {
		t.Fatal(err)
	}
	scope, _ := tool.ToolExecutionScopeFromContext(contextBudgetTestContext(t))
	missingTrace = tool.WithToolExecutionScope(missingTrace, scope)
	canceled, cancel := context.WithCancel(contextBudgetTestContext(t))
	cancel()
	for name, ctx := range map[string]context.Context{"nil": nil, "identity": context.Background(), "scope": missingScope, "trace": missingTrace, "canceled": canceled} {
		t.Run(name, func(t *testing.T) {
			inner := &contextBudgetRunnerStub{resp: tool.NewSuccess("result")}
			rec := &contextBudgetRecorderStub{}
			runner := NewContextBudgetRunner(inner, ContextBudgetRunnerConfig{Recorder: rec})
			if _, err := runner.ExecuteV2(ctx, "file_read", nil); err == nil {
				t.Fatal("missing lineage accepted")
			}
			if inner.calls != 0 || len(rec.usages) != 0 || len(rec.events) != 0 {
				t.Fatalf("rejected request produced effects: %d %#v", inner.calls, rec)
			}
		})
	}
}
