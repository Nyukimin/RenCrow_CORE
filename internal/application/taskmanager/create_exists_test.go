package taskmanager

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Decision D8: a TaskID names one Task for its whole life. Create (and
// CreateAndStartRun) used to append a new version over an existing Task with
// the same ID, which re-opened terminal Tasks and let a retransmitted request
// run twice. The Task owner now refuses the second creation with
// domaintask.ErrTaskExists, decided in the same Task transaction that would
// write it.

func newExistsTestManager(t *testing.T) *Manager {
	t.Helper()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	return New(store, DefaultParallelLimits())
}

type storedTaskState struct {
	task    domaintask.Task
	context domaintask.SharedRoleContext
	runs    []domaintask.Run
	tasks   int
}

func snapshotTaskState(t *testing.T, manager *Manager, taskID modulecore.TaskID) storedTaskState {
	t.Helper()
	ctx := context.Background()
	task, err := manager.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("get stored Task: %v", err)
	}
	shared, err := manager.Context(ctx, taskID)
	if err != nil {
		t.Fatalf("get stored context: %v", err)
	}
	runs, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
	if err != nil {
		t.Fatalf("list stored Runs: %v", err)
	}
	tasks, err := manager.List(ctx, domaintask.Filter{Limit: 10000})
	if err != nil {
		t.Fatalf("list stored Tasks: %v", err)
	}
	return storedTaskState{task: task, context: shared, runs: runs, tasks: len(tasks)}
}

func requireTaskExists(t *testing.T, err error, taskID modulecore.TaskID) {
	t.Helper()
	if err == nil {
		t.Fatal("expected ErrTaskExists, got nil")
	}
	if !errors.Is(err, domaintask.ErrTaskExists) {
		t.Fatalf("error = %v, want errors.Is(ErrTaskExists)", err)
	}
	if errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("ErrTaskExists must not also match ErrNotFound: %v", err)
	}
	if !strings.Contains(err.Error(), taskID.String()) {
		t.Fatalf("error %q does not name the refused task_id", err)
	}
}

func TestCreateRejectsExistingTaskIDAndKeepsStoredVersion(t *testing.T) {
	manager := newExistsTestManager(t)
	ctx := context.Background()
	taskID := modulecore.NewTaskID()
	if _, err := manager.Create(ctx, domaintask.Task{TaskID: taskID, Title: "first version", Route: domaintask.RouteGeneral},
		domaintask.SharedRoleContext{UserIntent: "first intent"}); err != nil {
		t.Fatal(err)
	}
	before := snapshotTaskState(t, manager, taskID)

	_, err := manager.Create(ctx, domaintask.Task{TaskID: taskID, Title: "secret-title-xyz", Route: domaintask.RouteResearch},
		domaintask.SharedRoleContext{UserIntent: "secret-intent-xyz"})
	requireTaskExists(t, err, taskID)
	if strings.Contains(err.Error(), "secret-title-xyz") || strings.Contains(err.Error(), "secret-intent-xyz") {
		t.Fatalf("error leaks the refused record body: %v", err)
	}
	after := snapshotTaskState(t, manager, taskID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("a refused Create changed the store:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestCreateRejectsExistingTerminalTaskInsteadOfReopeningIt(t *testing.T) {
	manager := newExistsTestManager(t)
	ctx := context.Background()
	taskID := modulecore.NewTaskID()
	if _, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{TaskID: taskID, Title: "done", Route: domaintask.RouteGeneral, Assignee: "mio"},
		domaintask.SharedRoleContext{}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Succeed(ctx, taskID, "finished"); err != nil {
		t.Fatal(err)
	}
	before := snapshotTaskState(t, manager, taskID)
	if before.task.Status != domaintask.StatusSucceeded {
		t.Fatalf("fixture Task status = %s, want succeeded", before.task.Status)
	}

	_, err := manager.Create(ctx, domaintask.Task{TaskID: taskID, Title: "again", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	requireTaskExists(t, err, taskID)
	if after := snapshotTaskState(t, manager, taskID); !reflect.DeepEqual(before, after) {
		t.Fatalf("a refused Create re-opened or changed the terminal Task:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestCreateAndStartRunRejectsExistingTaskIDWithoutAnotherRun(t *testing.T) {
	manager := newExistsTestManager(t)
	ctx := context.Background()
	taskID := modulecore.NewTaskID()
	if _, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{TaskID: taskID, Title: "admit", Route: domaintask.RouteGeneral, Assignee: "shiro"},
		domaintask.SharedRoleContext{}); err != nil {
		t.Fatal(err)
	}
	before := snapshotTaskState(t, manager, taskID)

	_, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{TaskID: taskID, Title: "admit again", Route: domaintask.RouteGeneral, Assignee: "shiro"},
		domaintask.SharedRoleContext{})
	requireTaskExists(t, err, taskID)
	after := snapshotTaskState(t, manager, taskID)
	if len(after.runs) != 1 || !reflect.DeepEqual(before, after) {
		t.Fatalf("a refused CreateAndStartRun started another Run or changed the Task:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestCreateAndStartRunRejectsTaskThatWasOnlyCreated(t *testing.T) {
	manager := newExistsTestManager(t)
	ctx := context.Background()
	taskID := modulecore.NewTaskID()
	if _, err := manager.Create(ctx, domaintask.Task{TaskID: taskID, Title: "queued", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{}); err != nil {
		t.Fatal(err)
	}
	before := snapshotTaskState(t, manager, taskID)

	_, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{TaskID: taskID, Title: "queued", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	requireTaskExists(t, err, taskID)
	after := snapshotTaskState(t, manager, taskID)
	if !reflect.DeepEqual(before, after) || len(after.runs) != 0 {
		t.Fatalf("a refused CreateAndStartRun changed the queued Task:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestCreateRejectsSecondCreationInsideOneTransactionAndKeepsTheFirst(t *testing.T) {
	manager := newExistsTestManager(t)
	ctx := context.Background()
	taskID := modulecore.NewTaskID()
	var second error
	err := manager.taskTransaction(ctx, taskID, func(tx *Manager) error {
		if _, err := tx.createInTransaction(ctx, domaintask.Task{TaskID: taskID, Title: "first", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{}); err != nil {
			return err
		}
		_, second = tx.createInTransaction(ctx, domaintask.Task{TaskID: taskID, Title: "second", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	requireTaskExists(t, second, taskID)
	stored, err := manager.Get(ctx, taskID)
	if err != nil || stored.Title != "first" {
		t.Fatalf("stored = %+v err=%v, want the first creation", stored, err)
	}
}

func TestCreateRejectsExistingTaskIDAfterStoreReopen(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	taskID := modulecore.NewTaskID()

	first, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(first, DefaultParallelLimits()).Create(ctx, domaintask.Task{TaskID: taskID, Title: "before restart", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, second)
	manager := New(second, DefaultParallelLimits())
	before := snapshotTaskState(t, manager, taskID)
	_, err = manager.Create(ctx, domaintask.Task{TaskID: taskID, Title: "after restart", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	requireTaskExists(t, err, taskID)
	if after := snapshotTaskState(t, manager, taskID); !reflect.DeepEqual(before, after) {
		t.Fatalf("a refused Create after reopen changed the store:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestCreateDoesNotTreatNonCanonicalSpellingAsAnotherTask(t *testing.T) {
	manager := newExistsTestManager(t)
	ctx := context.Background()
	taskID := modulecore.NewTaskID()
	if _, err := manager.Create(ctx, domaintask.Task{TaskID: taskID, Title: "canonical", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{}); err != nil {
		t.Fatal(err)
	}
	before := snapshotTaskState(t, manager, taskID)

	upper := modulecore.TaskID(strings.ToUpper(string(taskID)))
	_, err := manager.Create(ctx, domaintask.Task{TaskID: upper, Title: "other spelling", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err == nil {
		t.Fatal("a non-canonical spelling of an existing TaskID was accepted as a new Task")
	}
	if after := snapshotTaskState(t, manager, taskID); !reflect.DeepEqual(before, after) {
		t.Fatalf("a refused non-canonical Create changed the store:\nbefore=%+v\nafter=%+v", before, after)
	}
}

// getTaskErrorStore makes every transactional GetTask fail with err, as a
// storage fault would. The existence decision must fail closed: only
// ErrNotFound may let a Task be created.
type getTaskErrorStore struct {
	domaintask.Store
	err error
}

func (s *getTaskErrorStore) TaskTransaction(ctx context.Context, taskID modulecore.TaskID, fn func(domaintask.Store) error) error {
	return s.Store.TaskTransaction(ctx, taskID, func(tx domaintask.Store) error {
		return fn(&getTaskErrorStore{Store: tx, err: s.err})
	})
}

func (s *getTaskErrorStore) GetTask(context.Context, modulecore.TaskID) (domaintask.Task, error) {
	return domaintask.Task{}, s.err
}

func TestCreateFailsClosedWhenTheExistenceCheckCannotBeAnswered(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	fault := errors.New("task store unavailable")
	manager := New(&getTaskErrorStore{Store: store, err: fault}, DefaultParallelLimits())
	taskID := modulecore.NewTaskID()

	_, err = manager.Create(context.Background(), domaintask.Task{TaskID: taskID, Title: "fault", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if !errors.Is(err, fault) || errors.Is(err, domaintask.ErrTaskExists) {
		t.Fatalf("error = %v, want the store fault and not ErrTaskExists", err)
	}
	if _, getErr := store.GetTask(context.Background(), taskID); !errors.Is(getErr, domaintask.ErrNotFound) {
		t.Fatalf("a Create whose existence check failed persisted a Task: %v", getErr)
	}
}
