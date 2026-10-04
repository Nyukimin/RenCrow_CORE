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
