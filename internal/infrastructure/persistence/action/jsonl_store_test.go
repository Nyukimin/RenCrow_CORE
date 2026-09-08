package action_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	actionstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestJSONLStoreActionAttemptRoundTrip(t *testing.T) {
	t.Parallel()
	store, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	action := domainaction.Action{
		ActionID:  modulecore.NewActionID(),
		TaskID:    modulecore.NewTaskID(),
		RunID:     modulecore.NewRunID(),
		Kind:      domainaction.KindExternalSend,
		Status:    domainaction.StatusOpen,
		CreatedAt: now,
		UpdatedAt: now,
	}
	attempt := domainaction.Attempt{
		AttemptID:   modulecore.NewAttemptID(),
		ActionID:    action.ActionID,
		StartReason: domainaction.AttemptStartReasonFirst,
		Status:      domainaction.AttemptStatusRunning,
		StartedAt:   now,
	}
	action.CurrentAttemptID = attempt.AttemptID
	if err := store.Transaction(ctx, func(tx actionmanager.Store) error {
		if err := tx.SaveAction(ctx, action); err != nil {
			return fmt.Errorf("save action: %w", err)
		}
		if err := tx.SaveAttempt(ctx, attempt); err != nil {
			return fmt.Errorf("save attempt: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetAction(ctx, action.ActionID)
	if err != nil {
		t.Fatalf("get action: %v", err)
	}
	if got.ActionID != action.ActionID || got.Kind != domainaction.KindExternalSend {
		t.Fatalf("unexpected action: %+v", got)
	}
	gotAttempt, err := store.GetAttempt(ctx, attempt.AttemptID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if gotAttempt.AttemptID != attempt.AttemptID {
		t.Fatalf("unexpected attempt: %+v", gotAttempt)
	}
}

func TestJSONLStoreRejectsRetainedTransactionStore(t *testing.T) {
	t.Parallel()
	store, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	var retained actionmanager.Store
	if err := store.Transaction(context.Background(), func(tx actionmanager.Store) error {
		retained = tx
		return nil
	}); err != nil {
		t.Fatalf("Transaction() error = %v", err)
	}
	if retained == nil {
		t.Fatal("transaction callback did not receive a store")
	}
	if _, err := retained.GetAction(context.Background(), modulecore.NewActionID()); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("retained transaction store error = %v, want expired-store error", err)
	}
}

func TestJSONLStoreReadTransactionRejectsWrites(t *testing.T) {
	t.Parallel()
	store, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	now := time.Now().UTC()
	action := domainaction.Action{
		ActionID:  modulecore.NewActionID(),
		TaskID:    modulecore.NewTaskID(),
		RunID:     modulecore.NewRunID(),
		Kind:      domainaction.KindTool,
		Status:    domainaction.StatusOpen,
		CreatedAt: now,
		UpdatedAt: now,
	}
	err = store.ReadTransaction(context.Background(), func(tx actionmanager.Store) error {
		return tx.SaveAction(context.Background(), action)
	})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("ReadTransaction() error = %v, want read-only error", err)
	}
}

func TestJSONLStoreNestedReadTransactionIsReadOnly(t *testing.T) {
	t.Parallel()
	store, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	err = store.Transaction(context.Background(), func(tx actionmanager.Store) error {
		return tx.ReadTransaction(context.Background(), func(read actionmanager.Store) error {
			return read.SaveAction(context.Background(), domainaction.Action{})
		})
	})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("nested ReadTransaction() error = %v, want read-only error", err)
	}
}

func TestJSONLStoreTransactionSerializesConcurrentOverlayWrites(t *testing.T) {
	t.Parallel()
	store, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	now := time.Now().UTC()
	actions := []domainaction.Action{
		{ActionID: modulecore.NewActionID(), TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), Kind: domainaction.KindTool, Status: domainaction.StatusOpen, CreatedAt: now, UpdatedAt: now},
		{ActionID: modulecore.NewActionID(), TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), Kind: domainaction.KindTool, Status: domainaction.StatusOpen, CreatedAt: now.Add(time.Nanosecond), UpdatedAt: now.Add(time.Nanosecond)},
	}
	if err := store.Transaction(context.Background(), func(tx actionmanager.Store) error {
		errs := make(chan error, len(actions))
		var wait sync.WaitGroup
		for _, item := range actions {
			item := item
			wait.Add(1)
			go func() {
				defer wait.Done()
				errs <- tx.SaveAction(context.Background(), item)
			}()
		}
		wait.Wait()
		close(errs)
		for saveErr := range errs {
			if saveErr != nil {
				return saveErr
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("concurrent transaction: %v", err)
	}
	items, err := store.ListActions(context.Background(), domainaction.Filter{})
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	if len(items) != len(actions) {
		t.Fatalf("actions = %d, want %d", len(items), len(actions))
	}
}

func TestJSONLStoreReadTransactionReturnsCoherentPair(t *testing.T) {
	t.Parallel()
	store, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	now := time.Now().UTC()
	actionID := modulecore.NewActionID()
	attemptID := modulecore.NewAttemptID()
	action := domainaction.Action{
		ActionID:         actionID,
		TaskID:           modulecore.NewTaskID(),
		RunID:            modulecore.NewRunID(),
		Kind:             domainaction.KindTool,
		Status:           domainaction.StatusOpen,
		CurrentAttemptID: attemptID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	attempt := domainaction.Attempt{
		AttemptID:   attemptID,
		ActionID:    actionID,
		StartReason: domainaction.AttemptStartReasonFirst,
		Status:      domainaction.AttemptStatusRunning,
		StartedAt:   now,
	}
	if err := store.Transaction(context.Background(), func(tx actionmanager.Store) error {
		if err := tx.SaveAction(context.Background(), action); err != nil {
			return err
		}
		return tx.SaveAttempt(context.Background(), attempt)
	}); err != nil {
		t.Fatalf("transaction: %v", err)
	}
	if err := store.ReadTransaction(context.Background(), func(tx actionmanager.Store) error {
		gotAction, err := tx.GetAction(context.Background(), actionID)
		if err != nil {
			return err
		}
		gotAttempt, err := tx.GetAttempt(context.Background(), attemptID)
		if err != nil {
			return err
		}
		if gotAction.CurrentAttemptID != gotAttempt.AttemptID || gotAttempt.ActionID != gotAction.ActionID {
			t.Fatalf("incoherent pair: action=%+v attempt=%+v", gotAction, gotAttempt)
		}
		return nil
	}); err != nil {
		t.Fatalf("ReadTransaction() error = %v", err)
	}
}

func TestJSONLStoreRejectsUnresolvedCurrentAttemptAtCommit(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "actions")
	store, err := actionstore.NewJSONLStore(root)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	now := time.Now().UTC()
	action := domainaction.Action{
		ActionID:         modulecore.NewActionID(),
		TaskID:           modulecore.NewTaskID(),
		RunID:            modulecore.NewRunID(),
		Kind:             domainaction.KindTool,
		Status:           domainaction.StatusOpen,
		CurrentAttemptID: modulecore.NewAttemptID(),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := store.Transaction(context.Background(), func(tx actionmanager.Store) error {
		return tx.SaveAction(context.Background(), action)
	}); err == nil || !strings.Contains(err.Error(), "unavailable current attempt") {
		t.Fatalf("unresolved current attempt transaction error = %v", err)
	}
	reopened, err := actionstore.NewJSONLStore(root)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	items, err := reopened.ListActions(context.Background(), domainaction.Filter{})
	if err != nil {
		t.Fatalf("list after rejected transaction: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("rejected action count = %d, want zero", len(items))
	}
}
