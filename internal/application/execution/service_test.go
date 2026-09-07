package execution

import (
	"context"
	"errors"
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
	records     map[string]domain.Record
	updateErr   error
	updateCalls int
}

func newMemRepo() *memRepo {
	return &memRepo{records: make(map[string]domain.Record)}
}

func (m *memRepo) Create(_ context.Context, record domain.Record) error {
	m.records[recordKey(record.TaskID, record.ActionID)] = record
	return nil
}

func (m *memRepo) UpdateStatus(_ context.Context, taskID modulecore.TaskID, actionID modulecore.ActionID, status domain.Status, errMsg string) (domain.Record, error) {
	m.updateCalls++
	if m.updateErr != nil {
		return domain.Record{
			TaskID:   taskID,
			ActionID: actionID,
			Status:   status,
			Error:    errMsg,
		}, m.updateErr
	}
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

func (m *memRepo) Get(_ context.Context, taskID modulecore.TaskID, actionID modulecore.ActionID) (domain.Record, error) {
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

	res, err := svc.RequestToolExecution(context.Background(), domain.Action{TaskID: modulecore.NewTaskID(), ActionID: modulecore.NewActionID(), Tool: "shell"})
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

	res, err := svc.RequestToolExecution(context.Background(), domain.Action{
		TaskID:      modulecore.NewTaskID(),
		ActionID:    modulecore.NewActionID(),
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

	if _, err := svc.RequestToolExecution(context.Background(), domain.Action{ActionID: modulecore.NewActionID(), Tool: "shell"}); err == nil {
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

	if _, err := svc.RequestToolExecution(context.Background(), domain.Action{TaskID: modulecore.NewTaskID(), TraceID: "legacy", ActionID: modulecore.NewActionID(), Tool: "shell"}); err == nil {
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

	result, err := svc.RequestToolExecution(context.Background(), domain.Action{TaskID: taskID, ActionID: modulecore.NewActionID(), Tool: "shell"})
	if err != nil {
		t.Fatalf("RequestToolExecution failed: %v", err)
	}
	if result.Record.TraceID != "" {
		t.Fatalf("trace identity must remain empty when the owner did not provide one: %q", result.Record.TraceID)
	}

	traceID := modulecore.NewTraceID()
	result, err = svc.RequestToolExecution(context.Background(), domain.Action{TaskID: taskID, TraceID: traceID, ActionID: modulecore.NewActionID(), Tool: "shell"})
	if err != nil {
		t.Fatalf("RequestToolExecution with trace failed: %v", err)
	}
	if result.Record.TraceID != traceID {
		t.Fatalf("TraceID = %q, want owner-provided %q", result.Record.TraceID, traceID)
	}
}

func TestServiceExecutorErrorRetainsResponseAndError(t *testing.T) {
	repo := newMemRepo()
	executorErr := errors.New("executor failed")
	response := tool.NewSuccess("partial result")
	exec := &stubExecutor{resp: response, err: executorErr}
	svc := NewService(&stubPolicy{decision: domain.PolicyDecision{Decision: domain.DecisionAllow}}, exec, repo)
	action := domain.Action{TaskID: modulecore.NewTaskID(), ActionID: modulecore.NewActionID(), Tool: "shell"}

	result, err := svc.RequestToolExecution(context.Background(), action)
	if !errors.Is(err, executorErr) {
		t.Fatalf("RequestToolExecution error = %v, want executor error", err)
	}
	if result == nil || result.Response != response {
		t.Fatalf("result = %#v, want response %p", result, response)
	}
	if result.Record.Status != domain.StatusFailed {
		t.Fatalf("result record status = %s, want failed", result.Record.Status)
	}
	if repo.updateCalls != 1 {
		t.Fatalf("audit update calls = %d, want 1", repo.updateCalls)
	}
}

func TestServiceExecutorErrorJoinsAuditFailureAndKeepsRunningRecord(t *testing.T) {
	auditErr := errors.New("audit update failed")
	repo := newMemRepo()
	repo.updateErr = auditErr
	executorErr := errors.New("executor failed")
	response := tool.NewSuccess("partial result")
	exec := &stubExecutor{resp: response, err: executorErr}
	svc := NewService(&stubPolicy{decision: domain.PolicyDecision{Decision: domain.DecisionAllow}}, exec, repo)
	action := domain.Action{TaskID: modulecore.NewTaskID(), ActionID: modulecore.NewActionID(), Tool: "shell"}

	result, err := svc.RequestToolExecution(context.Background(), action)
	if !errors.Is(err, executorErr) || !errors.Is(err, auditErr) {
		t.Fatalf("RequestToolExecution error = %v, want executor and audit errors", err)
	}
	if result == nil || result.Response != response {
		t.Fatalf("result = %#v, want response %p", result, response)
	}
	if result.Record.Status != domain.StatusRunning {
		t.Fatalf("result record status = %s, want original running record", result.Record.Status)
	}
	stored, ok := repo.records[recordKey(action.TaskID, action.ActionID)]
	if !ok || stored.Status != domain.StatusRunning {
		t.Fatalf("stored record = %#v, want unchanged running record", stored)
	}
}

func TestServiceStructuredErrorJoinsAuditFailureAndKeepsResponse(t *testing.T) {
	auditErr := errors.New("audit update failed")
	repo := newMemRepo()
	repo.updateErr = auditErr
	response := tool.NewError(tool.ErrTimeout, "timed out", nil)
	exec := &stubExecutor{resp: response}
	svc := NewService(&stubPolicy{decision: domain.PolicyDecision{Decision: domain.DecisionAllow}}, exec, repo)
	action := domain.Action{TaskID: modulecore.NewTaskID(), ActionID: modulecore.NewActionID(), Tool: "shell"}

	result, err := svc.RequestToolExecution(context.Background(), action)
	var toolErr *tool.ToolError
	if !errors.As(err, &toolErr) || toolErr != response.Error || !errors.Is(err, auditErr) {
		t.Fatalf("RequestToolExecution error = %v, want structured tool and audit errors", err)
	}
	if result == nil || result.Response != response {
		t.Fatalf("result = %#v, want response %p", result, response)
	}
	if result.Record.Status != domain.StatusRunning {
		t.Fatalf("result record status = %s, want original running record", result.Record.Status)
	}
}

func TestServiceSuccessfulResponseAuditFailureRetainsResponse(t *testing.T) {
	auditErr := errors.New("audit update failed")
	repo := newMemRepo()
	repo.updateErr = auditErr
	response := tool.NewSuccess("ok")
	exec := &stubExecutor{resp: response}
	svc := NewService(&stubPolicy{decision: domain.PolicyDecision{Decision: domain.DecisionAllow}}, exec, repo)
	action := domain.Action{TaskID: modulecore.NewTaskID(), ActionID: modulecore.NewActionID(), Tool: "shell"}

	result, err := svc.RequestToolExecution(context.Background(), action)
	if !errors.Is(err, auditErr) {
		t.Fatalf("RequestToolExecution error = %v, want audit error", err)
	}
	if result == nil || result.Response != response {
		t.Fatalf("result = %#v, want response %p", result, response)
	}
	if result.Record.Status != domain.StatusRunning {
		t.Fatalf("result record status = %s, want original running record", result.Record.Status)
	}
}

func TestServiceNilResponseIsAuditedAsFailure(t *testing.T) {
	repo := newMemRepo()
	exec := &stubExecutor{}
	svc := NewService(&stubPolicy{decision: domain.PolicyDecision{Decision: domain.DecisionAllow}}, exec, repo)
	action := domain.Action{TaskID: modulecore.NewTaskID(), ActionID: modulecore.NewActionID(), Tool: "shell"}

	result, err := svc.RequestToolExecution(context.Background(), action)
	if err != nil {
		t.Fatalf("RequestToolExecution failed: %v", err)
	}
	if result == nil || result.Response == nil || result.Response.Error == nil {
		t.Fatalf("nil response should produce a structured error result: %#v", result)
	}
	if result.Response.Error.Code != tool.ErrInternalError {
		t.Fatalf("synthesized error code = %s, want %s", result.Response.Error.Code, tool.ErrInternalError)
	}
	if result.Record.Status != domain.StatusFailed {
		t.Fatalf("result record status = %s, want failed", result.Record.Status)
	}
	if result.Record.Error != "empty tool response" {
		t.Fatalf("audit error = %q, want empty tool response", result.Record.Error)
	}
	if repo.updateCalls != 1 {
		t.Fatalf("audit update calls = %d, want 1", repo.updateCalls)
	}
}

func recordKey(taskID modulecore.TaskID, actionID modulecore.ActionID) string {
	return taskID.String() + "::" + string(actionID)
}
