package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestEnsureTaskForDelegationCreatesOneTaskRunAndContext(t *testing.T) {
	root := t.TempDir()
	store, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	delegationID := modulecore.NewRequestID()
	var checks int
	check := func(context.Context, modulecore.RequestID, uint64) error {
		checks++
		return nil
	}
	draft := domaintask.Task{Title: "delegated task", Route: domaintask.RouteResearch, Assignee: "shiro"}
	shared := domaintask.SharedRoleContext{UserIntent: "find current evidence", CurrentPlan: "research the request"}

	task, run, err := manager.EnsureTaskForDelegation(context.Background(), delegationID, 1, draft, shared, check)
	if err != nil {
		t.Fatalf("EnsureTaskForDelegation: %v", err)
	}
	if task.Status != domaintask.StatusRunning || task.StartedAt == nil {
		t.Fatalf("task=%#v, want running Task", task)
	}
	if run.TaskID != task.TaskID || run.RunID == "" || run.StartReason != domaintask.RunStartReasonFirst || run.Status != domaintask.RunStatusRunning {
		t.Fatalf("run=%#v, want the Task's first running Run", run)
	}
	if checks != 2 {
		t.Fatalf("admission checks=%d, want preflight and pre-commit checks", checks)
	}
	storedContext, err := manager.Context(context.Background(), task.TaskID)
	if err != nil || storedContext.TaskID != task.TaskID || storedContext.UserIntent != shared.UserIntent || storedContext.CurrentPlan != shared.CurrentPlan {
		t.Fatalf("context=%#v err=%v", storedContext, err)
	}
	assertStoreCounts(t, manager, 1, 1)

	receipt, err := store.LookupTaskOperation(context.Background(), fmt.Sprintf("delegation:%s:1:start", delegationID))
	if err != nil {
		t.Fatalf("LookupTaskOperation: %v", err)
	}
	if receipt.Scope != taskpersistence.TaskOperationScopeGlobal || receipt.TaskID != "" || len(receipt.Result) > 256 {
		t.Fatalf("receipt=%+v, want a small global receipt", receipt)
	}
}

func TestDelegationTaskOperationIDIsCanonicalCrossStoreIdentity(t *testing.T) {
	delegationID := modulecore.NewRequestID()
	if got, want := DelegationTaskOperationID(delegationID, 17), fmt.Sprintf("delegation:%s:17:start", delegationID); got != want {
		t.Fatalf("DelegationTaskOperationID()=%q, want %q", got, want)
	}
}

func TestEnsureTaskForDelegationReplaysSameTaskAndRunAfterRestart(t *testing.T) {
	root := t.TempDir()
	store, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	manager := New(store, DefaultParallelLimits())
	delegationID := modulecore.NewRequestID()
	draft := domaintask.Task{Title: "recover delegated task", Route: domaintask.RouteGeneral, Assignee: "shiro"}
	shared := domaintask.SharedRoleContext{UserIntent: "recover same task"}
	var checks int
	check := func(_ context.Context, gotDelegationID modulecore.RequestID, gotRevision uint64) error {
		checks++
		if gotDelegationID != delegationID || gotRevision != 4 {
			t.Errorf("admission check identity=(%s,%d), want=(%s,4)", gotDelegationID, gotRevision, delegationID)
		}
		return nil
	}

	firstTask, firstRun, err := manager.EnsureTaskForDelegation(context.Background(), delegationID, 4, draft, shared, check)
	if err != nil {
		t.Fatalf("first EnsureTaskForDelegation: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close Task store: %v", err)
	}

	reopened, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatalf("reopen Task store: %v", err)
	}
	registerTaskStoreCleanup(t, reopened)
	restartedManager := New(reopened, DefaultParallelLimits())
	secondTask, secondRun, err := restartedManager.EnsureTaskForDelegation(context.Background(), delegationID, 4, draft, shared, check)
	if err != nil {
		t.Fatalf("replayed EnsureTaskForDelegation: %v", err)
	}
	if secondTask.TaskID != firstTask.TaskID || secondRun.RunID != firstRun.RunID {
		t.Fatalf("replay Task/Run=(%s,%s), want original=(%s,%s)", secondTask.TaskID, secondRun.RunID, firstTask.TaskID, firstRun.RunID)
	}
	if checks != 3 {
		t.Fatalf("admission checks=%d, want checks on first preflight/pre-commit and replay preflight only", checks)
	}
	assertStoreCounts(t, restartedManager, 1, 1)
}

func TestEnsureTaskForDelegationRejectsChangedRequestForSameRevision(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	delegationID := modulecore.NewRequestID()
	check := func(context.Context, modulecore.RequestID, uint64) error { return nil }
	first, _, err := manager.EnsureTaskForDelegation(context.Background(), delegationID, 2,
		domaintask.Task{Title: "original", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{UserIntent: "original"}, check)
	if err != nil {
		t.Fatalf("first EnsureTaskForDelegation: %v", err)
	}
	if _, _, err := manager.EnsureTaskForDelegation(context.Background(), delegationID, 2,
		domaintask.Task{Title: "changed", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{UserIntent: "original"}, check); !errors.Is(err, taskpersistence.ErrTaskOperationConflict) {
		t.Fatalf("changed request error=%v, want ErrTaskOperationConflict", err)
	}
	stored, err := manager.Get(context.Background(), first.TaskID)
	if err != nil || stored.Title != "original" {
		t.Fatalf("stored Task=%#v err=%v, want original unchanged", stored, err)
	}
	assertStoreCounts(t, manager, 1, 1)
}

func TestEnsureTaskForDelegationUsesNewOperationForNewRevision(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	delegationID := modulecore.NewRequestID()
	check := func(context.Context, modulecore.RequestID, uint64) error { return nil }
	draft := domaintask.Task{Title: "revisioned delegated task", Route: domaintask.RouteGeneral}
	first, _, err := manager.EnsureTaskForDelegation(context.Background(), delegationID, 1, draft, domaintask.SharedRoleContext{}, check)
	if err != nil {
		t.Fatalf("revision 1: %v", err)
	}
	second, _, err := manager.EnsureTaskForDelegation(context.Background(), delegationID, 2, draft, domaintask.SharedRoleContext{}, check)
	if err != nil {
		t.Fatalf("revision 2: %v", err)
	}
	if first.TaskID == second.TaskID {
		t.Fatalf("revision 1 and 2 share TaskID %s", first.TaskID)
	}
	assertStoreCounts(t, manager, 2, 2)
}

func TestEnsureTaskForDelegationKeepsTaskCapacityAdmission(t *testing.T) {
	limits := DefaultParallelLimits()
	limits.Global = 1
	manager := newCreateAndStartManager(t, limits)
	if _, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "occupant", Route: domaintask.RouteGeneral,
	}, domaintask.SharedRoleContext{}); err != nil {
		t.Fatalf("start capacity occupant: %v", err)
	}
	delegationID := modulecore.NewRequestID()
	taskID := modulecore.NewTaskID()
	check := func(context.Context, modulecore.RequestID, uint64) error { return nil }
	_, _, err := manager.EnsureTaskForDelegation(context.Background(), delegationID, 1,
		domaintask.Task{TaskID: taskID, Title: "capacity refused", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{}, check)
	if !errors.Is(err, ErrParallelLimit) {
		t.Fatalf("EnsureTaskForDelegation error=%v, want ErrParallelLimit", err)
	}
	assertStoreCounts(t, manager, 1, 1)
	assertDelegationReceiptAbsent(t, manager, delegationID, 1)
	if _, err := manager.Get(context.Background(), taskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("capacity-refused Task lookup error=%v, want ErrNotFound", err)
	}
	if _, err := manager.Context(context.Background(), taskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("capacity-refused context lookup error=%v, want ErrNotFound", err)
	}
}

func TestEnsureTaskForDelegationPreCommitRejectionRollsBackTaskRunAndContext(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	delegationID := modulecore.NewRequestID()
	taskID := modulecore.NewTaskID()
	canceled := errors.New("delegation was canceled before Task commit")
	checks := 0
	check := func(context.Context, modulecore.RequestID, uint64) error {
		checks++
		if checks == 2 {
			return canceled
		}
		return nil
	}
	_, _, err := manager.EnsureTaskForDelegation(context.Background(), delegationID, 3,
		domaintask.Task{TaskID: taskID, Title: "must roll back", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{UserIntent: "must roll back"}, check)
	if !errors.Is(err, canceled) {
		t.Fatalf("EnsureTaskForDelegation error=%v, want pre-commit cancellation", err)
	}
	if checks != 2 {
		t.Fatalf("admission checks=%d, want preflight then pre-commit check", checks)
	}
	assertStoreCounts(t, manager, 0, 0)
	assertDelegationReceiptAbsent(t, manager, delegationID, 3)
	if _, err := manager.Get(context.Background(), taskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("rolled-back Task lookup error=%v, want ErrNotFound", err)
	}
	if _, err := manager.Context(context.Background(), taskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("rolled-back context lookup error=%v, want ErrNotFound", err)
	}
}

func TestEnsureTaskForDelegationRejectsUnauthorizedInputBeforeWriting(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	delegationID := modulecore.NewRequestID()
	taskID := modulecore.NewTaskID()
	unauthorized := errors.New("delegation is not authorized")
	checks := 0
	check := func(context.Context, modulecore.RequestID, uint64) error {
		checks++
		return unauthorized
	}
	_, _, err := manager.EnsureTaskForDelegation(context.Background(), delegationID, 1,
		domaintask.Task{TaskID: taskID, Title: "unauthorized", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{}, check)
	if !errors.Is(err, unauthorized) {
		t.Fatalf("unauthorized request error=%v", err)
	}
	if checks != 1 {
		t.Fatalf("admission checks=%d, want one preflight check", checks)
	}
	assertStoreCounts(t, manager, 0, 0)
	assertDelegationReceiptAbsent(t, manager, delegationID, 1)
	if _, err := manager.Get(context.Background(), taskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("unauthorized Task lookup error=%v, want ErrNotFound", err)
	}
	if _, err := manager.Context(context.Background(), taskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("unauthorized context lookup error=%v, want ErrNotFound", err)
	}
}

func assertDelegationReceiptAbsent(t *testing.T, manager *Manager, delegationID modulecore.RequestID, revision uint64) {
	t.Helper()
	store, ok := manager.store.(*taskpersistence.JSONLStore)
	if !ok {
		t.Fatalf("Task store type=%T, want JSONLStore", manager.store)
	}
	operationID := fmt.Sprintf("delegation:%s:%d:start", delegationID, revision)
	if _, err := store.LookupTaskOperation(context.Background(), operationID); !errors.Is(err, taskpersistence.ErrTaskOperationNotFound) {
		t.Fatalf("delegation receipt error=%v, want ErrTaskOperationNotFound", err)
	}
}
