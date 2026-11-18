package taskcenter

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/taskexecution"
	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	persisttask "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	persistworkstream "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type taskCenterLookupStub struct {
	calls int
}

func (stub *taskCenterLookupStub) Lookup(_ context.Context, kind, name, information string, limit int) (any, error) {
	stub.calls++
	return map[string]any{"kind": kind, "name": name, "information": information, "limit": limit, "matches": []string{}}, nil
}

func TestTaskCenterExecutionReadAndCreatorAcceptanceRecoverExactResult(t *testing.T) {
	ctx := context.Background()
	workstreamStore, err := persistworkstream.NewSQLiteStore(filepath.Join(t.TempDir(), "task-center-goal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workstreamStore.Close() })
	taskStore, err := persisttask.NewJSONLStore(filepath.Join(t.TempDir(), "task-center-task-store"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = taskStore.Close() })
	manager := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	policy := taskexecution.Policy{SchemaVersion: taskexecution.SchemaVersionV1, TaskKinds: map[string]taskexecution.KindPolicy{
		"movie.catalog.lookup": {AllowedAgents: taskexecution.AllowedAgents{Mode: taskexecution.AllowedAgentsExplicit, ActorIDs: []string{"mio"}}},
	}}
	policyRevision, err := policy.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	lookup := &taskCenterLookupStub{}
	service, err := NewService(workstreamStore, policy, []string{"mio"}, []Kind{{
		ID: "movie.catalog.lookup", Label: "Movie catalog lookup", IntrinsicAllowedAgents: []string{"mio"},
		RequiresTypedInput: true, ExecutorAvailable: true,
	}}, func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	service.WithNativeExecution(manager, workstreamStore, lookup)
	userScope := taskCenterServiceTestScope(t, "user-create")
	created, err := service.Submit(tool.WithToolExecutionScope(ctx, userScope), SubmitInput{
		DataScope: "public", KindID: "movie.catalog.lookup", KindInput: json.RawMessage(`{"kind":"movie","name":"Arrival","information":"overview","limit":1}`),
		Title: "Arrival catalog lookup", Description: "Typed catalog read", SuccessCriteria: []string{"Review the result."},
		Verification: []string{"Inspect the returned catalog matches."}, Assignee: "unassigned", Timing: modulecore.TaskTiming{},
		IdempotencyKey: "create-movie-goal", ExpectedPolicyRevision: policyRevision,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	agentScope := taskCenterServiceAgentScope(t, "agent-claim", "mio")
	claimed, err := service.Claim(tool.WithToolExecutionScope(ctx, agentScope), ClaimInput{
		GoalID: created.Item.GoalID, ExpectedRevision: created.Item.Revision,
		ExpectedPolicyRevision: policyRevision, IdempotencyKey: "claim-movie-goal",
	})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	callerTask, err := manager.Create(ctx, domaintask.Task{
		Title: "TaskCenter native invocation parent", Route: domaintask.RouteGeneral, Assignee: "mio",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create real native caller Task: %v", err)
	}
	callerRun, err := manager.StartRunWithReason(ctx, callerTask.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatalf("start real native caller Run: %v", err)
	}
	input := ExecutionInput{
		GoalID: claimed.Item.GoalID, ExpectedRevision: claimed.Item.Revision, ExpectedAssignmentEpoch: claimed.Item.AssignmentEpoch,
		ExpectedPolicyRevision: policyRevision, IdempotencyKey: "execute-movie-goal",
		CallerTaskID: callerTask.TaskID, CallerRunID: callerRun.RunID,
	}
	executionCtx, err := domainexecution.WithIdentity(tool.WithToolExecutionScope(ctx, agentScope), callerTask.TaskID, callerRun.RunID, callerRun.TraceID)
	if err != nil {
		t.Fatalf("bind real native caller identity: %v", err)
	}
	forgedCaller := input
	forgedCaller.CallerTaskID = modulecore.NewTaskID()
	if _, err := service.ExecuteTaskCenterExecution(executionCtx, forgedCaller); !errors.Is(err, ErrForbidden) {
		t.Fatalf("caller Task not matching owner-bound execution identity error=%v, want forbidden", err)
	}
	result, err := service.ExecuteTaskCenterExecution(executionCtx, input)
	if err != nil {
		t.Fatalf("Execute first call: %v", err)
	}
	if result.Verification != "verification_pending" || result.TaskID == "" || result.RunID == "" || lookup.calls != 1 {
		t.Fatalf("execution result=%+v lookup calls=%d", result, lookup.calls)
	}
	// Losing the effect owner must not make a previously completed result
	// unreadable or prevent replay of its exact execution receipt.
	service.nativeExecution.Movies = nil
	pendingReplay, err := service.ExecuteTaskCenterExecution(executionCtx, input)
	if err != nil || pendingReplay.Verification != "verification_pending" || pendingReplay.ResultReference != result.ResultReference || lookup.calls != 1 {
		t.Fatalf("pending execution receipt replay=%+v lookup calls=%d error=%v", pendingReplay, lookup.calls, err)
	}
	goal, err := workstreamStore.GetTaskPlan(tool.WithToolExecutionScope(ctx, userScope), userScope, claimed.Item.GoalID)
	if err != nil || goal.TaskPlan.ExecutionResult == nil || goal.TaskPlan.ExecutionResult.ArtifactID != result.ResultReference.ArtifactID {
		t.Fatalf("durable Goal result projection=%+v error=%v", goal.TaskPlan, err)
	}
	item, err := service.Get(tool.WithToolExecutionScope(ctx, userScope), claimed.Item.GoalID)
	if err != nil || !item.CanAcceptResult || item.ExecutionVerification != "verification_pending" || item.ResultSHA256 != result.ResultReference.ResultSHA256 {
		t.Fatalf("creator result item=%+v error=%v", item, err)
	}
	body, err := service.ReadResult(tool.WithToolExecutionScope(ctx, userScope), claimed.Item.GoalID)
	if err != nil || body.ResultSHA256 != result.ResultReference.ResultSHA256 || !json.Valid(body.Result) || body.ExecutionVerification != "verification_pending" {
		t.Fatalf("read result=%+v error=%v", body, err)
	}
	// The scope authorized to review this Goal can still read its completed
	// result while the executor is unavailable.
	if _, err := service.ReadResult(tool.WithToolExecutionScope(ctx, userScope), claimed.Item.GoalID); err != nil {
		t.Fatalf("read result after executor loss: %v", err)
	}
	accepted, err := service.AcceptResult(tool.WithToolExecutionScope(ctx, userScope), AcceptResultInput{
		GoalID: claimed.Item.GoalID, ExpectedRevision: item.Revision, ExpectedAssignmentEpoch: item.AssignmentEpoch, IdempotencyKey: "accept-movie-result",
	})
	if err != nil || accepted.Item.Status != "completed" || accepted.Item.ExecutionVerification != "accepted" || accepted.Item.CanAcceptResult {
		t.Fatalf("creator acceptance=%+v error=%v", accepted, err)
	}
	replayed, err := service.ExecuteTaskCenterExecution(executionCtx, input)
	if err != nil || replayed.Verification != "accepted" || replayed.ResultReference != result.ResultReference || lookup.calls != 1 {
		t.Fatalf("execution receipt replay=%+v lookup calls=%d error=%v", replayed, lookup.calls, err)
	}
	changedRequest := input
	changedRequest.ExpectedRevision++
	if _, err := service.ExecuteTaskCenterExecution(executionCtx, changedRequest); err == nil {
		t.Fatal("execution receipt replay accepted a changed original Goal revision")
	}
	receipt, found, err := service.Receipt(tool.WithToolExecutionScope(ctx, userScope), "execution_result_accept", "accept-movie-result")
	if err != nil || !found || receipt.Item.GoalID != claimed.Item.GoalID || receipt.Item.ExecutionVerification != "accepted" {
		t.Fatalf("acceptance receipt=%+v found=%v error=%v", receipt, found, err)
	}
	if err := manager.CloseStartedRun(ctx, callerTask.TaskID, callerRun.RunID, "mio", taskmanager.RunOutcome{Status: domaintask.StatusSucceeded, Summary: "TaskCenter fixture invocation completed"}, nil); err != nil {
		t.Fatalf("close native caller Run: %v", err)
	}
}
