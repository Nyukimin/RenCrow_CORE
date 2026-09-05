package taskmanager

import (
	"context"
	"errors"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestManagerLifecycleAndNotification(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
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
