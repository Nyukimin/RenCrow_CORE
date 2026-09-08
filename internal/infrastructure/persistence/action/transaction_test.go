package action

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestJSONLStoreTransactionConcurrentSaveFinalizationIsAtomic(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, err := NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	now := time.Now().UTC()
	actions := make([]domainaction.Action, 16)
	for index := range actions {
		actions[index] = domainaction.Action{
			ActionID:  modulecore.NewActionID(),
			TaskID:    modulecore.NewTaskID(),
			RunID:     modulecore.NewRunID(),
			Kind:      domainaction.KindTool,
			Status:    domainaction.StatusOpen,
			CreatedAt: now.Add(time.Duration(index) * time.Nanosecond),
			UpdatedAt: now.Add(time.Duration(index) * time.Nanosecond),
		}
	}
	type saveResult struct {
		index int
		err   error
	}
	started := make(chan struct{})
	saveResults := make(chan saveResult, len(actions))
	var retained domainaction.Store
	transactionDone := make(chan error, 1)
	go func() {
		transactionDone <- store.Transaction(ctx, func(tx domainaction.Store) error {
			retained = tx
			for index, item := range actions {
				go func(index int, item domainaction.Action) {
					<-started
					saveResults <- saveResult{index: index, err: tx.SaveAction(ctx, item)}
				}(index, item)
			}
			close(started)
			// Let the transaction finalizer race with saves released above. A
			// successful Save must be in the committed payload; a Save that
			// loses the lifecycle race must report expiry and leave no record.
			return nil
		})
	}()
	var transactionErr error
	select {
	case transactionErr = <-transactionDone:
	case <-ctx.Done():
		t.Fatalf("Transaction() did not finish: %v", ctx.Err())
	}
	if transactionErr != nil {
		t.Fatalf("Transaction() error = %v", transactionErr)
	}
	results := make([]error, len(actions))
	for range actions {
		select {
		case result := <-saveResults:
			results[result.index] = result.err
		case <-ctx.Done():
			t.Fatalf("concurrent SaveAction() results did not finish: %v", ctx.Err())
		}
	}
	for index, item := range actions {
		got, getErr := store.GetAction(context.Background(), item.ActionID)
		switch {
		case results[index] == nil:
			if getErr != nil {
				t.Fatalf("action %d SaveAction() succeeded but GetAction() error = %v", index, getErr)
			}
			if got.ActionID != item.ActionID {
				t.Fatalf("action %d persisted id = %s, want %s", index, got.ActionID, item.ActionID)
			}
		case errors.Is(results[index], errExpiredTransaction):
			if !errors.Is(getErr, domainaction.ErrNotFound) {
				t.Fatalf("action %d expired SaveAction() error = %v, persisted lookup = %v", index, results[index], getErr)
			}
		default:
			t.Fatalf("action %d SaveAction() error = %v, want nil or expired transaction", index, results[index])
		}
	}
	if _, err := retained.GetAction(context.Background(), modulecore.NewActionID()); !errors.Is(err, errExpiredTransaction) {
		t.Fatalf("retained transaction store error = %v, want expired transaction", err)
	}
}

func TestJSONLStoreTransactionExpiresAfterCallbackError(t *testing.T) {
	t.Parallel()
	store, err := NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	var retained domainaction.Store
	wantErr := errors.New("callback failed")
	if err := store.Transaction(context.Background(), func(tx domainaction.Store) error {
		retained = tx
		return wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("Transaction() error = %v, want %v", err, wantErr)
	}
	if retained == nil {
		t.Fatal("transaction callback did not provide a store")
	}
	if _, err := retained.GetAction(context.Background(), modulecore.NewActionID()); !errors.Is(err, errExpiredTransaction) {
		t.Fatalf("retained store error = %v, want expired transaction", err)
	}
}
