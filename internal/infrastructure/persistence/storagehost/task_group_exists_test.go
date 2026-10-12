package storagehost

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Decision D8 over the remote profile. The Task owner runs in the CORE process
// and reads through the storage host session, so the refusal of an existing
// TaskID is produced where the callback runs and reaches the caller unchanged:
// errors.Is(err, ErrTaskExists) holds with no new wire code and no change to the
// RPC payloads.
func TestTaskGroupRefusesAnExistingTaskIDThroughTheRemoteStore(t *testing.T) {
	fixture := newTaskGroupFixture(t, time.Second)
	manager := taskmanager.New(fixture.store, taskmanager.DefaultParallelLimits())
	ctx := context.Background()
	taskID := modulecore.NewTaskID()

	if _, err := manager.Create(ctx, domaintask.Task{TaskID: taskID, Title: "first", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{UserIntent: "first"}); err != nil {
		t.Fatalf("remote Create: %v", err)
	}
	before, err := manager.Get(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeOwner, err := fixture.owner.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}

	_, err = manager.Create(ctx, domaintask.Task{TaskID: taskID, Title: "second", Route: domaintask.RouteResearch}, domaintask.SharedRoleContext{UserIntent: "second"})
	if !errors.Is(err, domaintask.ErrTaskExists) {
		t.Fatalf("remote Create of an existing TaskID: error = %v, want errors.Is(ErrTaskExists)", err)
	}
	if errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("ErrTaskExists must not match ErrNotFound: %v", err)
	}
	_, _, err = manager.CreateAndStartRun(ctx, domaintask.Task{TaskID: taskID, Title: "third", Route: domaintask.RouteGeneral, Assignee: "mio"}, domaintask.SharedRoleContext{})
	if !errors.Is(err, domaintask.ErrTaskExists) {
		t.Fatalf("remote CreateAndStartRun of an existing TaskID: error = %v, want errors.Is(ErrTaskExists)", err)
	}

	after, err := manager.Get(ctx, taskID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("a refused remote Create changed the Task:\nbefore=%+v\nafter=%+v err=%v", before, after, err)
	}
	afterOwner, err := fixture.owner.GetTask(ctx, taskID)
	if err != nil || !reflect.DeepEqual(beforeOwner, afterOwner) {
		t.Fatalf("the storage host owner stored another version:\nbefore=%+v\nafter=%+v err=%v", beforeOwner, afterOwner, err)
	}
	runs, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
	if err != nil || len(runs) != 0 {
		t.Fatalf("a refused remote CreateAndStartRun left Runs: %v err=%v", runs, err)
	}
}
