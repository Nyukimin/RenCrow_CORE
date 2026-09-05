package execution

import (
	"context"
	"testing"
	"time"

	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type stubPolicy struct {
	decision domain.PolicyDecision
}

func (s *stubPolicy) Evaluate(action domain.Action) domain.PolicyDecision {
	return s.decision
}

type stubExecutor struct {
	called bool
	resp   *tool.ToolResponse
	err    error
}

func (s *stubExecutor) ExecuteV2(_ context.Context, _ string, _ map[string]any) (*tool.ToolResponse, error) {
	s.called = true
	return s.resp, s.err
}

type memRepo struct {
	records map[string]domain.Record
}

func newMemRepo() *memRepo {
	return &memRepo{records: make(map[string]domain.Record)}
}

func (m *memRepo) Create(_ context.Context, record domain.Record) error {
	m.records[recordKey(record.TaskID, record.ActionID)] = record
	return nil
}

func (m *memRepo) UpdateStatus(_ context.Context, taskID modulecore.TaskID, actionID string, status domain.Status, errMsg string) (domain.Record, error) {
	k := recordKey(taskID, actionID)
	rec := m.records[k]
	rec.Status = status
	rec.Error = errMsg
	if status.IsTerminal() {
		now := time.Now().UTC()
		rec.FinishedAt = &now
	}
	m.records[k] = rec
	return rec, nil
}

func (m *memRepo) Get(_ context.Context, taskID modulecore.TaskID, actionID string) (domain.Record, error) {
	return m.records[recordKey(taskID, actionID)], nil
}

func (m *memRepo) CountByStatus(_ context.Context) (map[domain.Status]int, error) {
	counts := map[domain.Status]int{}
	for _, r := range m.records {
		counts[r.Status]++
	}
	return counts, nil
}

func TestService_RequestToolExecution_Deny(t *testing.T) {
	repo := newMemRepo()
	exec := &stubExecutor{}
	svc := NewService(&stubPolicy{decision: domain.PolicyDecision{Decision: domain.DecisionDeny}}, exec, repo)

	res, err := svc.RequestToolExecution(context.Background(), domain.Action{TaskID: modulecore.NewTaskID(), ActionID: "a", Tool: "shell"})
	if err != nil {
		t.Fatalf("RequestToolExecution failed: %v", err)
	}
	if res.Record.Status != domain.StatusDenied {
		t.Fatalf("expected denied, got %s", res.Record.Status)
	}
	if res.Record.EventType != "security.violation" {
		t.Fatalf("expected security.violation event, got %s", res.Record.EventType)
	}
	if exec.called {
		t.Fatal("executor must not be called on deny")
	}
}

func TestService_RequestAllowExecutesImmediately(t *testing.T) {
	repo := newMemRepo()
	exec := &stubExecutor{resp: tool.NewSuccess("ok")}
	svc := NewService(&stubPolicy{decision: domain.PolicyDecision{Decision: domain.DecisionAllow}}, exec, repo)
	now := time.Now().UTC()
	svc.now = func() time.Time { return now }

	res, err := svc.RequestToolExecution(context.Background(), domain.Action{
		TaskID:      modulecore.NewTaskID(),
		ActionID:    "a1",
		Tool:        "shell",
		Arguments:   map[string]any{"command": "echo ok"},
		RequestedAt: now,
	})
	if err != nil {
		t.Fatalf("RequestToolExecution failed: %v", err)
	}
	if !exec.called {
		t.Fatal("executor should be called immediately for allow decision")
	}
	if res.Record.Status != domain.StatusSucceeded {
		t.Fatalf("expected succeeded, got %s", res.Record.Status)
	}
	if res.Record.EventType != "security.decision" {
		t.Fatalf("expected security.decision event, got %s", res.Record.EventType)
	}
}

func TestServiceRejectsMissingTaskIDBeforePolicyEvaluation(t *testing.T) {
	repo := newMemRepo()
	exec := &stubExecutor{resp: tool.NewSuccess("ok")}
	svc := NewService(&stubPolicy{decision: domain.PolicyDecision{Decision: domain.DecisionAllow}}, exec, repo)

	if _, err := svc.RequestToolExecution(context.Background(), domain.Action{ActionID: "a", Tool: "shell"}); err == nil {
		t.Fatal("expected canonical task identity validation error")
	}
	if exec.called {
		t.Fatal("executor must not run for an invalid task identity")
	}
}

func TestServiceRejectsInvalidTraceIdentityBeforePolicyEvaluation(t *testing.T) {
	repo := newMemRepo()
	exec := &stubExecutor{resp: tool.NewSuccess("ok")}
	svc := NewService(&stubPolicy{decision: domain.PolicyDecision{Decision: domain.DecisionAllow}}, exec, repo)

	if _, err := svc.RequestToolExecution(context.Background(), domain.Action{TaskID: modulecore.NewTaskID(), TraceID: "legacy", ActionID: "a", Tool: "shell"}); err == nil {
		t.Fatal("expected canonical trace identity validation error")
	}
	if exec.called {
		t.Fatal("executor must not run for an invalid trace identity")
	}
}

func TestServiceCopiesOnlyProvidedTraceID(t *testing.T) {
	repo := newMemRepo()
	exec := &stubExecutor{resp: tool.NewSuccess("ok")}
	svc := NewService(&stubPolicy{decision: domain.PolicyDecision{Decision: domain.DecisionAllow}}, exec, repo)
	taskID := modulecore.NewTaskID()

	result, err := svc.RequestToolExecution(context.Background(), domain.Action{TaskID: taskID, ActionID: "a", Tool: "shell"})
	if err != nil {
		t.Fatalf("RequestToolExecution failed: %v", err)
	}
	if result.Record.TraceID != "" {
		t.Fatalf("trace identity must remain empty when the owner did not provide one: %q", result.Record.TraceID)
	}

	traceID := modulecore.NewTraceID()
	result, err = svc.RequestToolExecution(context.Background(), domain.Action{TaskID: taskID, TraceID: traceID, ActionID: "b", Tool: "shell"})
	if err != nil {
		t.Fatalf("RequestToolExecution with trace failed: %v", err)
	}
	if result.Record.TraceID != traceID {
		t.Fatalf("TraceID = %q, want owner-provided %q", result.Record.TraceID, traceID)
	}
}

func recordKey(taskID modulecore.TaskID, actionID string) string {
	return taskID.String() + "::" + actionID
}
