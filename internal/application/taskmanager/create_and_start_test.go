package taskmanager

import (
	"context"
	"errors"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newCreateAndStartManager(t *testing.T, limits ParallelLimits) *Manager {
	t.Helper()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	return New(store, limits)
}

func assertStoreCounts(t *testing.T, manager *Manager, wantTasks, wantRuns int) {
	t.Helper()
	tasks, err := manager.List(context.Background(), domaintask.Filter{Limit: 10000})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := manager.ListRuns(context.Background(), domaintask.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != wantTasks || len(runs) != wantRuns {
		t.Fatalf("store holds %d Tasks and %d Runs, want %d and %d", len(tasks), len(runs), wantTasks, wantRuns)
	}
}

func TestCreateAndStartRunAdmitsRunningTaskWithFirstRunAndContext(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())

	task, run, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "admit", Route: domaintask.RouteGeneral, Assignee: "shiro",
	}, domaintask.SharedRoleContext{CurrentPlan: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domaintask.StatusRunning || task.StartedAt == nil {
		t.Fatalf("task=%#v, want running with StartedAt", task)
	}
	if run.TaskID != task.TaskID || run.StartReason != domaintask.RunStartReasonFirst || run.Status != domaintask.RunStatusRunning || run.Assignee != "shiro" {
		t.Fatalf("run=%#v", run)
	}
	stored, err := manager.Get(context.Background(), task.TaskID)
	if err != nil || stored.Status != domaintask.StatusRunning {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	shared, err := manager.Context(context.Background(), task.TaskID)
	if err != nil || shared.CurrentPlan != "plan" {
		t.Fatalf("context=%#v err=%v", shared, err)
	}
	assertStoreCounts(t, manager, 1, 1)
}

func TestCreateAndStartRunHonorsExplicitTaskIDAndAllocatesOtherwise(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	want := modulecore.NewTaskID()

	explicit, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{TaskID: want, Title: "explicit", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil || explicit.TaskID != want {
		t.Fatalf("explicit=%#v err=%v", explicit, err)
	}
	allocated, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{Title: "allocated", Route: domaintask.RouteResearch}, domaintask.SharedRoleContext{})
	if err != nil || allocated.TaskID.Validate() != nil || allocated.TaskID == want {
		t.Fatalf("allocated=%#v err=%v", allocated, err)
	}
}

// The reason this entry exists: a refused admission persists nothing, so no
// Run-less queued Task can accumulate behind a full execution capacity.
func TestCreateAndStartRunRefusalPersistsNothing(t *testing.T) {
	limits := DefaultParallelLimits()
	limits.Global = 1
	manager := newCreateAndStartManager(t, limits)
	occupant, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{Title: "occupant", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}

	refusedID := modulecore.NewTaskID()
	_, _, err = manager.CreateAndStartRun(context.Background(), domaintask.Task{TaskID: refusedID, Title: "refused", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{CurrentPlan: "plan"})
	if !errors.Is(err, ErrParallelLimit) {
		t.Fatalf("error=%v, want ErrParallelLimit", err)
	}
	assertStoreCounts(t, manager, 1, 1)
	if _, err := manager.Get(context.Background(), refusedID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("refused Task lookup error=%v, want not found", err)
	}
	if _, err := manager.Context(context.Background(), refusedID); err == nil {
		t.Fatal("refused Task left a shared context behind")
	}
	notifications, err := manager.Notifications(context.Background(), 100, false)
	if err != nil || len(notifications) != 0 {
		t.Fatalf("notifications=%#v err=%v", notifications, err)
	}

	// The refusal consumed nothing: once the occupant ends, admission succeeds.
	if _, err := manager.Succeed(context.Background(), occupant.TaskID, "done"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{Title: "after", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{}); err != nil {
		t.Fatalf("admission after capacity was freed: %v", err)
	}
}

func TestCreateAndStartRunRefusalByRouteLimitPersistsNothing(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	if _, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{Title: "ops one", Route: domaintask.RouteOperations}, domaintask.SharedRoleContext{}); err != nil {
		t.Fatal(err)
	}

	_, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{Title: "ops two", Route: domaintask.RouteOperations}, domaintask.SharedRoleContext{})
	if !errors.Is(err, ErrParallelLimit) {
		t.Fatalf("error=%v, want ErrParallelLimit", err)
	}
	assertStoreCounts(t, manager, 1, 1)
}

func TestCreateAndStartRunRefusalByUnmetDependencyPersistsNothing(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	dependency, err := manager.Create(context.Background(), domaintask.Task{Title: "dependency", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "dependent", Route: domaintask.RouteGeneral, DependencyTaskIDs: []modulecore.TaskID{dependency.TaskID},
	}, domaintask.SharedRoleContext{})
	if err == nil {
		t.Fatal("a Task with an unmet dependency was admitted")
	}
	assertStoreCounts(t, manager, 1, 0)
}

func TestCreateAndStartRunInvalidInputPersistsNothing(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	if _, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{}); err == nil {
		t.Fatal("a Task without a title was admitted")
	}
	mismatched := domaintask.SharedRoleContext{TaskID: modulecore.NewTaskID()}
	if _, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{Title: "mismatch", Route: domaintask.RouteGeneral}, mismatched); err == nil {
		t.Fatal("a mismatched context was admitted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := manager.CreateAndStartRun(cancelled, domaintask.Task{Title: "cancelled", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{}); err == nil {
		t.Fatal("a cancelled context was admitted")
	}
	assertStoreCounts(t, manager, 0, 0)
}
