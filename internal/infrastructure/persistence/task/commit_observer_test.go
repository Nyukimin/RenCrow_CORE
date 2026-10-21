package task

import (
	"context"
	"errors"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestJSONLStoreCommitObserverRunsOnlyAfterSuccessfulWrite(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	defer store.Close()
	observed := make(chan struct{}, 8)
	store.SetCommitObserver(func() {
		observed <- struct{}{}
	})
	now := time.Now().UTC()
	task := domaintask.Task{
		TaskID: modulecore.NewTaskID(), Title: "commit observer", Route: domaintask.RouteGeneral,
		Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal,
		InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now,
	}
	injected := errors.New("callback rejected")
	err = store.Transaction(context.Background(), func(tx domaintask.Store) error {
		if err := tx.SaveTask(context.Background(), task); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("failed Transaction error = %v", err)
	}
	assertNoObservation(t, observed)

	if err := store.ReadTransaction(context.Background(), func(tx domaintask.Store) error {
		_, err := tx.GetTask(context.Background(), task.TaskID)
		if errors.Is(err, domaintask.ErrNotFound) {
			return nil
		}
		return err
	}); err != nil {
		t.Fatalf("ReadTransaction() error = %v", err)
	}
	assertNoObservation(t, observed)

	if err := store.SaveTask(context.Background(), task); err != nil {
		t.Fatalf("SaveTask() error = %v", err)
	}
	select {
	case <-observed:
	case <-time.After(2 * time.Second):
		t.Fatal("commit observer was not called after a successful write")
	}

	store.SetCommitObserver(nil)
	task.Title = "observer removed"
	if err := store.SaveTask(context.Background(), task); err != nil {
		t.Fatalf("SaveTask(second) error = %v", err)
	}
	assertNoObservation(t, observed)
}

func assertNoObservation(t *testing.T, observed <-chan struct{}) {
	t.Helper()
	select {
	case <-observed:
		t.Fatal("commit observer ran for a non-committed or read-only transaction")
	default:
	}
}
