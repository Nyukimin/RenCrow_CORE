package conversationruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestDelegatedTaskExecutorPersistsTextOnlyReportWithTrustedIdentity(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	manager, closeManager, taskID, source := newExecutionTask(t, t.TempDir())
	defer closeManager()
	store := newDelegatedExecutionStoreFake(source, taskID, now)
	worker := &privateWorkerFake{result: "A bounded text result."}
	owner := &executionTaskOwnerFake{Manager: manager}
	executor := newTestDelegatedTaskExecutor(t, store, owner, worker, nil, now)

	result, err := executor.StartOne(ctx)
	if err != nil {
		t.Fatalf("StartOne(): %v", err)
	}
	if !result.Claimed || !result.Generated || !result.ReportSaved || !result.ReportPending || result.TaskID != taskID || result.RunID == "" || result.ReportID == "" {
		t.Fatalf("StartOne()=%+v, want generated and durable pending report", result)
	}
	if worker.calls != 1 || worker.actor != "shiro" || len(worker.capabilities) != 0 {
		t.Fatalf("private worker calls=%d actor=%q capabilities=%v, want one Shiro text-only call", worker.calls, worker.actor, worker.capabilities)
	}
	if owner.effectCalls != 0 {
		t.Fatalf("Task ExecuteRunEffect calls=%d, want worker to run without a whole-agent Task fence", owner.effectCalls)
	}
	identity, err := execution.IdentityFromContext(worker.ctx)
	if err != nil || identity.TaskID != taskID || identity.RunID != result.RunID || identity.TraceID != source.Handoff.SourceTraceID {
		t.Fatalf("worker execution identity=%+v err=%v, want bound Task/Run/source trace", identity, err)
	}
	scope, ok := tool.ToolExecutionScopeFromContext(worker.ctx)
	if !ok || scope.ActorKind != tool.ActorKindAgent || scope.ActorID != "shiro" || scope.AuthenticationSource != tool.AuthenticationSourceAgentOrchestrator ||
		scope.AgentRole != "worker" || scope.Purpose != "delegated_task_execution" || !reflect.DeepEqual(scope.AllowedDataScopes, []string{tool.DataScopePublic}) {
		t.Fatalf("worker ToolExecutionScope=%+v found=%v, want trusted Shiro public-only scope", scope, ok)
	}
	if worker.input.RootTaskID() != taskID || worker.input.TurnID() != source.Handoff.OriginTurnID ||
		worker.input.TraceID() != source.Handoff.SourceTraceID || worker.input.UserMessageID() != source.Handoff.OriginUserMessageID ||
		worker.input.AgentMessageID() != source.Handoff.InstructionMessageID || worker.input.SessionID() != string(source.Handoff.SessionID) ||
		!strings.Contains(worker.input.MessageText(), source.Handoff.Instruction) || !strings.Contains(worker.input.MessageText(), source.Readback.Text) {
		t.Fatalf("worker canonical TurnInput identity/content mismatch: task=%s turn=%s trace=%s session=%q text=%q", worker.input.RootTaskID(), worker.input.TurnID(), worker.input.TraceID(), worker.input.SessionID(), worker.input.MessageText())
	}
	if store.retryCount != 0 {
		t.Fatalf("retry count=%d, want no retry after durable Task report", store.retryCount)
	}
	reports, err := manager.ListPendingReports(ctx)
	if err != nil || len(reports) != 1 {
		t.Fatalf("ListPendingReports()=%+v err=%v, want exactly one durable pending report", reports, err)
	}
	report := reports[0]
	if report.ReportID != result.ReportID || report.RunID != result.RunID || report.TaskStatus != domaintask.StatusSucceeded ||
		report.Envelope.Status != conversation.ReportStatusSucceeded || report.Envelope.Result != worker.result ||
		report.Envelope.WorkTaskID != taskID || report.Envelope.WorkRunID != result.RunID || report.TrustedSender != "shiro" {
		t.Fatalf("saved Task report=%+v, want full source-bound success report", report)
	}
	task, err := manager.Get(ctx, taskID)
	if err != nil || task.Status != domaintask.StatusSucceeded {
		t.Fatalf("completed Task=%+v err=%v, want succeeded", task, err)
	}
}

func TestDelegatedTaskExecutorAcceptsEmptyConstraintsAfterTaskStoreReload(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 2, 0, 0, time.UTC)
	root := t.TempDir()
	manager, closeManager := openDelegatedTaskManager(t, root)
	admissionStore := newDelegatedTaskAdmissionStore(now)
	source := admissionStore.source
	source.Handoff.Constraints = nil
	source.Readback.AcknowledgedConstraintKeys = []string{}
	admissionStore.source = source
	admission := newDelegatedTaskAdmission(t, admissionStore, manager, now, "shiro")
	admitted, err := admission.StartOne(ctx)
	if err != nil || !admitted.Bound {
		t.Fatalf("admit empty-constraints Task: result=%+v err=%v", admitted, err)
	}
	_, expectedShared, err := delegatedTaskDraft(source, "shiro")
	if err != nil || expectedShared.Constraints == nil || len(expectedShared.Constraints) != 0 {
		t.Fatalf("delegatedTaskDraft() constraints=%v err=%v, want a non-nil empty slice", expectedShared.Constraints, err)
	}
	if err := closeManager(); err != nil {
		t.Fatalf("close Task owner before reload: %v", err)
	}
	manager, closeManager = openDelegatedTaskManager(t, root)
	defer closeManager()
	persisted, err := manager.Context(ctx, admitted.TaskID)
	if err != nil || persisted.Constraints != nil {
		t.Fatalf("reloaded SharedRoleContext constraints=%v err=%v, want JSON-omitted nil slice", persisted.Constraints, err)
	}

	store := newDelegatedExecutionStoreFake(source, admitted.TaskID, now)
	worker := &privateWorkerFake{result: "No constraints were assigned."}
	executor := newTestDelegatedTaskExecutor(t, store, manager, worker, nil, now)
	result, err := executor.StartOne(ctx)
	if err != nil || !result.ReportSaved || worker.calls != 1 || store.retryCount != 0 {
		t.Fatalf("StartOne()=(%+v,%v) worker_calls=%d retries=%d, want a durable report after reload", result, err, worker.calls, store.retryCount)
	}
}

func TestDelegatedTaskExecutorClosesCurrentRunWhenSharedContextDiffers(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 3, 0, 0, time.UTC)
	manager, closeManager, taskID, source := newExecutionTask(t, t.TempDir())
	defer closeManager()
	shared, err := manager.Context(ctx, taskID)
	if err != nil {
		t.Fatalf("read Task SharedRoleContext: %v", err)
	}
	shared.Constraints = append(shared.Constraints, "tampered after admission")
	if err := manager.UpdateContext(ctx, shared); err != nil {
		t.Fatalf("tamper stored SharedRoleContext for mismatch case: %v", err)
	}
	store := newDelegatedExecutionStoreFake(source, taskID, now)
	worker := &privateWorkerFake{result: "must not run"}
	owner := &executionTaskOwnerFake{Manager: manager}
	executor := newTestDelegatedTaskExecutor(t, store, owner, worker, nil, now)

	result, err := executor.StartOne(ctx)
	if !errors.Is(err, ErrDelegatedExecutionInvalid) || result.ReportSaved || worker.calls != 0 || store.retryCount != 1 || owner.completeRunCalls != 1 {
		t.Fatalf("StartOne()=(%+v,%v) worker_calls=%d retries=%d close_calls=%d, want strict mismatch rejection after closing the exact Run", result, err, worker.calls, store.retryCount, owner.completeRunCalls)
	}
	task, runs, err := manager.ReadTaskRuns(ctx, []modulecore.TaskID{taskID})
	if err != nil || task[taskID].Status != domaintask.StatusWaiting || task[taskID].WaitingReason != delegatedExecutionRetryReason ||
		len(runs[taskID]) != 1 || runs[taskID][0].Status != domaintask.RunStatusWaiting {
		t.Fatalf("Task/Run after context mismatch: task=%+v runs=%+v err=%v, want exact Run waiting for retry", task[taskID], runs[taskID], err)
	}
}

func TestDelegatedTaskExecutorDoesNotCloseSuccessorWhenSharedContextDiffers(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 4, 0, 0, time.UTC)
	manager, closeManager, taskID, source := newExecutionTask(t, t.TempDir())
	defer closeManager()
	shared, err := manager.Context(ctx, taskID)
	if err != nil {
		t.Fatalf("read Task SharedRoleContext: %v", err)
	}
	shared.Constraints = append(shared.Constraints, "tampered after admission")
	if err := manager.UpdateContext(ctx, shared); err != nil {
		t.Fatalf("tamper stored SharedRoleContext for mismatch case: %v", err)
	}
	store := newDelegatedExecutionStoreFake(source, taskID, now)
	worker := &privateWorkerFake{result: "must not run"}
	owner := &executionTaskOwnerFake{Manager: manager}
	var successorID modulecore.RunID
	owner.beforeCompleteRun = func(taskID modulecore.TaskID, runID modulecore.RunID, actorID string) {
		if _, err := owner.Manager.CompleteRun(ctx, taskID, runID, actorID, domaintask.StatusWaiting, "", "concurrent lease retry"); err != nil {
			t.Fatalf("close old Run in mismatch race hook: %v", err)
		}
		successor, err := owner.Manager.StartRunFromCheckpoint(ctx, taskID, runID, actorID,
			domaintask.RunStartReasonCheckpointResume, strings.Repeat("b", 64))
		if err != nil {
			t.Fatalf("start successor Run in mismatch race hook: %v", err)
		}
		successorID = successor.RunID
	}
	executor := newTestDelegatedTaskExecutor(t, store, owner, worker, nil, now)

	result, err := executor.StartOne(ctx)
	if !errors.Is(err, taskmanager.ErrRunConflict) || result.ReportSaved || worker.calls != 0 || store.retryCount != 0 || successorID == "" {
		t.Fatalf("StartOne()=(%+v,%v) worker_calls=%d retries=%d successor=%s, want exact old-Run conflict without retry", result, err, worker.calls, store.retryCount, successorID)
	}
	task, runs, err := manager.ReadTaskRuns(ctx, []modulecore.TaskID{taskID})
	if err != nil || task[taskID].Status != domaintask.StatusRunning || len(runs[taskID]) != 2 ||
		runs[taskID][0].Status != domaintask.RunStatusWaiting || runs[taskID][1].RunID != successorID || runs[taskID][1].Status != domaintask.RunStatusRunning {
		t.Fatalf("Task/Runs after mismatch lease race: task=%+v runs=%+v err=%v, want successor untouched", task[taskID], runs[taskID], err)
	}
}

func TestDelegatedTaskExecutorRejectsSourceChangedDuringGeneration(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 5, 0, 0, time.UTC)
	manager, closeManager, taskID, source := newExecutionTask(t, t.TempDir())
	defer closeManager()
	store := newDelegatedExecutionStoreFake(source, taskID, now)
	owner := &executionTaskOwnerFake{Manager: manager}
	worker := &privateWorkerFake{result: "must not be published"}
	worker.afterRun = func() {
		store.source.Readback.Text = "accepted source changed during generation"
	}
	executor := newTestDelegatedTaskExecutor(t, store, owner, worker, nil, now)

	result, err := executor.StartOne(ctx)
	if err == nil || !result.Generated || result.ReportSaved || result.ReportPending || worker.calls != 1 {
		t.Fatalf("StartOne()=(%+v,%v) worker_calls=%d, want generated text rejected after source revision changed", result, err, worker.calls)
	}
	if owner.completeCalls != 0 || owner.effectCalls != 0 {
		t.Fatalf("Task completion calls=%d ExecuteRunEffect calls=%d, want neither completion nor a whole-agent fence", owner.completeCalls, owner.effectCalls)
	}
	if store.retryCount != 1 {
		t.Fatalf("retry count=%d, want changed source to retry the lease", store.retryCount)
	}
	reports, err := manager.ListPendingReports(ctx)
	if err != nil || len(reports) != 0 {
		t.Fatalf("ListPendingReports()=%+v err=%v, want no report for stale generated output", reports, err)
	}
	task, runs, err := manager.ReadTaskRuns(ctx, []modulecore.TaskID{taskID})
	if err != nil || task[taskID].Status != domaintask.StatusWaiting || len(runs[taskID]) != 1 || runs[taskID][0].Status != domaintask.RunStatusWaiting {
		t.Fatalf("Task/Run after stale source detection: task=%+v runs=%+v err=%v, want retryable waiting state", task[taskID], runs[taskID], err)
	}
}

func TestDelegatedTaskExecutorClosesRunAndRetriesLeaseAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Date(2026, 10, 22, 12, 15, 0, 0, time.UTC)
	manager, closeManager, taskID, source := newExecutionTask(t, t.TempDir())
	defer closeManager()
	store := newDelegatedExecutionStoreFake(source, taskID, now)
	worker := &privateWorkerFake{result: "canceled after generation"}
	worker.afterRun = cancel
	owner := &executionTaskOwnerFake{Manager: manager}
	executor := newTestDelegatedTaskExecutor(t, store, owner, worker, nil, now)

	result, err := executor.StartOne(ctx)
	if !errors.Is(err, context.Canceled) || result.Generated || result.ReportSaved || store.retryCount != 1 {
		t.Fatalf("StartOne()=(%+v,%v) retries=%d, want cancellation before report generation is accepted and a retryable lease", result, err, store.retryCount)
	}
	task, runs, readErr := manager.ReadTaskRuns(context.Background(), []modulecore.TaskID{taskID})
	if readErr != nil || task[taskID].Status != domaintask.StatusWaiting || task[taskID].WaitingReason != delegatedExecutionRetryReason ||
		len(runs[taskID]) != 1 || runs[taskID][0].RunID != result.RunID || runs[taskID][0].Status != domaintask.RunStatusWaiting {
		t.Fatalf("Task/Run after cancellation: task=%+v runs=%+v err=%v, want exact Run waiting for automatic checkpoint retry", task[taskID], runs[taskID], readErr)
	}
	if owner.completeRunCalls != 1 || owner.completeRunSawCanceled || !owner.completeRunHasDeadline || store.retrySawCanceled || !store.retryHasDeadline {
		t.Fatalf("cleanup/retry contexts: closeCalls=%d closeCanceled=%v closeDeadline=%v retryCanceled=%v retryDeadline=%v, want detached bounded writes",
			owner.completeRunCalls, owner.completeRunSawCanceled, owner.completeRunHasDeadline, store.retrySawCanceled, store.retryHasDeadline)
	}
}

func TestDelegatedTaskExecutorSurfacesRunClosureFailureWithoutRetryingLease(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 17, 0, 0, time.UTC)
	manager, closeManager, taskID, source := newExecutionTask(t, t.TempDir())
	defer closeManager()
	store := newDelegatedExecutionStoreFake(source, taskID, now)
	worker := &privateWorkerFake{result: "text that cannot be committed"}
	worker.afterRun = func() { store.source.Readback.Text = "changed before report commit" }
	injected := errors.New("injected waiting transition failure")
	owner := &executionTaskOwnerFake{Manager: manager, completeRunErr: injected}
	executor := newTestDelegatedTaskExecutor(t, store, owner, worker, nil, now)

	result, err := executor.StartOne(ctx)
	if !errors.Is(err, injected) || !result.Generated || result.ReportSaved || store.retryCount != 0 || owner.completeRunCalls != 1 {
		t.Fatalf("StartOne()=(%+v,%v) retries=%d closeCalls=%d, want surfaced closure failure and no lease retry", result, err, store.retryCount, owner.completeRunCalls)
	}
	task, runs, readErr := manager.ReadTaskRuns(ctx, []modulecore.TaskID{taskID})
	if readErr != nil || task[taskID].Status != domaintask.StatusRunning || len(runs[taskID]) != 1 || runs[taskID][0].Status != domaintask.RunStatusRunning {
		t.Fatalf("Task/Run after refused closure: task=%+v runs=%+v err=%v, want active state visible with error", task[taskID], runs[taskID], readErr)
	}
}

func TestDelegatedTaskExecutorCannotCloseSuccessorRunAfterLeaseRace(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 18, 0, 0, time.UTC)
	manager, closeManager, taskID, source := newExecutionTask(t, t.TempDir())
	defer closeManager()
	store := newDelegatedExecutionStoreFake(source, taskID, now)
	worker := &privateWorkerFake{result: "stale text"}
	worker.afterRun = func() { store.source.Readback.Text = "changed before report commit" }
	owner := &executionTaskOwnerFake{Manager: manager}
	var successorID modulecore.RunID
	owner.beforeCompleteRun = func(taskID modulecore.TaskID, runID modulecore.RunID, actorID string) {
		if _, err := owner.Manager.CompleteRun(ctx, taskID, runID, actorID, domaintask.StatusWaiting, "", "concurrent lease retry"); err != nil {
			t.Fatalf("close expired attempt Run in race hook: %v", err)
		}
		successor, err := owner.Manager.StartRunFromCheckpoint(ctx, taskID, runID, actorID,
			domaintask.RunStartReasonCheckpointResume, strings.Repeat("a", 64))
		if err != nil {
			t.Fatalf("start later lease successor in race hook: %v", err)
		}
		successorID = successor.RunID
	}
	executor := newTestDelegatedTaskExecutor(t, store, owner, worker, nil, now)

	result, err := executor.StartOne(ctx)
	if !errors.Is(err, taskmanager.ErrRunConflict) || !result.Generated || result.ReportSaved || store.retryCount != 0 || successorID == "" {
		t.Fatalf("StartOne()=(%+v,%v) retries=%d successor=%s, want stale-run closure conflict without retry", result, err, store.retryCount, successorID)
	}
	task, runs, readErr := manager.ReadTaskRuns(ctx, []modulecore.TaskID{taskID})
	if readErr != nil || task[taskID].Status != domaintask.StatusRunning || len(runs[taskID]) != 2 ||
		runs[taskID][0].Status != domaintask.RunStatusWaiting || runs[taskID][1].RunID != successorID || runs[taskID][1].Status != domaintask.RunStatusRunning {
		t.Fatalf("Task/Runs after lease race: task=%+v runs=%+v err=%v, want later successor untouched", task[taskID], runs[taskID], readErr)
	}
}

func TestDelegatedTaskExecutorStoresWorkerFailureAsFullFailedReport(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 10, 0, 0, time.UTC)
	manager, closeManager, taskID, source := newExecutionTask(t, t.TempDir())
	defer closeManager()
	store := newDelegatedExecutionStoreFake(source, taskID, now)
	worker := &privateWorkerFake{err: errors.New("provider detail must not be exposed")}
	executor := newTestDelegatedTaskExecutor(t, store, manager, worker, nil, now)

	result, err := executor.StartOne(ctx)
	if err != nil {
		t.Fatalf("StartOne() after worker failure: %v", err)
	}
	if result.Generated || !result.ReportSaved || !result.ReportPending {
		t.Fatalf("StartOne()=%+v, want a durable failed report without generated result", result)
	}
	reports, err := manager.ListPendingReports(ctx)
	if err != nil || len(reports) != 1 {
		t.Fatalf("ListPendingReports()=%+v err=%v", reports, err)
	}
	if reports[0].TaskStatus != domaintask.StatusFailed || reports[0].Envelope.Status != conversation.ReportStatusFailed ||
		reports[0].Envelope.Result != delegatedExecutionFailureText || strings.Contains(reports[0].Envelope.Result, "provider detail") ||
		len(reports[0].Envelope.Limitations) == 0 {
		t.Fatalf("failed Task report=%+v, want safe full failed report", reports[0])
	}
}

func TestDelegatedTaskExecutorRetriesUncommittedReportWithCheckpointSuccessor(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 20, 0, 0, time.UTC)
	manager, closeManager, taskID, source := newExecutionTask(t, t.TempDir())
	defer closeManager()
	queued, err := manager.Create(ctx, domaintask.Task{Title: "capacity probe", Route: domaintask.RouteGeneral, Assignee: "mio"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create queued capacity probe: %v", err)
	}
	for _, assignee := range []string{"mio", "midori"} {
		if _, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{
			Title: "parallel capacity holder", Route: domaintask.RouteGeneral, Assignee: assignee,
		}, domaintask.SharedRoleContext{}); err != nil {
			t.Fatalf("start parallel capacity holder for %s: %v", assignee, err)
		}
	}
	if allowed, reason, err := manager.CanStart(ctx, queued); err != nil || allowed || reason != "global running task limit reached" {
		t.Fatalf("CanStart before retry cleanup = allowed=%v reason=%q err=%v, want capacity held", allowed, reason, err)
	}
	store := newDelegatedExecutionStoreFake(source, taskID, now)
	worker := &privateWorkerFake{result: "retry-safe text"}
	owner := &executionTaskOwnerFake{Manager: manager, completeFailures: 1}
	executor := newTestDelegatedTaskExecutor(t, store, owner, worker, nil, now)

	first, err := executor.StartOne(ctx)
	if err == nil || !first.Generated || first.ReportSaved || store.retryCount != 1 {
		t.Fatalf("first StartOne()=(%+v,%v), want generated text, failed report commit, and retry", first, err)
	}
	task, runs, err := manager.ReadTaskRuns(ctx, []modulecore.TaskID{taskID})
	if err != nil || task[taskID].Status != domaintask.StatusWaiting || len(runs[taskID]) != 1 ||
		runs[taskID][0].RunID != first.RunID || runs[taskID][0].Status != domaintask.RunStatusWaiting || task[taskID].WaitingReason == "" {
		t.Fatalf("Task/Run after failed report commit: task=%+v runs=%+v err=%v, want exact Run waiting", task[taskID], runs[taskID], err)
	}
	if allowed, reason, err := manager.CanStart(ctx, queued); err != nil || !allowed || reason != "" {
		t.Fatalf("CanStart after retry cleanup = allowed=%v reason=%q err=%v, want released execution slot", allowed, reason, err)
	}
	_, shared, err := delegatedTaskDraft(source, "shiro")
	if err != nil {
		t.Fatalf("build immutable delegated Task source: %v", err)
	}
	shared.TaskID = taskID
	expectedCheckpointHash, err := delegatedExecutionCheckpointHash(DelegatedExecutionSource{
		OwnerID: source.OwnerID, TaskID: taskID, Handoff: source.Handoff, Readback: source.Readback, Context: source.Context,
	}, shared)
	if err != nil {
		t.Fatalf("hash immutable delegated source: %v", err)
	}
	second, err := executor.StartOne(ctx)
	if err != nil || !second.ReportSaved || second.TaskID != first.TaskID || second.RunID == first.RunID || worker.calls != 2 {
		t.Fatalf("retry StartOne()=(%+v,%v), want same Task, a new checkpoint Run, and safe text replay", second, err)
	}
	_, runs, err = manager.ReadTaskRuns(ctx, []modulecore.TaskID{taskID})
	if err != nil || len(runs[taskID]) != 2 || runs[taskID][1].RunID != second.RunID ||
		runs[taskID][1].Status != domaintask.RunStatusSucceeded || runs[taskID][1].StartReason != domaintask.RunStartReasonCheckpointResume ||
		runs[taskID][1].StartCheckpointSHA256 != expectedCheckpointHash {
		t.Fatalf("Task Run history after checkpoint retry=%+v err=%v, want succeeded source-bound successor", runs[taskID], err)
	}
}

func TestDelegatedTaskExecutorRecoversStaleRunAndCheckpointResumesTextOnly(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 30, 0, 0, time.UTC)
	root := t.TempDir()
	manager, closeManager, taskID, source := newExecutionTask(t, root)
	firstTask, firstRuns, err := manager.ReadTaskRuns(ctx, []modulecore.TaskID{taskID})
	if err != nil || len(firstRuns[taskID]) != 1 {
		t.Fatalf("initial Task/Run read: task=%+v runs=%+v err=%v", firstTask[taskID], firstRuns[taskID], err)
	}
	firstRunID := firstRuns[taskID][0].RunID
	if err := closeManager(); err != nil {
		t.Fatalf("close initial Task owner: %v", err)
	}
	manager, closeManager = openDelegatedTaskManager(t, root)
	defer closeManager()
	store := newDelegatedExecutionStoreFake(source, taskID, now)
	worker := &privateWorkerFake{result: "resumed text"}
	executor := newTestDelegatedTaskExecutor(t, store, manager, worker, nil, now)
	result, err := executor.StartOne(ctx)
	if err != nil || !result.ReportSaved || result.RunID == firstRunID {
		t.Fatalf("StartOne after process restart=(%+v,%v), want a checkpoint successor and durable report", result, err)
	}
	_, runs, err := manager.ReadTaskRuns(ctx, []modulecore.TaskID{taskID})
	if err != nil || len(runs[taskID]) != 2 || runs[taskID][0].Status != domaintask.RunStatusWaiting ||
		runs[taskID][1].RunID != result.RunID || runs[taskID][1].StartReason != domaintask.RunStartReasonCheckpointResume ||
		runs[taskID][1].StartCheckpointSHA256 == "" || runs[taskID][1].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("recovered Run history=%+v err=%v, want waiting predecessor and completed source-hash successor", runs[taskID], err)
	}
}

func TestDelegatedTaskExecutorRecoversCheckpointSuccessorAfterSecondCrash(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 35, 0, 0, time.UTC)
	root := t.TempDir()
	manager, closeManager, taskID, source := newExecutionTask(t, root)
	_, runs, err := manager.ReadTaskRuns(ctx, []modulecore.TaskID{taskID})
	if err != nil || len(runs[taskID]) != 1 {
		t.Fatalf("initial Task/Run read: runs=%+v err=%v", runs[taskID], err)
	}
	firstRunID := runs[taskID][0].RunID
	if err := closeManager(); err != nil {
		t.Fatalf("close initial Task owner: %v", err)
	}
	manager, closeManager = openDelegatedTaskManager(t, root)
	_, recovered, err := manager.RecoverRunAfterProcessRestart(ctx, taskID, firstRunID, "shiro", domaintask.StatusWaiting, "", delegatedExecutionWaitingReason)
	if err != nil || !recovered {
		t.Fatalf("recover first stale Run: recovered=%v err=%v", recovered, err)
	}
	_, shared, err := delegatedTaskDraft(source, "shiro")
	if err != nil {
		t.Fatalf("build immutable delegated Task source: %v", err)
	}
	shared.TaskID = taskID
	checkpointHash, err := delegatedExecutionCheckpointHash(DelegatedExecutionSource{
		OwnerID: source.OwnerID, TaskID: taskID, Handoff: source.Handoff, Readback: source.Readback, Context: source.Context,
	}, shared)
	if err != nil {
		t.Fatalf("hash immutable delegated source: %v", err)
	}
	checkpointRun, err := manager.StartRunFromCheckpoint(ctx, taskID, firstRunID, "shiro", domaintask.RunStartReasonCheckpointResume, checkpointHash)
	if err != nil {
		t.Fatalf("start checkpoint successor before simulated crash: %v", err)
	}
	if err := closeManager(); err != nil {
		t.Fatalf("close Task owner after checkpoint start: %v", err)
	}
	manager, closeManager = openDelegatedTaskManager(t, root)
	defer closeManager()
	store := newDelegatedExecutionStoreFake(source, taskID, now)
	worker := &privateWorkerFake{result: "safe replay after successor recovery"}
	executor := newTestDelegatedTaskExecutor(t, store, manager, worker, nil, now)
	result, err := executor.StartOne(ctx)
	if err != nil || !result.ReportSaved || result.RunID == checkpointRun.RunID || worker.calls != 1 {
		t.Fatalf("StartOne after successor crash=(%+v,%v), want exact stale successor recovery then new source-bound Run", result, err)
	}
	_, runs, err = manager.ReadTaskRuns(ctx, []modulecore.TaskID{taskID})
	if err != nil || len(runs[taskID]) != 3 || runs[taskID][0].Status != domaintask.RunStatusWaiting ||
		runs[taskID][1].RunID != checkpointRun.RunID || runs[taskID][1].Status != domaintask.RunStatusWaiting ||
		runs[taskID][2].RunID != result.RunID || runs[taskID][2].Status != domaintask.RunStatusSucceeded ||
		runs[taskID][2].StartReason != domaintask.RunStartReasonCheckpointResume || runs[taskID][2].StartCheckpointSHA256 != checkpointHash {
		t.Fatalf("Task Run history=%+v err=%v, want both stale Run generations closed and exact source checkpoint resumed", runs[taskID], err)
	}
}

func TestDelegatedTaskExecutorReconcilesLostReportCompletionResponse(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 40, 0, 0, time.UTC)
	manager, closeManager, taskID, source := newExecutionTask(t, t.TempDir())
	defer closeManager()
	store := newDelegatedExecutionStoreFake(source, taskID, now)
	worker := &privateWorkerFake{result: "committed before lost response"}
	owner := &executionTaskOwnerFake{Manager: manager, persistThenFail: true}
	executor := newTestDelegatedTaskExecutor(t, store, owner, worker, nil, now)

	first, err := executor.StartOne(ctx)
	if err != nil || !first.Generated || !first.ReportSaved || !first.ReportPending || first.ReportID == "" ||
		store.retryCount != 0 || worker.calls != 1 {
		t.Fatalf("StartOne()=(%+v,%v), retries=%d worker_calls=%d, want committed report reconciled without closing or retrying", first, err, store.retryCount, worker.calls)
	}
	reports, err := manager.ListPendingReports(ctx)
	if err != nil || len(reports) != 1 || reports[0].ReportID != first.ReportID || reports[0].RunID != first.RunID {
		t.Fatalf("pending report after lost response=%+v err=%v, want exact committed report", reports, err)
	}
}

func TestDelegatedTaskExecutorNeverCallsKuroForKuroLease(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 50, 0, 0, time.UTC)
	manager, closeManager, taskID, source := newExecutionTask(t, t.TempDir())
	defer closeManager()
	store := newDelegatedExecutionStoreFake(source, taskID, now)
	store.lease.Assignee = "kuro"
	worker := &privateWorkerFake{result: "must not run"}
	executor := newTestDelegatedTaskExecutor(t, store, manager, worker, worker, now)
	result, err := executor.StartOne(ctx)
	if err == nil || !result.Claimed || worker.calls != 0 || store.retryCount != 1 || store.loadCount != 0 {
		t.Fatalf("Kuro lease result=(%+v,%v) worker_calls=%d retries=%d loads=%d, want fail-closed without a worker/source call", result, err, worker.calls, store.retryCount, store.loadCount)
	}
}

func TestDelegatedTaskExecutorRejectsDelegatedCapabilitiesBeforeWorker(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 55, 0, 0, time.UTC)
	manager, closeManager, taskID, source := newExecutionTask(t, t.TempDir())
	defer closeManager()
	store := newDelegatedExecutionStoreFake(source, taskID, now)
	store.source.Handoff.AllowedCapabilities = []string{"codex.run"}
	worker := &privateWorkerFake{result: "must not run"}
	executor := newTestDelegatedTaskExecutor(t, store, manager, worker, nil, now)
	result, err := executor.StartOne(ctx)
	if err == nil || !result.Claimed || worker.calls != 0 || store.retryCount != 1 {
		t.Fatalf("capability-bearing source result=(%+v,%v) worker_calls=%d retries=%d, want fail-closed before worker", result, err, worker.calls, store.retryCount)
	}
}

func newExecutionTask(t *testing.T, root string) (*taskmanager.Manager, func() error, modulecore.TaskID, DelegatedTaskSource) {
	t.Helper()
	now := time.Date(2026, 10, 22, 11, 0, 0, 0, time.UTC)
	manager, closeManager := openDelegatedTaskManager(t, root)
	admissionStore := newDelegatedTaskAdmissionStore(now)
	admission := newDelegatedTaskAdmission(t, admissionStore, manager, now, "shiro")
	result, err := admission.StartOne(context.Background())
	if err != nil || !result.Bound || result.TaskID == "" {
		_ = closeManager()
		t.Fatalf("admit delegated Task for execution test: result=%+v err=%v", result, err)
	}
	return manager, closeManager, result.TaskID, admissionStore.source
}

func newTestDelegatedTaskExecutor(t *testing.T, store DelegatedExecutionStore, tasks DelegatedTaskExecutionOwner, shiro, midori DelegatedPrivateWorker, now time.Time) *DelegatedTaskExecutor {
	t.Helper()
	executor, err := NewDelegatedTaskExecutor(DelegatedTaskExecutionDependencies{
		Store: store, Tasks: tasks, Shiro: shiro, Midori: midori,
		Config: DelegatedTaskExecutionConfig{Now: func() time.Time { return now }},
	})
	if err != nil {
		t.Fatalf("NewDelegatedTaskExecutor(): %v", err)
	}
	return executor
}

type delegatedExecutionStoreFake struct {
	lease            DelegatedExecutionLease
	source           DelegatedTaskSource
	taskID           modulecore.TaskID
	claimCount       int
	loadCount        int
	retryCount       int
	retrySawCanceled bool
	retryHasDeadline bool
}

func newDelegatedExecutionStoreFake(source DelegatedTaskSource, taskID modulecore.TaskID, now time.Time) *delegatedExecutionStoreFake {
	return &delegatedExecutionStoreFake{
		lease: DelegatedExecutionLease{
			WorkID: "delegated-execution-work", DedupeKey: fmt.Sprintf("delegation:%s:%d:execute", source.Handoff.DelegationID, source.Handoff.Revision),
			Kind: ExecuteDelegatedTaskWorkKind, SourceEventID: modulecore.NewEventID(), SessionID: source.Handoff.SessionID,
			LeaseToken: "execution-lease-1", LeaseExpires: now.Add(time.Minute), Version: 1, Assignee: source.Handoff.ToActor,
		},
		source: source, taskID: taskID,
	}
}

func (s *delegatedExecutionStoreFake) ClaimDelegatedExecutionWork(_ context.Context, request DelegatedExecutionClaimRequest) ([]DelegatedExecutionLease, error) {
	if request.Kind != ExecuteDelegatedTaskWorkKind || request.Limit != 1 || request.LeaseDuration <= 0 {
		return nil, fmt.Errorf("unexpected delegated execution claim: %+v", request)
	}
	s.claimCount++
	s.lease.LeaseToken = fmt.Sprintf("execution-lease-%d", s.claimCount)
	s.lease.Version = int64(s.claimCount)
	s.lease.LeaseExpires = request.Now.Add(request.LeaseDuration)
	return []DelegatedExecutionLease{s.lease}, nil
}

func (s *delegatedExecutionStoreFake) LoadDelegatedExecution(_ context.Context, lease DelegatedExecutionLease, actorID string, now time.Time) (DelegatedExecutionSource, error) {
	s.loadCount++
	if lease != s.lease || !s.lease.LeaseExpires.After(now) || actorID != s.lease.Assignee {
		return DelegatedExecutionSource{}, errors.New("stale execution lease or wrong Actor")
	}
	return DelegatedExecutionSource{
		Lease: lease, OwnerID: s.source.OwnerID, TaskID: s.taskID,
		Handoff: s.source.Handoff, Readback: s.source.Readback, Context: s.source.Context,
	}, nil
}

func (s *delegatedExecutionStoreFake) RetryDelegatedExecutionWork(ctx context.Context, lease DelegatedExecutionLease, _ time.Time) error {
	s.retrySawCanceled = ctx.Err() != nil
	_, s.retryHasDeadline = ctx.Deadline()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if lease.WorkID != s.lease.WorkID || lease.LeaseToken != s.lease.LeaseToken || lease.Version != s.lease.Version {
		return errors.New("stale delegated execution retry lease")
	}
	s.retryCount++
	return nil
}

type privateWorkerFake struct {
	result       string
	err          error
	afterRun     func()
	calls        int
	actor        string
	capabilities []string
	ctx          context.Context
	input        conversation.TurnInput
}

func (w *privateWorkerFake) ExecutePrivateWork(ctx context.Context, input conversation.TurnInput, capabilities []string) (string, error) {
	w.calls++
	w.ctx, w.input = ctx, input
	w.capabilities = append([]string(nil), capabilities...)
	if scope, ok := tool.ToolExecutionScopeFromContext(ctx); ok {
		w.actor = scope.ActorID
	}
	if w.afterRun != nil {
		w.afterRun()
	}
	return w.result, w.err
}

type executionTaskOwnerFake struct {
	*taskmanager.Manager
	completeFailures       int
	persistThenFail        bool
	completeRunErr         error
	beforeCompleteRun      func(modulecore.TaskID, modulecore.RunID, string)
	completeRunCalls       int
	completeRunSawCanceled bool
	completeRunHasDeadline bool
	effectCalls            int
	completeCalls          int
}

func (o *executionTaskOwnerFake) ExecuteRunEffect(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, effect func(context.Context) error) error {
	o.effectCalls++
	return o.Manager.ExecuteRunEffect(ctx, taskID, runID, actorID, effect)
}

func (o *executionTaskOwnerFake) CompleteWithReport(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, status domaintask.Status, report conversation.ReportEnvelope) (domaintask.Task, error) {
	o.completeCalls++
	if o.completeFailures > 0 {
		o.completeFailures--
		return domaintask.Task{}, errors.New("injected pre-commit report persistence failure")
	}
	completed, err := o.Manager.CompleteWithReport(ctx, taskID, runID, actorID, status, report)
	if err != nil {
		return completed, err
	}
	if o.persistThenFail {
		o.persistThenFail = false
		return completed, errors.New("injected lost response after durable report commit")
	}
	return completed, nil
}

func (o *executionTaskOwnerFake) CompleteRun(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, status domaintask.Status, summary string, waitingReason string) (domaintask.Task, error) {
	o.completeRunCalls++
	o.completeRunSawCanceled = ctx.Err() != nil
	_, o.completeRunHasDeadline = ctx.Deadline()
	if o.beforeCompleteRun != nil {
		before := o.beforeCompleteRun
		o.beforeCompleteRun = nil
		before(taskID, runID, actorID)
	}
	if o.completeRunErr != nil {
		return domaintask.Task{}, o.completeRunErr
	}
	return o.Manager.CompleteRun(ctx, taskID, runID, actorID, status, summary, waitingReason)
}
