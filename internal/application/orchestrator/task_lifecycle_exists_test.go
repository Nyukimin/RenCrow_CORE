package orchestrator

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Decision D8: a TaskID names one Task for its whole life, so the Task owner
// refuses a second creation with domaintask.ErrTaskExists. The lifecycle never
// treats that as success: a request that reuses an existing ID is not the
// resume of a running execution (that is attachExisting, which creates
// nothing), and it must not overwrite or re-open the stored Task.

func newRealLifecycle(t *testing.T) (*taskLifecycle, *taskmanager.Manager) {
	t.Helper()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	manager := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	return newTaskLifecycle(manager), manager
}

func TestTaskLifecycleRefusesToRecreateAnExistingRootTask(t *testing.T) {
	ctx := context.Background()
	lifecycle, manager := newRealLifecycle(t)
	rootID := modulecore.NewTaskID()

	if _, err := lifecycle.createRoot(ctx, ProcessMessageRequest{RootTaskID: string(rootID), UserMessage: "first request"}); err != nil {
		t.Fatal(err)
	}
	before, err := manager.Get(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	beforeContext, err := manager.Context(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}

	_, err = lifecycle.createRoot(ctx, ProcessMessageRequest{RootTaskID: string(rootID), UserMessage: "a different request reusing the ID"})
	if !errors.Is(err, domaintask.ErrTaskExists) {
		t.Fatalf("createRoot error = %v, want errors.Is(ErrTaskExists)", err)
	}
	after, err := manager.Get(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	afterContext, err := manager.Context(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeContext, afterContext) {
		t.Fatalf("a refused root creation changed the stored Task:\nbefore=%+v %+v\nafter=%+v %+v", before, beforeContext, after, afterContext)
	}
}

func TestTaskLifecycleRefusesToRecreateAFinishedRootTask(t *testing.T) {
	ctx := context.Background()
	lifecycle, manager := newRealLifecycle(t)
	rootID := modulecore.NewTaskID()
	req := ProcessMessageRequest{RootTaskID: string(rootID), UserMessage: "run once"}

	if _, err := lifecycle.createRoot(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(ctx, rootID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Succeed(ctx, rootID, "done"); err != nil {
		t.Fatal(err)
	}

	if _, err := lifecycle.createRoot(ctx, req); !errors.Is(err, domaintask.ErrTaskExists) {
		t.Fatalf("createRoot error = %v, want errors.Is(ErrTaskExists)", err)
	}
	stored, err := manager.Get(ctx, rootID)
	if err != nil || stored.Status != domaintask.StatusSucceeded {
		t.Fatalf("stored root = %+v err=%v, want it left succeeded rather than re-queued", stored, err)
	}
}

// missOnceTaskLifecycleManager reproduces the check-then-act gap of the repair
// path: the lifecycle's pre-check Get happens outside the Create transaction,
// so the Task can appear between the two. The first Get reports "not found"
// although the Task is already stored.
type missOnceTaskLifecycleManager struct {
	TaskLifecycleManager
	missed bool
}

func (m *missOnceTaskLifecycleManager) Get(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	if !m.missed {
		m.missed = true
		return domaintask.Task{}, domaintask.ErrNotFound
	}
	return m.TaskLifecycleManager.Get(ctx, taskID)
}

func TestTaskLifecycleRepairRefusesAnExistingTaskEvenWhenThePreCheckMisses(t *testing.T) {
	ctx := context.Background()
	_, manager := newRealLifecycle(t)
	repairID := modulecore.NewTaskID()
	if _, err := manager.Create(ctx, domaintask.Task{TaskID: repairID, Title: "earlier repair", Route: domaintask.RouteGeneral, OwnerID: taskLifecycleMio},
		domaintask.SharedRoleContext{UserIntent: "earlier"}); err != nil {
		t.Fatal(err)
	}
	before, err := manager.Get(ctx, repairID)
	if err != nil {
		t.Fatal(err)
	}

	lifecycle := newTaskLifecycle(&missOnceTaskLifecycleManager{TaskLifecycleManager: manager})
	events := newRecordingTaskLifecycleEventPort()
	req := normalizeRepairProcessRequest(ProcessRepairRequest{TaskID: repairID, Reason: "retry", Instruction: "again", TargetRoute: "CODE2"})

	_, err = lifecycle.startRepairExecution(ctx, events, req, routing.RouteCODE2)
	if err == nil {
		t.Fatal("an existing repair Task was accepted after the pre-check missed it")
	}
	if !errors.Is(err, domaintask.ErrTaskExists) {
		t.Fatalf("error = %v, want errors.Is(ErrTaskExists)", err)
	}
	if want := "repair task " + repairID.String() + " already exists"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not read %q (the same refusal as the pre-check)", err, want)
	}
	if len(events.events) != 0 {
		t.Fatalf("a refused repair published events: %#v", events.events)
	}
	after, err := manager.Get(ctx, repairID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("a refused repair changed the existing Task (it must not even be failed):\nbefore=%+v\nafter=%+v err=%v", before, after, err)
	}
	runs, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: repairID})
	if err != nil || len(runs) != 0 {
		t.Fatalf("a refused repair started a Run: runs=%v err=%v", runs, err)
	}
}
