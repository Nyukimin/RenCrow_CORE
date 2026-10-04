package l1sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

func TestChatGPTStorageHostMutationReceiptsRecoverExactResultAfterReopen(t *testing.T) {
	t.Run("raw batch", func(t *testing.T) {
		path, store := chatGPTStorageHostTestStore(t)
		batch := chatGPTRawTestBatch("storagehost-raw", 1, 0, 1, 1)
		mutation := ChatGPTStorageHostMutation{RequestID: "storagehost-raw", OwnerID: "ren", ActorID: "ren", Batch: &batch, Apply: true}
		chatGPTStorageHostExecuteAndRecover(t, path, store, ledgerTestContext(t, mutation.RequestID, mutation.OwnerID), ChatGPTStorageHostImportBatch, mutation)
	})

	t.Run("ledger event", func(t *testing.T) {
		path, store := chatGPTStorageHostTestStore(t)
		input := ledgerTestInput("storagehost-event", "ren", "ren", ledgerTestBinding(), domainmemory.ChatGPTImportStateValidating)
		mutation := ChatGPTStorageHostMutation{Event: &input}
		chatGPTStorageHostExecuteAndRecover(t, path, store, ledgerTestContext(t, input.RequestID, input.OwnerID), ChatGPTStorageHostAppendEvent, mutation)
	})

	t.Run("retry", func(t *testing.T) {
		path, store := chatGPTStorageHostTestStore(t)
		fixture := appendChatGPTMachineFixture(t, store, "storagehost-retry", domainmemory.ProfilePromotionFailed)
		mutation := ChatGPTStorageHostMutation{RequestID: "storagehost-retry", OwnerID: "machine-owner", ActorID: "machine-owner", ExportID: fixture.exportID}
		chatGPTStorageHostExecuteAndRecover(t, path, store, ledgerTestContext(t, mutation.RequestID, mutation.OwnerID), ChatGPTStorageHostRetry, mutation)
	})

	t.Run("finalize", func(t *testing.T) {
		path, store := chatGPTStorageHostTestStore(t)
		fixture := appendChatGPTMachineFixture(t, store, "storagehost-finalize", domainmemory.ProfilePromotionCompleted)
		input := domainmemory.ChatGPTImportFinalizeInput{RequestID: "storagehost-finalize", OwnerID: "machine-owner", ActorID: "machine-owner", ExportID: fixture.exportID, Apply: true}
		mutation := ChatGPTStorageHostMutation{Finalize: &input}
		chatGPTStorageHostExecuteAndRecover(t, path, store, ledgerTestContext(t, input.RequestID, input.OwnerID), ChatGPTStorageHostFinalize, mutation)
	})

	t.Run("startup reconciliation", func(t *testing.T) {
		path, store := chatGPTStorageHostTestStore(t)
		input := ledgerTestInput("storagehost-reconcile-active", "ren", "ren", ledgerTestBinding(), domainmemory.ChatGPTImportStateValidating)
		if _, err := store.AppendChatGPTImportEvent(ledgerTestContext(t, input.RequestID, input.OwnerID), input); err != nil {
			t.Fatal(err)
		}
		chatGPTStorageHostExecuteAndRecover(t, path, store, context.Background(), ChatGPTStorageHostReconcile, ChatGPTStorageHostMutation{Reconcile: true})
	})
}

func TestChatGPTStorageHostReceiptConflictAndSubstitutionFailClosed(t *testing.T) {
	path, store := chatGPTStorageHostTestStore(t)
	input := ledgerTestInput("storagehost-proof", "ren", "ren", ledgerTestBinding(), domainmemory.ChatGPTImportStateValidating)
	mutation := ChatGPTStorageHostMutation{Event: &input}
	identity := chatGPTStorageHostTestIdentity(t, "storagehost-proof-op", ChatGPTStorageHostAppendEvent, "storagehost-proof")
	ctx := ledgerTestContext(t, input.RequestID, input.OwnerID)
	result, err := store.ExecuteChatGPTStorageHostOperation(ctx, identity, mutation)
	if err != nil {
		t.Fatal(err)
	}
	changed := identity
	changed.PayloadSHA256 = chatGPTStorageHostTestHash("changed-payload")
	if _, err := store.ExecuteChatGPTStorageHostOperation(ctx, changed, mutation); !errors.Is(err, ErrChatGPTStorageHostConflict) {
		t.Fatalf("same op changed payload error=%v, want conflict", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewL1SQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	substituted := result
	substituted.Event = new(domainmemory.ChatGPTImportEvent)
	*substituted.Event = *result.Event
	substituted.Event.EventID = "substituted-event"
	effect := chatGPTStorageHostEffect{Result: substituted}
	resultJSON, _ := json.Marshal(substituted)
	effectJSON, _ := json.Marshal(effect)
	resultHash := sha256.Sum256(resultJSON)
	effectHash := sha256.Sum256(effectJSON)
	resultSHA := hex.EncodeToString(resultHash[:])
	effectSHA := hex.EncodeToString(effectHash[:])
	proof := chatGPTStorageHostProofSHA(identity.Operation, identity.PayloadSHA256, identity.WriterGeneration, resultSHA, effectSHA)
	if _, err := store.db.Exec(`UPDATE l1_chatgpt_storagehost_operation_receipt SET result_json=?, result_sha256=?, effect_json=?, effect_sha256=?, proof_sha256=? WHERE op_id=?`, string(resultJSON), resultSHA, string(effectJSON), effectSHA, proof, identity.OpID); err != nil {
		t.Fatal(err)
	}
	state, _, err := store.LookupChatGPTStorageHostOperationReceipt(ctx, identity, mutation)
	if err == nil || state != ChatGPTStorageHostReceiptPending {
		t.Fatalf("substituted proof state=%v err=%v, want pending/unknown", state, err)
	}
	var events int
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_chatgpt_import_event WHERE request_id=?`, input.RequestID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("conflict/substitution duplicated canonical event: %d", events)
	}
}

func chatGPTStorageHostExecuteAndRecover(t *testing.T, path string, store *L1SQLiteStore, ctx context.Context, operation ChatGPTStorageHostOperation, mutation ChatGPTStorageHostMutation) {
	t.Helper()
	identity := chatGPTStorageHostTestIdentity(t, "storagehost-"+string(operation), operation, string(operation))
	want, err := store.ExecuteChatGPTStorageHostOperation(ctx, identity, mutation)
	if err != nil {
		t.Fatalf("execute %s: %v", operation, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewL1SQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, got, err := reopened.LookupChatGPTStorageHostOperationReceipt(ctx, identity, mutation)
	if err != nil {
		t.Fatalf("lookup %s after reopen: %v", operation, err)
	}
	if state != ChatGPTStorageHostReceiptCommitted || !chatGPTStorageHostResultsEqual(want, got) {
		t.Fatalf("recovered %s state=%v got=%+v want=%+v", operation, state, got, want)
	}
	var receipts int
	if err := reopened.db.QueryRow(`SELECT count(*) FROM l1_chatgpt_storagehost_operation_receipt WHERE op_id=?`, identity.OpID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 {
		t.Fatalf("%s receipt count=%d, want 1", operation, receipts)
	}
}

func chatGPTStorageHostTestStore(t *testing.T) (string, *L1SQLiteStore) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "l1.db")
	store, err := NewL1SQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, store
}

func chatGPTStorageHostTestIdentity(t *testing.T, opID string, operation ChatGPTStorageHostOperation, seed string) ChatGPTStorageHostOperationIdentity {
	t.Helper()
	identity, err := NewChatGPTStorageHostOperationIdentity(opID, operation, chatGPTStorageHostTestHash(seed), 7)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func chatGPTStorageHostTestHash(seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(digest[:])
}
