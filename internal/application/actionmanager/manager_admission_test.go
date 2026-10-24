package actionmanager_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	actionstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type signalingActionStore struct {
	actionmanager.Store
	entered chan struct{}
}

func (s *signalingActionStore) Transaction(ctx context.Context, callback func(actionmanager.Store) error) error {
	s.entered <- struct{}{}
	return s.Store.Transaction(ctx, callback)
}

func openManagerAdmissionFixture(t *testing.T) (*actionmanager.Manager, *actionstore.JSONLStore, *persistencequeue.Queue[persistencequeue.PersistenceCommand], string, chan struct{}) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "actions")
	base, err := actionstore.NewJSONLStoreWithOptions(root, actionstore.OpenOptions{Index: true})
	if err != nil {
		t.Fatalf("open Action store: %v", err)
	}
	queue, err := persistencequeue.NewPersistenceQueue(8, 1<<20)
	if err != nil {
		t.Fatalf("create persistence queue: %v", err)
	}
	if err := base.SetPersistenceQueue(queue); err != nil {
		t.Fatalf("attach persistence queue: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := queue.Shutdown(ctx); err != nil {
			t.Errorf("shutdown persistence queue: %v", err)
		}
	})
	entered := make(chan struct{}, 8)
	manager := actionmanager.New(&signalingActionStore{Store: base, entered: entered})
	return manager, base, queue, root, entered
}

func holdManagerActionStoreLock(t *testing.T, root string) func() {
	t.Helper()
	holder, err := jsonlbatch.New(root, []string{"action_state.jsonl", "action_attempt.jsonl"})
	if err != nil {
		t.Fatalf("open test lock holder: %v", err)
	}
	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- holder.Read(context.Background(), func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	select {
	case <-locked:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("test did not acquire Action store OS lock")
	}
	var released bool
	return func() {
		if released {
			return
		}
		released = true
		close(release)
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("release test OS lock: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("test lock holder did not finish")
		}
	}
}

func TestCreateActionAdmissionDoesNotWaitBehindSynchronousManagerWriter(t *testing.T) {
	manager, _, queue, root, entered := openManagerAdmissionFixture(t)
	unlock := holdManagerActionStoreLock(t, root)
	defer unlock()

	syncResult := make(chan error, 1)
	go func() {
		_, _, err := manager.CreateAction(context.Background(), actionmanager.CreateInput{
			TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), Kind: domainaction.KindTool, Name: "sync.writer",
		})
		syncResult <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("synchronous CreateAction did not reach its store transaction")
	}

	admission, err := persistencequeue.Admit(queue, 1, 128<<10)
	if err != nil {
		t.Fatalf("admit second Action: %v", err)
	}
	defer admission.Release()
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	completed := make(chan error, 1)
	go func() {
		_, _, err := manager.CreateAction(ctx, actionmanager.CreateInput{
			TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), Kind: domainaction.KindTool, Name: "admitted.writer",
		})
		completed <- err
	}()
	select {
	case err := <-completed:
		if !errors.Is(err, actionmanager.ErrAdmissionBusy) {
			t.Fatalf("admitted CreateAction while sync writer holds Manager lock = %v, want ErrAdmissionBusy", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("admitted CreateAction waited for the synchronous Manager writer")
	}

	unlock()
	select {
	case err := <-syncResult:
		if err != nil {
			t.Fatalf("synchronous CreateAction after lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("synchronous CreateAction did not finish after lock release")
	}
}

func TestCloseAttemptAdmissionDoesNotWaitBehindSynchronousManagerWriter(t *testing.T) {
	manager, _, queue, root, entered := openManagerAdmissionFixture(t)
	action, attempt, err := manager.CreateAction(context.Background(), actionmanager.CreateInput{
		TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), Kind: domainaction.KindTool, Name: "close.sync.writer",
	})
	if err != nil {
		t.Fatalf("seed CreateAction: %v", err)
	}
	<-entered

	unlock := holdManagerActionStoreLock(t, root)
	defer unlock()
	syncResult := make(chan error, 1)
	go func() {
		_, err := manager.CloseAttempt(context.Background(), action.ActionID, attempt.AttemptID,
			actionmanager.AttemptOutcomeOf(nil, "sync completion"), nil)
		syncResult <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("synchronous CloseAttempt did not reach its store transaction")
	}

	admission, err := persistencequeue.Admit(queue, 1, 128<<10)
	if err != nil {
		t.Fatalf("admit second Attempt close: %v", err)
	}
	defer admission.Release()
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	completed := make(chan error, 1)
	go func() {
		_, err := manager.CloseAttempt(ctx, action.ActionID, attempt.AttemptID,
			actionmanager.AttemptOutcomeOf(nil, "admitted completion"), nil)
		completed <- err
	}()
	select {
	case err := <-completed:
		if !errors.Is(err, actionmanager.ErrAdmissionBusy) {
			t.Fatalf("admitted CloseAttempt while sync writer holds Manager lock = %v, want ErrAdmissionBusy", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("admitted CloseAttempt waited for the synchronous Manager writer")
	}

	unlock()
	select {
	case err := <-syncResult:
		if err != nil {
			t.Fatalf("synchronous CloseAttempt after lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("synchronous CloseAttempt did not finish after lock release")
	}
}
