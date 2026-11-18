package task

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestJSONLStoreGlobalTaskOperationSharesReceiptAndCommitsAtomically(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	defer store.Close()
	taskID := modulecore.NewTaskID()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	taskValue := domaintask.Task{
		TaskID: taskID, Title: "global operation", Route: domaintask.RouteGeneral,
		Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal,
		InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now,
	}
	contextValue := domaintask.SharedRoleContext{TaskID: taskID, UserIntent: "atomic pair", UpdatedAt: now}
	requestHash := taskOperationHash("global-operation")
	var callbacks int
	run := func() (json.RawMessage, error) {
		return store.ExecuteIdempotentGlobalTaskOperation(context.Background(), "global-op-1", requestHash, func(tx domaintask.Store) (json.RawMessage, error) {
			callbacks++
			if err := tx.SaveTask(context.Background(), taskValue); err != nil {
				return nil, err
			}
			if err := tx.SaveContext(context.Background(), contextValue); err != nil {
				return nil, err
			}
			got, err := tx.GetTask(context.Background(), taskID)
			if err != nil || got.Title != taskValue.Title {
				return nil, errors.New("global owner transaction lost read-your-writes")
			}
			return json.RawMessage(`{"committed":true}`), nil
		})
	}
	first, err := run()
	if err != nil {
		t.Fatalf("first global operation: %v", err)
	}
	second, err := run()
	if err != nil {
		t.Fatalf("receipt replay: %v", err)
	}
	if callbacks != 1 || string(first) != string(second) {
		t.Fatalf("callbacks=%d first=%s second=%s, want one callback and same durable result", callbacks, first, second)
	}
	if _, err := store.ExecuteIdempotentGlobalTaskOperation(context.Background(), "global-op-1", taskOperationHash("changed"), func(domaintask.Store) (json.RawMessage, error) {
		t.Fatal("conflicting request callback was invoked")
		return nil, nil
	}); !errors.Is(err, ErrTaskOperationConflict) {
		t.Fatalf("changed request error=%v, want ErrTaskOperationConflict", err)
	}
	receipt, err := store.LookupTaskOperation(context.Background(), "global-op-1")
	if err != nil {
		t.Fatalf("LookupTaskOperation: %v", err)
	}
	if receipt.Scope != TaskOperationScopeGlobal || receipt.TaskID != "" || receipt.WriterGeneration == 0 {
		t.Fatalf("global receipt=%+v, want explicit global scope and no TaskID", receipt)
	}
	if _, err := store.GetTask(context.Background(), taskID); err != nil {
		t.Fatalf("Task and receipt were not committed together: %v", err)
	}
	if _, err := store.GetContext(context.Background(), taskID); err != nil {
		t.Fatalf("context and receipt were not committed together: %v", err)
	}
}

func TestGlobalTaskOperationReceiptRaisesWriterGenerationFloorAfterRestart(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	if _, err := store.ExecuteIdempotentGlobalTaskOperation(context.Background(), "global-floor-op", taskOperationHash("floor"), func(domaintask.Store) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}); err != nil {
		t.Fatalf("global operation: %v", err)
	}
	receipt, err := store.LookupTaskOperation(context.Background(), "global-floor-op")
	if err != nil {
		t.Fatalf("LookupTaskOperation: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("reopen after global receipt: %v", err)
	}
	defer reopened.Close()
	generation, err := reopened.WriterGeneration()
	if err != nil {
		t.Fatalf("WriterGeneration after restart: %v", err)
	}
	if generation <= receipt.WriterGeneration {
		t.Fatalf("writer generation=%d receipt generation=%d, global receipt floor was not enforced", generation, receipt.WriterGeneration)
	}
}

func TestLookupGlobalTaskOperationIsReadOnlyAndReturnsCopiedResult(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	defer store.Close()
	const operationID = "global-read-only"
	hash := taskOperationHash("read-only")
	want := json.RawMessage(`{"committed":true}`)
	if _, err := store.ExecuteIdempotentGlobalTaskOperation(context.Background(), operationID, hash, func(domaintask.Store) (json.RawMessage, error) {
		return append(json.RawMessage(nil), want...), nil
	}); err != nil {
		t.Fatalf("create global receipt: %v", err)
	}
	generationBefore, err := store.WriterGeneration()
	if err != nil {
		t.Fatalf("WriterGeneration before read: %v", err)
	}
	got, found, err := store.LookupGlobalTaskOperation(context.Background(), operationID, hash)
	if err != nil || !found || string(got) != string(want) {
		t.Fatalf("LookupGlobalTaskOperation=(%s,%t,%v), want (%s,true,nil)", got, found, err, want)
	}
	got[0] = 'x'
	if _, found, err := store.LookupGlobalTaskOperation(context.Background(), "global-absent", hash); err != nil || found {
		t.Fatalf("missing global receipt=(found %t, err %v), want (false,nil)", found, err)
	}
	again, found, err := store.LookupGlobalTaskOperation(context.Background(), operationID, hash)
	if err != nil || !found || string(again) != string(want) {
		t.Fatalf("second LookupGlobalTaskOperation=(%s,%t,%v), want independent copy %s", again, found, err, want)
	}
	generationAfter, err := store.WriterGeneration()
	if err != nil {
		t.Fatalf("WriterGeneration after read: %v", err)
	}
	if generationAfter != generationBefore {
		t.Fatalf("writer generation changed across read-only lookups: before=%d after=%d", generationBefore, generationAfter)
	}
}

func TestLookupGlobalTaskOperationRejectsChangedHashAndTaskScopedReceipt(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	globalID := "global-hash-conflict"
	globalHash := taskOperationHash("original")
	if _, err := store.ExecuteIdempotentGlobalTaskOperation(ctx, globalID, globalHash, func(domaintask.Store) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}); err != nil {
		t.Fatalf("create global receipt: %v", err)
	}
	if _, _, err := store.LookupGlobalTaskOperation(ctx, globalID, taskOperationHash("changed")); !errors.Is(err, ErrTaskOperationConflict) {
		t.Fatalf("changed request hash error=%v, want ErrTaskOperationConflict", err)
	}

	taskID := modulecore.NewTaskID()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	taskValue := domaintask.Task{
		TaskID: taskID, Title: "task scoped receipt", Route: domaintask.RouteGeneral,
		Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal,
		InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Transaction(ctx, func(tx domaintask.Store) error { return tx.SaveTask(ctx, taskValue) }); err != nil {
		t.Fatalf("save Task for scoped receipt: %v", err)
	}
	const scopedID = "task-scoped-global-query"
	scopedHash := taskOperationHash("task scoped")
	if _, err := store.ExecuteIdempotentTaskOperation(ctx, taskID, scopedID, scopedHash, func(domaintask.Store) (json.RawMessage, error) {
		return json.RawMessage(`{"task":true}`), nil
	}); err != nil {
		t.Fatalf("create task-scoped receipt: %v", err)
	}
	if _, _, err := store.LookupGlobalTaskOperation(ctx, scopedID, scopedHash); !errors.Is(err, ErrTaskOperationConflict) {
		t.Fatalf("task-scoped receipt lookup error=%v, want ErrTaskOperationConflict", err)
	}
}

func TestLookupGlobalTaskOperationPreservesCanceledClosedAndInvalidErrors(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	hash := taskOperationHash("lookup errors")
	if _, _, err := store.LookupGlobalTaskOperation(nil, "global-errors", hash); err == nil {
		t.Fatal("nil context unexpectedly passed lookup validation")
	}
	if _, _, err := store.LookupGlobalTaskOperation(context.Background(), "", hash); err == nil {
		t.Fatal("invalid operation ID unexpectedly passed lookup validation")
	}
	if _, _, err := store.LookupGlobalTaskOperation(context.Background(), "global-errors", "bad-hash"); err == nil {
		t.Fatal("invalid request hash unexpectedly passed lookup validation")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.LookupGlobalTaskOperation(canceled, "global-errors", hash); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup error=%v, want context.Canceled", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, err := store.LookupGlobalTaskOperation(context.Background(), "global-errors", hash); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed store lookup error=%v, want os.ErrClosed", err)
	}
}

func TestLookupGlobalTaskOperationDoesNotMapCorruptReceiptToNotFound(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: false})
	if err != nil {
		t.Fatalf("NewJSONLStoreWithOptions: %v", err)
	}
	defer store.Close()
	hash := taskOperationHash("corrupt receipt")
	if _, err := store.ExecuteIdempotentGlobalTaskOperation(context.Background(), "global-corrupt", hash, func(domaintask.Store) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}); err != nil {
		t.Fatalf("create receipt: %v", err)
	}
	receiptFile, err := os.OpenFile(filepath.Join(root, taskOperationReceiptFilename), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open receipt log for corruption fixture: %v", err)
	}
	if _, err := receiptFile.WriteString("{\"operation_id\":\n"); err != nil {
		_ = receiptFile.Close()
		t.Fatalf("append corrupt receipt fixture: %v", err)
	}
	if err := receiptFile.Close(); err != nil {
		t.Fatalf("close receipt fixture: %v", err)
	}
	if _, found, err := store.LookupGlobalTaskOperation(context.Background(), "global-corrupt", hash); found || !errors.Is(err, ErrTaskOperationReceiptCorrupt) {
		t.Fatalf("corrupt receipt lookup=(found %t, err %v), want corrupt error and no NotFound", found, err)
	}
}

func TestTaskOperationReceiptScopeRejectsInvalidGlobalTaskIDAndUnknownScope(t *testing.T) {
	for _, test := range []struct {
		name  string
		scope TaskOperationScope
		task  modulecore.TaskID
	}{
		{name: "global_with_task_id", scope: TaskOperationScopeGlobal, task: modulecore.NewTaskID()},
		{name: "unknown_scope", scope: "other", task: modulecore.NewTaskID()},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			receipt := TaskOperationReceipt{
				OperationID: "invalid-scope", RequestHash: taskOperationHash("scope"),
				Scope: test.scope, TaskID: test.task, Result: json.RawMessage(`{}`), WriterGeneration: 1,
			}
			encoded, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, taskOperationReceiptFilename), append(encoded, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			store, err := NewJSONLStore(root)
			if store != nil {
				_ = store.Close()
			}
			if !errors.Is(err, ErrTaskOperationReceiptCorrupt) {
				t.Fatalf("NewJSONLStore error=%v, want ErrTaskOperationReceiptCorrupt", err)
			}
		})
	}
}

func TestLegacyTaskOperationReceiptWithoutScopeRemainsReadableAndRaisesGenerationFloor(t *testing.T) {
	root := t.TempDir()
	taskID := modulecore.NewTaskID()
	legacy := struct {
		OperationID      string            `json:"operation_id"`
		RequestHash      string            `json:"request_hash"`
		TaskID           modulecore.TaskID `json:"task_id"`
		Result           json.RawMessage   `json:"result"`
		WriterGeneration uint64            `json:"writer_generation"`
	}{"legacy-op", taskOperationHash("legacy"), taskID, json.RawMessage(`{"ok":true}`), 1}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, taskOperationReceiptFilename), append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".writer.lock"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("NewJSONLStore legacy receipt: %v", err)
	}
	receipt, err := store.LookupTaskOperation(context.Background(), "legacy-op")
	if err != nil {
		t.Fatalf("LookupTaskOperation legacy receipt: %v", err)
	}
	generation, err := store.WriterGeneration()
	if err != nil {
		t.Fatalf("WriterGeneration: %v", err)
	}
	if receipt.Scope != "" || generation <= receipt.WriterGeneration {
		t.Fatalf("legacy receipt=%+v generation=%d, want readable legacy scope and a higher writer floor", receipt, generation)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
