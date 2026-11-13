package heartbeat

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type heartbeatActorAdmissionFake struct {
	actor     string
	priority  domaintask.Priority
	available bool
	err       error
	calls     int
	releases  int
	onRelease func()
}

func (a *heartbeatActorAdmissionFake) Acquire(context.Context, string, domaintask.Priority) (func(), error) {
	return nil, errors.New("heartbeat worker used blocking actor admission")
}

func (a *heartbeatActorAdmissionFake) TryAcquire(_ context.Context, actor string, priority domaintask.Priority) (func(), bool, error) {
	a.calls++
	a.actor = actor
	a.priority = priority
	if a.err != nil {
		return nil, false, a.err
	}
	if !a.available {
		return nil, false, nil
	}
	return func() {
		a.releases++
		if a.onRelease != nil {
			a.onRelease()
		}
	}, true, nil
}

func newHeartbeatAdmissionTestOwner(t *testing.T) *taskmanager.Manager {
	t.Helper()
	store, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close heartbeat task store: %v", err)
		}
	})
	return taskmanager.New(store, taskmanager.DefaultParallelLimits())
}

func TestBeginWorkerDefersBeforeCreatingTaskWhenActorAdmissionIsBusy(t *testing.T) {
	owner := newHeartbeatAdmissionTestOwner(t)
	admission := &heartbeatActorAdmissionFake{}
	svc := NewHeartbeatService(nil, nil, t.TempDir(), 30).
		WithTaskOwner(owner, "shiro").
		WithActorAdmission(admission)

	_, _, _, err := svc.beginWorker(context.Background(), "run the scheduled work", "heartbeat", "scheduled")
	if !errors.Is(err, ErrHeartbeatAdmissionDeferred) {
		t.Fatalf("beginWorker() error = %v, want ErrHeartbeatAdmissionDeferred", err)
	}
	if admission.calls != 1 || admission.actor != "shiro" || admission.priority != domaintask.PriorityLow || admission.releases != 0 {
		t.Fatalf("actor admission calls=%+v, want one low-priority Shiro attempt and no release", admission)
	}
	tasks, err := owner.List(context.Background(), domaintask.Filter{})
	if err != nil || len(tasks) != 0 {
		t.Fatalf("deferred worker left Tasks behind: %+v %v", tasks, err)
	}
}

func TestBeginWorkerHoldsActorAdmissionUntilTaskFinalization(t *testing.T) {
	owner := newHeartbeatAdmissionTestOwner(t)
	admission := &heartbeatActorAdmissionFake{available: true}
	var taskID modulecore.TaskID
	admission.onRelease = func() {
		task, err := owner.Get(context.Background(), taskID)
		if err != nil || task.Status != domaintask.StatusSucceeded {
			t.Errorf("actor admission released before Task finalization: Task=%+v err=%v", task, err)
		}
	}
	svc := NewHeartbeatService(nil, nil, t.TempDir(), 30).
		WithTaskOwner(owner, "shiro").
		WithActorAdmission(admission)

	_, input, finish, err := svc.beginWorker(context.Background(), "run the scheduled work", "heartbeat", "scheduled")
	if err != nil {
		t.Fatalf("beginWorker() error = %v", err)
	}
	taskID = input.RootTaskID()
	if admission.releases != 0 {
		t.Fatalf("actor admission was released before finish: %d", admission.releases)
	}
	if err := finish(nil); err != nil {
		t.Fatalf("finish() error = %v", err)
	}
	if err := finish(errors.New("duplicate finish")); err != nil {
		t.Fatalf("second finish() error = %v, want the first result", err)
	}
	if admission.calls != 1 || admission.actor != "shiro" || admission.priority != domaintask.PriorityLow || admission.releases != 1 {
		t.Fatalf("actor admission calls=%+v, want one low-priority Shiro lease released once", admission)
	}
}

func TestBeginWorkerReleasesActorAdmissionWhenTaskSetupFails(t *testing.T) {
	owner := newHeartbeatAdmissionTestOwner(t)
	setupErr := errors.New("task setup failed")
	admission := &heartbeatActorAdmissionFake{available: true}
	svc := NewHeartbeatService(nil, nil, t.TempDir(), 30).
		WithTaskOwner(failingHeartbeatTaskOwner{TaskOwner: owner, admitErr: setupErr}, "shiro").
		WithActorAdmission(admission)

	_, _, _, err := svc.beginWorker(context.Background(), "run the scheduled work", "heartbeat", "scheduled")
	if !errors.Is(err, setupErr) || errors.Is(err, ErrHeartbeatAdmissionDeferred) {
		t.Fatalf("beginWorker() error = %v, want setup failure", err)
	}
	if admission.calls != 1 || admission.releases != 1 {
		t.Fatalf("actor admission calls=%+v, want one acquired lease released on setup failure", admission)
	}
}
