package tools

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type contextBudgetRunnerStub struct {
	resp *tool.ToolResponse
}

func (s *contextBudgetRunnerStub) ExecuteV2(context.Context, string, map[string]any) (*tool.ToolResponse, error) {
	return s.resp, nil
}

func (s *contextBudgetRunnerStub) ListTools(context.Context) ([]tool.ToolMetadata, error) {
	return []tool.ToolMetadata{{ToolID: "file_read"}}, nil
}

type contextBudgetRecorderStub struct {
	usages []domainai.ContextUsage
	events []domainai.WorkflowEvent
	err    error
}

func (s *contextBudgetRecorderStub) SaveContextUsage(_ context.Context, item domainai.ContextUsage) error {
	if s.err != nil {
		return s.err
	}
	s.usages = append(s.usages, item)
	return nil
}

func (s *contextBudgetRecorderStub) SaveWorkflowEvent(_ context.Context, item domainai.WorkflowEvent) error {
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

	resp, err := runner.ExecuteV2(context.Background(), "file_read", nil)
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
	if recorder.events[0].ParentEventID != recorder.usages[0].EventID {
		t.Fatalf("event should link to context usage: event=%#v usage=%#v", recorder.events[0], recorder.usages[0])
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

	_, err := runner.ExecuteV2(context.Background(), "file_read", nil)
	if err == nil {
		t.Fatal("expected recorder failure")
	}
	if !strings.Contains(err.Error(), "tool context usage save failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}
