package taskmanager

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestManagerCompleteRunSuccessAtomicallyClosesTaskAndRun(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	task, run := newRunningExecutionTaskRun(t, manager, "Mio")

	completed, err := manager.CompleteRun(ctx, task.TaskID, run.RunID, "Mio", domaintask.StatusSucceeded, "finished", "")
	if err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	if completed.Status != domaintask.StatusSucceeded || completed.Summary != "finished" {
		t.Fatalf("returned Task = %#v", completed)
	}
	persistedTask, err := manager.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedTask.Status != domaintask.StatusSucceeded || persistedTask.Summary != "finished" {
		t.Fatalf("persisted Task = %#v", persistedTask)
	}
	persistedRun, err := manager.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedRun.Status != domaintask.RunStatusSucceeded || persistedRun.Summary != "finished" || persistedRun.CompletedAt == nil {
		t.Fatalf("persisted Run = %#v", persistedRun)
	}
	notifications, err := manager.Notifications(ctx, 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(notifications) != 1 || notifications[0].TaskID != task.TaskID || notifications[0].Status != domaintask.StatusSucceeded {
		t.Fatalf("notifications = %#v", notifications)
	}
}

func TestManagerCompleteRunRejectsWrongActorAndTaskWithoutWrites(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	firstTask, firstRun := newRunningExecutionTaskRun(t, manager, "Mio")
	secondTask, secondRun := newRunningExecutionTaskRun(t, manager, "Shiro")
	beforeFirstTask, err := manager.Get(ctx, firstTask.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeSecondTask, err := manager.Get(ctx, secondTask.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	beforeNotifications, err := manager.Notifications(ctx, 10, false)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		taskID modulecore.TaskID
		runID  modulecore.RunID
		actor  string
	}{
		{name: "wrong_actor", taskID: firstTask.TaskID, runID: firstRun.RunID, actor: "Shiro"},
		{name: "wrong_task", taskID: secondTask.TaskID, runID: firstRun.RunID, actor: "Mio"},
		{name: "wrong_run", taskID: firstTask.TaskID, runID: secondRun.RunID, actor: "Mio"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := manager.CompleteRun(ctx, test.taskID, test.runID, test.actor, domaintask.StatusSucceeded, "must reject", ""); !errors.Is(err, ErrRunConflict) {
				t.Fatalf("CompleteRun error = %v, want ErrRunConflict", err)
			}
			afterFirstTask, err := manager.Get(ctx, firstTask.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			afterSecondTask, err := manager.Get(ctx, secondTask.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			afterRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{})
			if err != nil {
				t.Fatal(err)
			}
			afterNotifications, err := manager.Notifications(ctx, 10, false)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeFirstTask, afterFirstTask) || !reflect.DeepEqual(beforeSecondTask, afterSecondTask) || !reflect.DeepEqual(beforeRuns, afterRuns) || !reflect.DeepEqual(beforeNotifications, afterNotifications) {
				t.Fatalf("rejected completion changed records: before tasks=%#v/%#v runs=%#v notifications=%#v after tasks=%#v/%#v runs=%#v notifications=%#v", beforeFirstTask, beforeSecondTask, beforeRuns, beforeNotifications, afterFirstTask, afterSecondTask, afterRuns, afterNotifications)
			}
		})
	}
}

func TestManagerCompleteRunRejectsOldRunAfterCheckpointResume(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	task, oldRun := newRunningExecutionTaskRun(t, manager, "Mio")
	newRun, err := manager.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonCheckpointResume)
	if err != nil {
		t.Fatalf("checkpoint resume: %v", err)
	}
	beforeTask, err := manager.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CompleteRun(ctx, task.TaskID, oldRun.RunID, "Mio", domaintask.StatusSucceeded, "stale", ""); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("old Run completion error = %v, want ErrRunConflict", err)
	}
	afterTask, err := manager.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	afterRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeTask, afterTask) || !reflect.DeepEqual(beforeRuns, afterRuns) {
		t.Fatalf("stale completion changed records: before task=%#v runs=%#v after task=%#v runs=%#v", beforeTask, beforeRuns, afterTask, afterRuns)
	}
	oldPersisted, err := manager.GetRun(ctx, oldRun.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if oldPersisted.Status != domaintask.RunStatusInterrupted || newRun.RunID == oldPersisted.RunID {
		t.Fatalf("old/new Runs = %#v / %#v", oldPersisted, newRun)
	}
}

type staleCompletionRunStore struct {
	Store
	run domaintask.Run
}

func (s staleCompletionRunStore) Transaction(ctx context.Context, fn func(Store) error) error {
	return s.Store.Transaction(ctx, func(store Store) error {
		return fn(staleCompletionRunStore{Store: store, run: s.run})
	})
}

func (s staleCompletionRunStore) GetRun(context.Context, modulecore.RunID) (domaintask.Run, error) {
	return s.run, nil
}

func TestManagerCompleteRunRejectsStaleGenerationWithoutWrites(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	setup := New(store, DefaultParallelLimits())
	task, run := newRunningExecutionTaskRun(t, setup, "Mio")
	stale := run
	stale.WriterGeneration = 0
	manager := New(staleCompletionRunStore{Store: store, run: stale}, DefaultParallelLimits())
	beforeTask, err := setup.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRun, err := setup.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CompleteRun(ctx, task.TaskID, run.RunID, "Mio", domaintask.StatusSucceeded, "stale generation", ""); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("stale generation completion error = %v, want ErrRunConflict", err)
	}
	afterTask, err := setup.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	afterRun, err := setup.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeTask, afterTask) || !reflect.DeepEqual(beforeRun, afterRun) {
		t.Fatalf("stale generation completion changed records: before task=%#v run=%#v after task=%#v run=%#v", beforeTask, beforeRun, afterTask, afterRun)
	}
}

func TestManagerCompleteRunWaitingReleasesSlotAndSupportsCheckpointResume(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	limits := DefaultParallelLimits()
	limits.Global = 1
	manager := New(store, limits)
	task, run := newRunningExecutionTaskRun(t, manager, "Mio")
	queued, err := manager.Create(ctx, domaintask.Task{Title: "queued work", Assignee: "Shiro", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	allowed, reason, err := manager.CanStart(ctx, queued)
	if err != nil {
		t.Fatal(err)
	}
	if allowed || reason != "global running task limit reached" {
		t.Fatalf("CanStart while first Run active = allowed=%v reason=%q", allowed, reason)
	}
	if _, err := manager.CompleteRun(ctx, task.TaskID, run.RunID, "Mio", domaintask.StatusWaiting, "paused", "checkpoint retry after dependency recovery"); err != nil {
		t.Fatalf("waiting completion: %v", err)
	}
	allowed, reason, err = manager.CanStart(ctx, queued)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed || reason != "" {
		t.Fatalf("CanStart after waiting completion = allowed=%v reason=%q", allowed, reason)
	}
	resumed, err := manager.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonCheckpointResume)
	if err != nil {
		t.Fatalf("checkpoint resume after waiting: %v", err)
	}
	if resumed.StartReason != domaintask.RunStartReasonCheckpointResume || resumed.Status != domaintask.RunStatusRunning {
		t.Fatalf("resumed Run = %#v", resumed)
	}
	updated, err := manager.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domaintask.StatusRunning || updated.WaitingReason != "" {
		t.Fatalf("resumed Task = %#v", updated)
	}
}

func TestManagerCompleteRunRollsBackOnNotificationFailure(t *testing.T) {
	ctx := context.Background()
	base, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, base)
	setup := New(base, DefaultParallelLimits())
	task, run := newRunningExecutionTaskRun(t, setup, "Mio")
	beforeTask, err := setup.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRun, err := setup.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("completion notification append failed")
	manager := New(&faultTaskStore{Store: base, failOperation: "notification", failErr: injected}, DefaultParallelLimits())
	if _, err := manager.CompleteRun(ctx, task.TaskID, run.RunID, "Mio", domaintask.StatusSucceeded, "must rollback", ""); !errors.Is(err, injected) {
		t.Fatalf("CompleteRun error = %v, want injected error", err)
	}
	afterTask, err := setup.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	afterRun, err := setup.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	notifications, err := setup.Notifications(ctx, 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeTask, afterTask) || !reflect.DeepEqual(beforeRun, afterRun) || len(notifications) != 0 {
		t.Fatalf("failed completion changed records: before task=%#v run=%#v after task=%#v run=%#v notifications=%#v", beforeTask, beforeRun, afterTask, afterRun, notifications)
	}
}

func TestManagerCompleteRunRejectsInvalidInputsWithoutWrites(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	task, run := newRunningExecutionTaskRun(t, manager, "Mio")
	before, err := manager.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		taskID modulecore.TaskID
		runID  modulecore.RunID
		actor  string
		status domaintask.Status
		reason string
	}{
		{name: "blank_actor", taskID: task.TaskID, runID: run.RunID, actor: " ", status: domaintask.StatusSucceeded},
		{name: "invalid_status", taskID: task.TaskID, runID: run.RunID, actor: "Mio", status: domaintask.StatusBlocked},
		{name: "invalid_task", taskID: "", runID: run.RunID, actor: "Mio", status: domaintask.StatusSucceeded},
		{name: "invalid_run", taskID: task.TaskID, runID: "", actor: "Mio", status: domaintask.StatusSucceeded},
		{name: "waiting_reason_required", taskID: task.TaskID, runID: run.RunID, actor: "Mio", status: domaintask.StatusWaiting},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := manager.CompleteRun(ctx, test.taskID, test.runID, test.actor, test.status, "", test.reason); err == nil {
				t.Fatal("invalid completion was accepted")
			}
			after, err := manager.Get(ctx, task.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("invalid completion changed Task: before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestManagerVerifyRunCompletionAcceptsExactTerminalWithoutWrites(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	task, run := newRunningExecutionTaskRun(t, manager, "Mio")
	if _, err := manager.CompleteRun(ctx, task.TaskID, run.RunID, "Mio", domaintask.StatusSucceeded, "finished", ""); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}

	beforeTask, err := manager.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRun, err := manager.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	beforeNotifications, err := manager.Notifications(ctx, 10, false)
	if err != nil {
		t.Fatal(err)
	}

	if err := manager.VerifyRunCompletion(ctx, task.TaskID, run.RunID, "Mio", domaintask.StatusSucceeded); err != nil {
		t.Fatalf("VerifyRunCompletion: %v", err)
	}
	afterTask, err := manager.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	afterRun, err := manager.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	afterRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	afterNotifications, err := manager.Notifications(ctx, 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeTask, afterTask) || !reflect.DeepEqual(beforeRun, afterRun) || !reflect.DeepEqual(beforeRuns, afterRuns) || !reflect.DeepEqual(beforeNotifications, afterNotifications) {
		t.Fatalf("terminal verification changed records: before task=%#v run=%#v runs=%#v notifications=%#v after task=%#v run=%#v runs=%#v notifications=%#v", beforeTask, beforeRun, beforeRuns, beforeNotifications, afterTask, afterRun, afterRuns, afterNotifications)
	}
}

func TestManagerVerifyRunCompletionRejectsWrongActorAndStatusWithoutWrites(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	task, run := newRunningExecutionTaskRun(t, manager, "Mio")
	if _, err := manager.CompleteRun(ctx, task.TaskID, run.RunID, "Mio", domaintask.StatusSucceeded, "finished", ""); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	beforeTask, err := manager.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		actor  string
		status domaintask.Status
	}{
		{name: "wrong_actor", actor: "Shiro", status: domaintask.StatusSucceeded},
		{name: "wrong_status", actor: "Mio", status: domaintask.StatusFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := manager.VerifyRunCompletion(ctx, task.TaskID, run.RunID, test.actor, test.status); !errors.Is(err, ErrRunConflict) {
				t.Fatalf("VerifyRunCompletion error = %v, want ErrRunConflict", err)
			}
			afterTask, err := manager.Get(ctx, task.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			afterRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeTask, afterTask) || !reflect.DeepEqual(beforeRuns, afterRuns) {
				t.Fatalf("rejected verification changed records: before task=%#v runs=%#v after task=%#v runs=%#v", beforeTask, beforeRuns, afterTask, afterRuns)
			}
		})
	}
}

func TestManagerVerifyRunCompletionRejectsStaleAndNewerRunsWithoutWrites(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	task, oldRun := newRunningExecutionTaskRun(t, manager, "Mio")
	newRun, err := manager.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonCheckpointResume)
	if err != nil {
		t.Fatalf("checkpoint resume: %v", err)
	}
	beforeTask, err := manager.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		runID  modulecore.RunID
		status domaintask.Status
	}{
		{name: "stale_run", runID: oldRun.RunID, status: domaintask.StatusSucceeded},
		{name: "newer_active_run", runID: newRun.RunID, status: domaintask.StatusSucceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := manager.VerifyRunCompletion(ctx, task.TaskID, test.runID, "Mio", test.status); !errors.Is(err, ErrRunConflict) {
				t.Fatalf("VerifyRunCompletion error = %v, want ErrRunConflict", err)
			}
			afterTask, err := manager.Get(ctx, task.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			afterRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeTask, afterTask) || !reflect.DeepEqual(beforeRuns, afterRuns) {
				t.Fatalf("rejected verification changed records: before task=%#v runs=%#v after task=%#v runs=%#v", beforeTask, beforeRuns, afterTask, afterRuns)
			}
		})
	}
}

func TestManagerVerifyRunCompletionRejectsOlderTerminalRunAndAcceptsLatestWithoutWrites(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	clock := time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)
	manager.now = func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}
	task, oldRun := newRunningExecutionTaskRun(t, manager, "Mio")
	if _, err := manager.CompleteRun(ctx, task.TaskID, oldRun.RunID, "Mio", domaintask.StatusSucceeded, "first", ""); err != nil {
		t.Fatalf("old Run completion: %v", err)
	}
	newRun, err := manager.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonExplicitRerun)
	if err != nil {
		t.Fatalf("explicit rerun: %v", err)
	}
	if _, err := manager.CompleteRun(ctx, task.TaskID, newRun.RunID, "Mio", domaintask.StatusSucceeded, "second", ""); err != nil {
		t.Fatalf("new Run completion: %v", err)
	}

	beforeTask, err := manager.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	beforeNotifications, err := manager.Notifications(ctx, 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.VerifyRunCompletion(ctx, task.TaskID, oldRun.RunID, "Mio", domaintask.StatusSucceeded); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("older terminal Run verification error = %v, want ErrRunConflict", err)
	}
	assertRunCompletionRecordsUnchanged(t, manager, ctx, task.TaskID, beforeTask, beforeRuns, beforeNotifications)

	if err := manager.VerifyRunCompletion(ctx, task.TaskID, newRun.RunID, "Mio", domaintask.StatusSucceeded); err != nil {
		t.Fatalf("latest terminal Run verification: %v", err)
	}
	assertRunCompletionRecordsUnchanged(t, manager, ctx, task.TaskID, beforeTask, beforeRuns, beforeNotifications)
}

func assertRunCompletionRecordsUnchanged(t *testing.T, manager *Manager, ctx context.Context, taskID modulecore.TaskID, beforeTask domaintask.Task, beforeRuns []domaintask.Run, beforeNotifications []domaintask.Notification) {
	t.Helper()
	afterTask, err := manager.Get(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	afterRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	afterNotifications, err := manager.Notifications(ctx, 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeTask, afterTask) || !reflect.DeepEqual(beforeRuns, afterRuns) || !reflect.DeepEqual(beforeNotifications, afterNotifications) {
		t.Fatalf("VerifyRunCompletion changed records: before task=%#v runs=%#v notifications=%#v after task=%#v runs=%#v notifications=%#v", beforeTask, beforeRuns, beforeNotifications, afterTask, afterRuns, afterNotifications)
	}
}
