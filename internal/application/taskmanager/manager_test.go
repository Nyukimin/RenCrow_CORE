package taskmanager

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func registerTaskStoreCleanup(t *testing.T, store *taskpersistence.JSONLStore) {
	t.Helper()
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close task store: %v", err)
		}
	})
}

func TestManagerLifecycleAndNotification(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	manager.now = func() time.Time { return time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC) }
	value, err := manager.Create(context.Background(), domaintask.Task{Title: "write spec", Route: domaintask.RouteCode}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), value.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Wait(context.Background(), value.TaskID, "external review"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resume(context.Background(), value.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), value.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Succeed(context.Background(), value.TaskID, "done"); err != nil {
		t.Fatal(err)
	}
	items, err := manager.Notifications(context.Background(), 10, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].TaskID != value.TaskID || items[0].Type != "task.notification" {
		t.Fatalf("notifications = %#v", items)
	}
}

func TestManagerWaitAndDependencyAndParallelLimit(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, ParallelLimits{Global: 1, PerModule: 1, CodingTasks: 1, LongResearchTasks: 1, DestructiveTasks: 1})
	dependency, err := manager.Create(context.Background(), domaintask.Task{Title: "dependency", Route: domaintask.RouteCode}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	dependent, err := manager.Create(context.Background(), domaintask.Task{Title: "dependent", Route: domaintask.RouteCode, DependencyTaskIDs: []modulecore.TaskID{dependency.TaskID}}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), dependent.TaskID); err == nil {
		t.Fatal("dependent task started before dependency")
	}
	if _, err := manager.Start(context.Background(), dependency.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Wait(context.Background(), dependency.TaskID, "dependency wait"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Wait(context.Background(), dependency.TaskID, ""); err == nil {
		t.Fatal("empty wait reason accepted")
	}
	if _, err := manager.Resume(context.Background(), dependency.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), dependency.TaskID); err != nil {
		t.Fatal(err)
	}
	parallel, err := manager.Create(context.Background(), domaintask.Task{Title: "parallel", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), parallel.TaskID); err == nil {
		t.Fatal("global parallel limit was ignored")
	}
	if _, err := manager.Succeed(context.Background(), dependency.TaskID, "dependency done"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), dependent.TaskID); err != nil {
		t.Fatalf("dependent task did not start after dependency succeeded: %v", err)
	}
}

func TestManagerChildDoesNotDoubleCountItsRunningRootParallelCapacity(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, ParallelLimits{Global: 1, PerModule: 1, CodingTasks: 1, LongResearchTasks: 1, DestructiveTasks: 1})
	root, err := manager.Create(context.Background(), domaintask.Task{
		Title: "OPS orchestration", Route: domaintask.RouteOperations,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), root.TaskID); err != nil {
		t.Fatal(err)
	}
	child, err := manager.Create(context.Background(), domaintask.Task{
		Title: "OPS execution", Route: domaintask.RouteOperations, ParentTaskID: root.TaskID, Assignee: "shiro",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), child.TaskID); err != nil {
		t.Fatalf("child was blocked by its own running root: %v", err)
	}
	unrelated, err := manager.Create(context.Background(), domaintask.Task{
		Title: "unrelated OPS", Route: domaintask.RouteOperations, Assignee: "shiro",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), unrelated.TaskID); !errors.Is(err, ErrParallelLimit) {
		t.Fatalf("unrelated operation start error=%v want ErrParallelLimit", err)
	}
}

func TestManagerBlockResumeFailCancelAndParent(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	parent, err := manager.Create(context.Background(), domaintask.Task{Title: "parent", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := manager.Create(context.Background(), domaintask.Task{Title: "child", Route: domaintask.RouteGeneral, ParentTaskID: parent.TaskID}, domaintask.SharedRoleContext{})
	if err != nil || child.ParentTaskID != parent.TaskID {
		t.Fatalf("child=%#v err=%v", child, err)
	}
	if _, err := manager.Start(context.Background(), child.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Block(context.Background(), child.TaskID, "external system unavailable"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resume(context.Background(), child.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), child.TaskID); err != nil {
		t.Fatal(err)
	}
	failed, err := manager.Fail(context.Background(), child.TaskID, "bounded attempts exhausted", []string{"inspect receipt"})
	if err != nil || failed.Status != domaintask.StatusFailed || len(failed.NextActions) != 1 {
		t.Fatalf("failed=%#v err=%v", failed, err)
	}
	cancelled, err := manager.Create(context.Background(), domaintask.Task{Title: "cancel", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Queue(context.Background(), cancelled.TaskID); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Cancel(context.Background(), cancelled.TaskID, "no longer needed")
	if err != nil || result.Status != domaintask.StatusCancelled {
		t.Fatalf("cancelled=%#v err=%v", result, err)
	}
	missingParent := modulecore.NewTaskID()
	if _, err := manager.Create(context.Background(), domaintask.Task{Title: "orphan", Route: domaintask.RouteGeneral, ParentTaskID: missingParent}, domaintask.SharedRoleContext{}); err == nil {
		t.Fatal("missing parent Task was accepted")
	}
}

func TestManagerSupersedePersistsReplacementRelationship(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	old, err := manager.Create(context.Background(), domaintask.Task{Title: "old", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := manager.Create(context.Background(), domaintask.Task{Title: "replacement", Route: domaintask.RouteGeneral, SupersedesTaskID: old.TaskID}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Supersede(context.Background(), old.TaskID, replacement.TaskID); err != nil {
		t.Fatal(err)
	}
	oldAfter, err := manager.Get(context.Background(), old.TaskID)
	if err != nil || oldAfter.Status != domaintask.StatusSuperseded {
		t.Fatalf("old task = %#v err=%v", oldAfter, err)
	}
	replacementAfter, err := manager.Get(context.Background(), replacement.TaskID)
	if err != nil || replacementAfter.SupersedesTaskID != old.TaskID {
		t.Fatalf("replacement task = %#v err=%v", replacementAfter, err)
	}
	unlinked, err := manager.Create(context.Background(), domaintask.Task{Title: "unlinked", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Supersede(context.Background(), replacement.TaskID, unlinked.TaskID); err == nil {
		t.Fatal("replacement without supersedes_task_id was accepted")
	}
}

func TestManagerRejectsMismatchedContextIdentityBeforeSaving(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	taskID := modulecore.NewTaskID()
	otherID := modulecore.NewTaskID()
	if _, err := manager.Create(context.Background(), domaintask.Task{TaskID: taskID, Title: "task", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{TaskID: otherID}); err == nil {
		t.Fatal("mismatched context task_id was accepted")
	}
	if _, err := manager.Get(context.Background(), taskID); err == nil {
		t.Fatal("Task was saved before context identity rejection")
	}
}

func TestManagerRecordsRoutingAndAssignmentWithoutStatusTransition(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	manager.now = func() time.Time { return time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC) }
	value, err := manager.Create(context.Background(), domaintask.Task{Title: "route task", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	routingEventID := modulecore.NewEventID()
	routed, err := manager.RecordRouting(context.Background(), value.TaskID, domaintask.RouteANALYZE, routingEventID)
	if err != nil {
		t.Fatalf("RecordRouting: %v", err)
	}
	if routed.Route != domaintask.RouteANALYZE || routed.RoutingEventID != routingEventID || routed.Status != domaintask.StatusQueued {
		t.Fatalf("routed task = %#v", routed)
	}
	assignmentEventID := modulecore.NewEventID()
	assigned, err := manager.RecordAssignment(context.Background(), value.TaskID, " Mio ", assignmentEventID)
	if err != nil {
		t.Fatalf("RecordAssignment: %v", err)
	}
	if assigned.Assignee != "Mio" || assigned.AssignmentEventID != assignmentEventID || assigned.Status != domaintask.StatusQueued {
		t.Fatalf("assigned task = %#v", assigned)
	}
	persisted, err := manager.Get(context.Background(), value.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Route != domaintask.RouteANALYZE || persisted.RoutingEventID != routingEventID || persisted.Assignee != "Mio" || persisted.AssignmentEventID != assignmentEventID || persisted.Status != domaintask.StatusQueued {
		t.Fatalf("persisted task = %#v", persisted)
	}
}

func TestManagerRejectsInvalidReferencesAndBlankAssignmentBeforeSaving(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	manager.now = func() time.Time { return time.Date(2026, 9, 5, 3, 0, 0, 0, time.UTC) }
	value, err := manager.Create(context.Background(), domaintask.Task{Title: "validation task", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := manager.Get(context.Background(), value.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RecordRouting(context.Background(), value.TaskID, domaintask.RouteCHAT, modulecore.EventID("invalid-event-id")); err == nil {
		t.Fatal("invalid routing event ID was accepted")
	}
	if _, err := manager.RecordAssignment(context.Background(), value.TaskID, "   ", modulecore.NewEventID()); err == nil {
		t.Fatal("blank assignee was accepted")
	}
	if _, err := manager.RecordAssignment(context.Background(), value.TaskID, "Mio", modulecore.EventID("invalid-event-id")); err == nil {
		t.Fatal("invalid assignment event ID was accepted")
	}
	after, err := manager.Get(context.Background(), value.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Route != before.Route || after.RoutingEventID != before.RoutingEventID || after.Assignee != before.Assignee || after.AssignmentEventID != before.AssignmentEventID || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("invalid records changed task: before=%#v after=%#v", before, after)
	}
}

func TestManagerRunLifecycleCreatesDistinctRunsAndClosesCurrentRun(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	now := time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	task, err := manager.Create(ctx, domaintask.Task{Title: "run lifecycle", Route: domaintask.RouteGeneral, Assignee: "Mio"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(ctx, task.TaskID); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	runs, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil || len(runs) != 1 || runs[0].StartReason != domaintask.RunStartReasonFirst || runs[0].Status != domaintask.RunStatusRunning {
		t.Fatalf("first runs = %#v err=%v", runs, err)
	}
	firstRunID := runs[0].RunID
	if _, err := manager.Wait(ctx, task.TaskID, "checkpoint"); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if _, err := manager.Resume(ctx, task.TaskID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if _, err := manager.StartWithReason(ctx, task.TaskID, domaintask.RunStartReasonCheckpointResume); err != nil {
		t.Fatalf("checkpoint StartWithReason: %v", err)
	}
	runs, err = manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs after resume = %#v err=%v", runs, err)
	}
	var first, resumed *domaintask.Run
	for index := range runs {
		item := runs[index]
		switch item.StartReason {
		case domaintask.RunStartReasonFirst:
			first = &item
		case domaintask.RunStartReasonCheckpointResume:
			resumed = &item
		}
	}
	if first == nil || resumed == nil || first.RunID == resumed.RunID || first.RunID != firstRunID || first.Status != domaintask.RunStatusWaiting || resumed.Status != domaintask.RunStatusRunning {
		t.Fatalf("run history = %#v", runs)
	}
	if _, err := manager.Succeed(ctx, task.TaskID, "done"); err != nil {
		t.Fatalf("Succeed: %v", err)
	}
	before, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	var terminal *domaintask.Run
	for index := range before {
		if before[index].Status == domaintask.RunStatusSucceeded {
			item := before[index]
			terminal = &item
		}
	}
	if err != nil || len(before) != 2 || terminal == nil || terminal.CompletedAt == nil {
		t.Fatalf("terminal runs = %#v err=%v", before, err)
	}
	completedAt := *terminal.CompletedAt
	if _, err := manager.Succeed(ctx, task.TaskID, "same terminal state"); err != nil {
		t.Fatalf("second Succeed: %v", err)
	}
	after, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	var terminalAfter *domaintask.Run
	for index := range after {
		if after[index].Status == domaintask.RunStatusSucceeded {
			item := after[index]
			terminalAfter = &item
		}
	}
	if err != nil || len(after) != len(before) || terminalAfter == nil || terminalAfter.CompletedAt == nil || !terminalAfter.CompletedAt.Equal(completedAt) {
		t.Fatalf("terminal run was rewritten: before=%#v after=%#v err=%v", before, after, err)
	}
}

func TestManagerStartRunWithReasonReturnsPersistedRunForCheckpointAndLeaseResume(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	manager.now = func() time.Time { return time.Date(2026, 9, 5, 4, 30, 0, 0, time.UTC) }
	task, err := manager.Create(ctx, domaintask.Task{Title: "run handoff", Route: domaintask.RouteGeneral, Assignee: "Mio"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}

	first, err := manager.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatalf("first StartRunWithReason: %v", err)
	}
	if err := first.RunID.Validate(); err != nil {
		t.Fatalf("first run id is not canonical: %v", err)
	}
	if first.TaskID != task.TaskID || first.StartReason != domaintask.RunStartReasonFirst || first.Assignee != "Mio" || first.Status != domaintask.RunStatusRunning {
		t.Fatalf("first run = %#v", first)
	}
	persisted, err := manager.GetRun(ctx, first.RunID)
	if err != nil {
		t.Fatalf("get returned first run: %v", err)
	}
	if persisted.RunID != first.RunID || persisted.TaskID != first.TaskID || persisted.StartReason != first.StartReason || persisted.Status != first.Status || persisted.Assignee != first.Assignee || !persisted.StartedAt.Equal(first.StartedAt) {
		t.Fatalf("returned run does not match persisted run: returned=%#v persisted=%#v", first, persisted)
	}

	if _, err := manager.Wait(ctx, task.TaskID, "checkpoint"); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if _, err := manager.Resume(ctx, task.TaskID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	checkpoint, err := manager.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonCheckpointResume)
	if err != nil {
		t.Fatalf("checkpoint StartRunWithReason: %v", err)
	}
	if checkpoint.RunID == first.RunID || checkpoint.TaskID != task.TaskID || checkpoint.StartReason != domaintask.RunStartReasonCheckpointResume || checkpoint.Status != domaintask.RunStatusRunning {
		t.Fatalf("checkpoint run = %#v", checkpoint)
	}

	lease, err := manager.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonLeaseReacquire)
	if err != nil {
		t.Fatalf("lease StartRunWithReason: %v", err)
	}
	if lease.RunID == first.RunID || lease.RunID == checkpoint.RunID || lease.TaskID != task.TaskID || lease.StartReason != domaintask.RunStartReasonLeaseReacquire || lease.Status != domaintask.RunStatusRunning {
		t.Fatalf("lease run = %#v", lease)
	}

	runs, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil {
		t.Fatalf("list run history: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("run history length = %d, want 3: %#v", len(runs), runs)
	}
	for _, run := range runs {
		switch run.RunID {
		case first.RunID:
			if run.Status != domaintask.RunStatusWaiting || run.CompletedAt == nil {
				t.Fatalf("first run was not closed once as waiting: %#v", run)
			}
		case checkpoint.RunID:
			if run.Status != domaintask.RunStatusInterrupted || run.CompletedAt == nil {
				t.Fatalf("checkpoint run was not closed once as interrupted: %#v", run)
			}
		case lease.RunID:
			if run.Status != domaintask.RunStatusRunning || run.CompletedAt != nil {
				t.Fatalf("lease run is not the sole active run: %#v", run)
			}
		default:
			t.Fatalf("unexpected run in history: %#v", run)
		}
	}
}

func TestManagerInterruptRunClosesExactRunWithoutChangingTask(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	now := time.Date(2026, 9, 5, 4, 45, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	task, err := manager.Create(ctx, domaintask.Task{Title: "interrupt exact run", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := manager.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	taskBefore, err := manager.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}

	interrupted, err := manager.InterruptRun(ctx, task.TaskID, run.RunID, " queue lease lost ")
	if err != nil {
		t.Fatalf("InterruptRun: %v", err)
	}
	if interrupted.RunID != run.RunID || interrupted.TaskID != task.TaskID || interrupted.Status != domaintask.RunStatusInterrupted || interrupted.Summary != "queue lease lost" || interrupted.CompletedAt == nil || !interrupted.CompletedAt.Equal(now) {
		t.Fatalf("interrupted run = %#v", interrupted)
	}
	taskAfter, err := manager.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(taskAfter, taskBefore) {
		t.Fatalf("InterruptRun changed Task: before=%#v after=%#v", taskBefore, taskAfter)
	}
}

func TestManagerInterruptRunRejectsCrossTaskAndPreservesHistory(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	manager.now = func() time.Time { return time.Date(2026, 9, 5, 5, 0, 0, 0, time.UTC) }
	first, err := manager.Create(ctx, domaintask.Task{Title: "first task", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Create(ctx, domaintask.Task{Title: "second task", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StartRunWithReason(ctx, first.TaskID, domaintask.RunStartReasonFirst); err != nil {
		t.Fatal(err)
	}
	secondRun, err := manager.StartRunWithReason(ctx, second.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.InterruptRun(ctx, first.TaskID, secondRun.RunID, "must not cross task boundary"); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("cross-task InterruptRun error = %v, want ErrRunConflict", err)
	}
	persisted, err := manager.GetRun(ctx, secondRun.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != domaintask.RunStatusRunning || persisted.Summary != "" {
		t.Fatalf("cross-task rejection changed Run: %#v", persisted)
	}
}

func TestManagerInterruptRunIsIdempotentOnlyForInterruptedRuns(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	now := time.Date(2026, 9, 5, 5, 15, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	task, err := manager.Create(ctx, domaintask.Task{Title: "idempotent interrupt", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := manager.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.InterruptRun(ctx, task.TaskID, run.RunID, "first cleanup")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.InterruptRun(ctx, task.TaskID, run.RunID, "different cleanup")
	if err != nil {
		t.Fatalf("idempotent InterruptRun: %v", err)
	}
	if !reflect.DeepEqual(second, first) || second.Summary != "first cleanup" {
		t.Fatalf("idempotent InterruptRun rewrote history: first=%#v second=%#v", first, second)
	}

	terminalTask, err := manager.Create(ctx, domaintask.Task{Title: "terminal conflict", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	terminalRun, err := manager.StartRunWithReason(ctx, terminalTask.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Succeed(ctx, terminalTask.TaskID, "already succeeded"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.InterruptRun(ctx, terminalTask.TaskID, terminalRun.RunID, "must not rewrite success"); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("terminal InterruptRun error = %v, want ErrRunConflict", err)
	}
	persisted, err := manager.GetRun(ctx, terminalRun.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != domaintask.RunStatusSucceeded || persisted.Summary != "already succeeded" {
		t.Fatalf("terminal Run history changed: %#v", persisted)
	}
}

func TestManagerAssignmentReassignmentClosesAndReopensRun(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	manager.now = func() time.Time { return time.Date(2026, 9, 5, 5, 0, 0, 0, time.UTC) }
	task, err := manager.Create(ctx, domaintask.Task{Title: "reassign", Route: domaintask.RouteGeneral, Assignee: "Mio"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(ctx, task.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RecordAssignment(ctx, task.TaskID, "Shiro", modulecore.NewEventID()); err != nil {
		t.Fatalf("RecordAssignment: %v", err)
	}
	runs, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %#v err=%v", runs, err)
	}
	var reassigned, reopened *domaintask.Run
	for index := range runs {
		item := runs[index]
		if item.Status == domaintask.RunStatusReassigned {
			reassigned = &item
		}
		if item.Status == domaintask.RunStatusRunning {
			reopened = &item
		}
	}
	if reassigned == nil || reassigned.CompletedAt == nil || reopened == nil || reopened.StartReason != domaintask.RunStartReasonAgentReassignment || reopened.Assignee != "Shiro" {
		t.Fatalf("reassignment runs = %#v", runs)
	}
}

func TestManagerStartWithReasonSupportsAllCanonicalReasons(t *testing.T) {
	reasons := []domaintask.RunStartReason{
		domaintask.RunStartReasonFirst,
		domaintask.RunStartReasonProcessRestartResume,
		domaintask.RunStartReasonLeaseReacquire,
		domaintask.RunStartReasonAgentReassignment,
		domaintask.RunStartReasonCheckpointResume,
		domaintask.RunStartReasonExplicitRerun,
	}
	for _, reason := range reasons {
		t.Logf("reason=%s", reason)
		store, err := taskpersistence.NewJSONLStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		registerTaskStoreCleanup(t, store)
		manager := New(store, DefaultParallelLimits())
		manager.now = func() time.Time { return time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC) }
		task, err := manager.Create(context.Background(), domaintask.Task{Title: "reason", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
		if err != nil {
			t.Fatal(err)
		}
		if reason == domaintask.RunStartReasonFirst {
			if _, err := manager.StartWithReason(context.Background(), task.TaskID, reason); err != nil {
				t.Fatalf("StartWithReason(first): %v", err)
			}
		} else {
			if _, err := manager.Start(context.Background(), task.TaskID); err != nil {
				t.Fatalf("initial Start: %v", err)
			}
			if _, err := manager.Wait(context.Background(), task.TaskID, "prepare explicit reason"); err != nil {
				t.Fatalf("Wait: %v", err)
			}
			if _, err := manager.Resume(context.Background(), task.TaskID); err != nil {
				t.Fatalf("Resume: %v", err)
			}
			if _, err := manager.StartWithReason(context.Background(), task.TaskID, reason); err != nil {
				t.Fatalf("StartWithReason(%s): %v", reason, err)
			}
		}
		runs, err := manager.ListRuns(context.Background(), domaintask.RunFilter{TaskID: task.TaskID, Status: domaintask.RunStatusRunning})
		if err != nil || len(runs) != 1 || runs[0].StartReason != reason {
			t.Fatalf("running runs for %s = %#v err=%v", reason, runs, err)
		}
	}
}

func TestManagerExplicitRerunReopensTerminalTaskWithFreshRun(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	manager.now = func() time.Time { return time.Date(2026, 9, 5, 9, 0, 0, 0, time.UTC) }
	task, err := manager.Create(ctx, domaintask.Task{Title: "rerun terminal", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(ctx, task.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Succeed(ctx, task.TaskID, "first attempt"); err != nil {
		t.Fatal(err)
	}
	terminal, err := manager.Get(ctx, task.TaskID)
	if err != nil || terminal.Status != domaintask.StatusSucceeded || terminal.FinishedAt == nil {
		t.Fatalf("terminal task = %#v err=%v", terminal, err)
	}
	firstRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil || len(firstRuns) != 1 {
		t.Fatalf("first runs = %#v err=%v", firstRuns, err)
	}
	firstRunID := firstRuns[0].RunID

	reopened, err := manager.StartWithReason(ctx, task.TaskID, domaintask.RunStartReasonExplicitRerun)
	if err != nil {
		t.Fatalf("explicit rerun: %v", err)
	}
	if reopened.Status != domaintask.StatusRunning || reopened.FinishedAt != nil || reopened.WaitingReason != "" {
		t.Fatalf("reopened task = %#v", reopened)
	}
	runs, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil || len(runs) != 2 {
		t.Fatalf("rerun history = %#v err=%v", runs, err)
	}
	var fresh *domaintask.Run
	for index := range runs {
		if runs[index].RunID != firstRunID {
			item := runs[index]
			fresh = &item
		}
	}
	if fresh == nil || fresh.Status != domaintask.RunStatusRunning || fresh.StartReason != domaintask.RunStartReasonExplicitRerun {
		t.Fatalf("fresh rerun = %#v", fresh)
	}
}

func TestManagerNonExplicitReasonsCannotReopenTerminalTask(t *testing.T) {
	reasons := []domaintask.RunStartReason{
		domaintask.RunStartReasonProcessRestartResume,
		domaintask.RunStartReasonLeaseReacquire,
		domaintask.RunStartReasonAgentReassignment,
		domaintask.RunStartReasonCheckpointResume,
	}
	for _, reason := range reasons {
		store, err := taskpersistence.NewJSONLStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		registerTaskStoreCleanup(t, store)
		manager := New(store, DefaultParallelLimits())
		manager.now = func() time.Time { return time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC) }
		task, err := manager.Create(context.Background(), domaintask.Task{Title: "terminal reason", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Start(context.Background(), task.TaskID); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Succeed(context.Background(), task.TaskID, "closed"); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.StartWithReason(context.Background(), task.TaskID, reason); err == nil {
			t.Fatalf("terminal task reopened for reason %s", reason)
		}
		persisted, err := manager.Get(context.Background(), task.TaskID)
		if err != nil || persisted.Status != domaintask.StatusSucceeded || persisted.FinishedAt == nil {
			t.Fatalf("terminal task changed for reason %s: %#v err=%v", reason, persisted, err)
		}
	}
}

func TestManagerRunPersistsWriterGeneration(t *testing.T) {
	root := t.TempDir()
	var previous float64
	for i := 0; i < 2; i++ {
		store, err := taskpersistence.NewJSONLStore(root)
		if err != nil {
			t.Fatal(err)
		}
		registerTaskStoreCleanup(t, store)
		manager := New(store, DefaultParallelLimits())
		task, err := manager.Create(context.Background(), domaintask.Task{Title: "generation test", Assignee: "shiro", Route: domaintask.RouteOperations}, domaintask.SharedRoleContext{})
		if err != nil {
			t.Fatal(err)
		}
		run, err := manager.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		generation, _ := wire["writer_generation"].(float64)
		if generation <= previous {
			t.Fatalf("Run lacks advanced owner generation: previous=%v wire=%v", previous, wire)
		}
		saved, err := manager.GetRun(context.Background(), run.RunID)
		if err != nil || !reflect.DeepEqual(saved, run) {
			t.Fatalf("returned/persisted Run mismatch: %+v %+v %v", run, saved, err)
		}
		previous = generation
		if _, err := manager.Succeed(context.Background(), task.TaskID, "done"); err != nil {
			t.Fatal(err)
		}
		if err := manager.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func newRunningExecutionTaskRun(t *testing.T, manager *Manager, assignee string) (domaintask.Task, domaintask.Run) {
	t.Helper()
	task, err := manager.Create(context.Background(), domaintask.Task{
		Title: "execution admission", Route: domaintask.RouteGeneral, Assignee: assignee,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := manager.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	return task, run
}

func TestManagerValidateRunExecutionRequiresCanonicalOwner(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	firstTask, firstRun := newRunningExecutionTaskRun(t, manager, "Mio")
	secondTask, secondRun := newRunningExecutionTaskRun(t, manager, "Shiro")

	if err := manager.ValidateRunExecution(ctx, firstTask.TaskID, firstRun.RunID, "Mio"); err != nil {
		t.Fatalf("valid execution admission rejected: %v", err)
	}
	for _, test := range []struct {
		name       string
		taskID     modulecore.TaskID
		runID      modulecore.RunID
		actorID    string
		wantReason string
	}{
		{name: "wrong_actor", taskID: firstTask.TaskID, runID: firstRun.RunID, actorID: "Shiro", wantReason: "actor"},
		{name: "wrong_task", taskID: secondTask.TaskID, runID: firstRun.RunID, actorID: "Mio", wantReason: "task"},
		{name: "wrong_run", taskID: firstTask.TaskID, runID: secondRun.RunID, actorID: "Mio", wantReason: "run"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := manager.ValidateRunExecution(ctx, test.taskID, test.runID, test.actorID)
			if !errors.Is(err, ErrRunConflict) {
				t.Fatalf("%s error = %v, want ErrRunConflict", test.wantReason, err)
			}
		})
	}
}

func TestManagerValidateRunExecutionRejectsInvalidAdmissionInputs(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	task, run := newRunningExecutionTaskRun(t, manager, "Mio")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name string
		call func() error
	}{
		{name: "nil_context", call: func() error {
			return manager.ValidateRunExecution(nil, task.TaskID, run.RunID, "Mio")
		}},
		{name: "cancelled_context", call: func() error {
			return manager.ValidateRunExecution(cancelled, task.TaskID, run.RunID, "Mio")
		}},
		{name: "invalid_task_id", call: func() error {
			return manager.ValidateRunExecution(context.Background(), modulecore.TaskID(""), run.RunID, "Mio")
		}},
		{name: "invalid_run_id", call: func() error {
			return manager.ValidateRunExecution(context.Background(), task.TaskID, modulecore.RunID(""), "Mio")
		}},
		{name: "blank_actor", call: func() error {
			return manager.ValidateRunExecution(context.Background(), task.TaskID, run.RunID, " ")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); err == nil {
				t.Fatal("invalid admission input was accepted")
			}
		})
	}

	if err := (*Manager)(nil).ValidateRunExecution(context.Background(), task.TaskID, run.RunID, "Mio"); err == nil {
		t.Fatal("nil manager was accepted")
	}
	if err := (&Manager{}).ValidateRunExecution(context.Background(), task.TaskID, run.RunID, "Mio"); err == nil {
		t.Fatal("nil store was accepted")
	}
}

func TestManagerValidateRunExecutionRejectsTerminalTaskAndRun(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	task, run := newRunningExecutionTaskRun(t, manager, "Mio")
	if _, err := manager.Succeed(context.Background(), task.TaskID, "completed"); err != nil {
		t.Fatal(err)
	}
	if err := manager.ValidateRunExecution(context.Background(), task.TaskID, run.RunID, "Mio"); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("terminal admission error = %v, want ErrRunConflict", err)
	}
}

func TestManagerValidateRunExecutionFencesClosedAndRestartedOwners(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	firstStore, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	firstManager := New(firstStore, DefaultParallelLimits())
	task, oldRun := newRunningExecutionTaskRun(t, firstManager, "Mio")
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := firstManager.ValidateRunExecution(ctx, task.TaskID, oldRun.RunID, "Mio"); err == nil {
		t.Fatal("closed writer admitted execution")
	}

	secondStore, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	secondManager := New(secondStore, DefaultParallelLimits())
	if err := secondManager.ValidateRunExecution(ctx, task.TaskID, oldRun.RunID, "Mio"); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("stale running Run error = %v, want ErrRunConflict", err)
	}

	newRun, err := secondManager.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonProcessRestartResume)
	if err != nil {
		t.Fatalf("restart Run: %v", err)
	}
	if err := secondManager.ValidateRunExecution(ctx, task.TaskID, newRun.RunID, "Mio"); err != nil {
		t.Fatalf("current restart Run rejected: %v", err)
	}
	if err := secondManager.ValidateRunExecution(ctx, task.TaskID, oldRun.RunID, "Mio"); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("closed old Run error = %v, want ErrRunConflict", err)
	}
}

// admissionRunStore injects read-boundary faults without mutating historical records.
type admissionRunStore struct {
	Store
	readRun func(context.Context, modulecore.RunID) (domaintask.Run, error)
}

func (s admissionRunStore) GetRun(ctx context.Context, id modulecore.RunID) (domaintask.Run, error) {
	return s.readRun(ctx, id)
}

// ReadTransaction keeps the injected GetRun boundary visible to the manager
// while this test double is used as the transaction owner.
func (s admissionRunStore) ReadTransaction(ctx context.Context, fn func(Store) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(s)
}

func TestManagerValidateRunExecutionRejectsReadBoundaryFaults(t *testing.T) {
	for _, name := range []string{"historical_zero", "wrong_returned_run", "read_failure", "writer_closed_during_read", "cancelled_during_read"} {
		t.Run(name, func(t *testing.T) {
			store, err := taskpersistence.NewJSONLStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			registerTaskStoreCleanup(t, store)
			manager := New(store, DefaultParallelLimits())
			task, run := newRunningExecutionTaskRun(t, manager, "Mio")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			readFailure := errors.New("read unavailable")
			manager.store = admissionRunStore{Store: store, readRun: func(context.Context, modulecore.RunID) (domaintask.Run, error) {
				switch name {
				case "historical_zero":
					run.WriterGeneration = 0
				case "wrong_returned_run":
					run.RunID = modulecore.NewRunID()
				case "read_failure":
					return domaintask.Run{}, readFailure
				case "writer_closed_during_read":
					if err := store.Close(); err != nil {
						t.Fatal(err)
					}
				case "cancelled_during_read":
					cancel()
				}
				return run, nil
			}}
			err = manager.ValidateRunExecution(ctx, task.TaskID, run.RunID, "Mio")
			if err == nil {
				t.Fatal("read-boundary fault admitted execution")
			}
			if name == "read_failure" && !errors.Is(err, readFailure) {
				t.Fatalf("read error lost: %v", err)
			}
			if name == "cancelled_during_read" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel error lost: %v", err)
			}
			if (name == "historical_zero" || name == "wrong_returned_run") && !errors.Is(err, ErrRunConflict) {
				t.Fatalf("conflict error lost: %v", err)
			}
		})
	}
}

// faultTaskStore wraps each transaction view so Save failures remain visible
// to Manager's transaction body. Embedding alone would promote Transaction
// and hand the callback the underlying view, bypassing the injected boundary.
type faultTaskStore struct {
	Store
	failOperation string
	failErr       error
}

func (s *faultTaskStore) transactionView(store Store) Store {
	return &faultTaskStore{Store: store, failOperation: s.failOperation, failErr: s.failErr}
}

func (s *faultTaskStore) Transaction(ctx context.Context, fn func(Store) error) error {
	return s.Store.Transaction(ctx, func(store Store) error {
		return fn(s.transactionView(store))
	})
}

func (s *faultTaskStore) ReadTransaction(ctx context.Context, fn func(Store) error) error {
	return s.Store.ReadTransaction(ctx, func(store Store) error {
		return fn(s.transactionView(store))
	})
}

func (s *faultTaskStore) injected(operation string) error {
	if s.failOperation == operation {
		return s.failErr
	}
	return nil
}

func (s *faultTaskStore) SaveTask(ctx context.Context, value domaintask.Task) error {
	if err := s.injected("task"); err != nil {
		return err
	}
	return s.Store.SaveTask(ctx, value)
}

func (s *faultTaskStore) SaveRun(ctx context.Context, value domaintask.Run) error {
	if err := s.injected("run"); err != nil {
		return err
	}
	return s.Store.SaveRun(ctx, value)
}

func (s *faultTaskStore) SaveContext(ctx context.Context, value domaintask.SharedRoleContext) error {
	if err := s.injected("context"); err != nil {
		return err
	}
	return s.Store.SaveContext(ctx, value)
}

func (s *faultTaskStore) SaveNotification(ctx context.Context, value domaintask.Notification) error {
	if err := s.injected("notification"); err != nil {
		return err
	}
	return s.Store.SaveNotification(ctx, value)
}

func TestManagerCreateRollsBackTaskWhenContextSaveFails(t *testing.T) {
	base, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, base)
	injected := errors.New("context append failed")
	manager := New(&faultTaskStore{Store: base, failOperation: "context", failErr: injected}, DefaultParallelLimits())
	draft := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "atomic create", Route: domaintask.RouteGeneral}
	_, err = manager.Create(context.Background(), draft, domaintask.SharedRoleContext{})
	if !errors.Is(err, injected) {
		t.Fatalf("Create error = %v, want injected error", err)
	}
	if _, err := base.GetTask(context.Background(), draft.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("Task remained after failed Create: %v", err)
	}
	if _, err := base.GetContext(context.Background(), draft.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("Context remained after failed Create: %v", err)
	}
}

func TestManagerStartRollsBackTaskWhenRunSaveFails(t *testing.T) {
	base, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, base)
	setup := New(base, DefaultParallelLimits())
	task, err := setup.Create(context.Background(), domaintask.Task{Title: "atomic start", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	beforeTask, err := base.GetTask(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRuns, err := base.ListRuns(context.Background(), domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("run append failed")
	manager := New(&faultTaskStore{Store: base, failOperation: "run", failErr: injected}, DefaultParallelLimits())
	if _, err := manager.Start(context.Background(), task.TaskID); !errors.Is(err, injected) {
		t.Fatalf("Start error = %v, want injected error", err)
	}
	afterTask, err := base.GetTask(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	afterRuns, err := base.ListRuns(context.Background(), domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterTask, beforeTask) || !reflect.DeepEqual(afterRuns, beforeRuns) {
		t.Fatalf("failed Start changed records: before task=%#v runs=%#v after task=%#v runs=%#v", beforeTask, beforeRuns, afterTask, afterRuns)
	}
}

func TestManagerTerminalUpdateRollsBackWhenNotificationSaveFails(t *testing.T) {
	base, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, base)
	setup := New(base, DefaultParallelLimits())
	task, err := setup.Create(context.Background(), domaintask.Task{Title: "atomic terminal", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Start(context.Background(), task.TaskID); err != nil {
		t.Fatal(err)
	}
	beforeTask, err := base.GetTask(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRuns, err := base.ListRuns(context.Background(), domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("notification append failed")
	manager := New(&faultTaskStore{Store: base, failOperation: "notification", failErr: injected}, DefaultParallelLimits())
	if _, err := manager.Succeed(context.Background(), task.TaskID, "done"); !errors.Is(err, injected) {
		t.Fatalf("Succeed error = %v, want injected error", err)
	}
	afterTask, err := base.GetTask(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	afterRuns, err := base.ListRuns(context.Background(), domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	afterNotifications, err := base.ListNotifications(context.Background(), 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterTask, beforeTask) || !reflect.DeepEqual(afterRuns, beforeRuns) || len(afterNotifications) != 0 {
		t.Fatalf("failed terminal update changed records: before task=%#v runs=%#v after task=%#v runs=%#v notifications=%#v", beforeTask, beforeRuns, afterTask, afterRuns, afterNotifications)
	}
}

func TestManagerConcurrentStartsHonorGlobalParallelLimit(t *testing.T) {
	base, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, base)
	limits := ParallelLimits{Global: 1, PerModule: 10, CodingTasks: 10, LongResearchTasks: 10, DestructiveTasks: 10}
	setup := New(base, limits)
	first, err := setup.Create(context.Background(), domaintask.Task{Title: "parallel first", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := setup.Create(context.Background(), domaintask.Task{Title: "parallel second", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	managers := []*Manager{New(base, limits), New(base, limits)}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for index, task := range []domaintask.Task{first, second} {
		manager := managers[index]
		go func() {
			<-start
			_, err := manager.Start(context.Background(), task.TaskID)
			errs <- err
		}()
	}
	close(start)
	var succeeded, limited int
	for range 2 {
		err := <-errs
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrParallelLimit):
			limited++
		default:
			t.Fatalf("concurrent Start error = %v, want success or ErrParallelLimit", err)
		}
	}
	if succeeded != 1 || limited != 1 {
		t.Fatalf("concurrent Start outcomes = succeeded %d limited %d, want 1/1", succeeded, limited)
	}
	running, err := base.ListTasks(context.Background(), domaintask.Filter{Status: domaintask.StatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 1 {
		t.Fatalf("running Tasks = %d, want 1: %#v", len(running), running)
	}
	runs, err := base.ListRuns(context.Background(), domaintask.RunFilter{Status: domaintask.RunStatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].TaskID != running[0].TaskID {
		t.Fatalf("running Runs = %#v for Tasks %#v, want one matching pair", runs, running)
	}
}
