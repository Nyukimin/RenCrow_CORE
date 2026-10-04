package durablestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestSQLiteStoreStorageHostReceiptIsAtomicAndReopenRecoverable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workflow.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	result, receipt := storageHostReceiptFixture("sr-storage-host", "dedupe-storage-host", "User-CaseSensitive")
	identity := durableWorkflowStorageHostIdentity("workflow-op-1", []byte(`{"result":"first"}`), receipt)
	if err := store.SaveWithReceiptForStorageHostOperation(ctx, identity, &result, receipt); err != nil {
		t.Fatalf("SaveWithReceiptForStorageHostOperation: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close first store: %v", err)
	}

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen SQLite store: %v", err)
	}
	defer reopened.Close()
	raw, found, err := reopened.LookupStorageHostOperationReceipt(ctx, identity)
	if err != nil || !found || !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		t.Fatalf("recovered receipt found=%v raw=%s err=%v, want null result", found, raw, err)
	}
	gotReceipt, err := reopened.FindByActionID(ctx, receipt.ActionID)
	if err != nil || gotReceipt == nil || *gotReceipt != receipt {
		t.Fatalf("FindByActionID=%+v err=%v, want %+v", gotReceipt, err, receipt)
	}
	gotResult, err := reopened.FindByRequirementID(ctx, result.Requirement.RequirementID)
	if err != nil || gotResult == nil || gotResult.Requirement.UserScope != "User-CaseSensitive" {
		t.Fatalf("FindByRequirementID=%+v err=%v", gotResult, err)
	}
	// Replaying the exact host identity reads the owner receipt and must not
	// duplicate either canonical row.
	if err := reopened.SaveWithReceiptForStorageHostOperation(ctx, identity, &result, receipt); err != nil {
		t.Fatalf("exact owner replay: %v", err)
	}
	assertDurableWorkflowRowCount(t, reopened, "durable_store_workflow", 1)
	assertDurableWorkflowRowCount(t, reopened, "durable_store_workflow_receipt", 1)
	assertDurableWorkflowRowCount(t, reopened, "durable_store_workflow_storagehost_receipt", 1)
}

func TestSQLiteStoreStorageHostReceiptConflictAndRollback(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "workflow.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	result, receipt := storageHostReceiptFixture("sr-conflict", "dedupe-conflict", "Channel:ChatID")
	identity := durableWorkflowStorageHostIdentity("workflow-op-conflict", []byte("first"), receipt)
	if err := store.SaveWithReceiptForStorageHostOperation(ctx, identity, &result, receipt); err != nil {
		t.Fatalf("initial save: %v", err)
	}
	changed := durableWorkflowStorageHostIdentity(identity.OpID, []byte("different"), receipt)
	if _, found, err := store.LookupStorageHostOperationReceipt(ctx, changed); !errors.Is(err, ErrDurableWorkflowStorageHostOperationConflict) || found {
		t.Fatalf("mismatched lookup found=%v err=%v, want operation conflict", found, err)
	}
	if err := store.SaveWithReceiptForStorageHostOperation(ctx, changed, &result, receipt); !errors.Is(err, ErrDurableWorkflowStorageHostOperationConflict) {
		t.Fatalf("mismatched save error=%v, want operation conflict", err)
	}

	badResult, badReceipt := storageHostReceiptFixture("sr-rollback", "dedupe-rollback", "User-A")
	badReceipt.PayloadHash = "different-request-hash"
	badIdentity := durableWorkflowStorageHostIdentity("workflow-op-rollback", []byte("rollback"), badReceipt)
	if err := store.SaveWithReceiptForStorageHostOperation(ctx, badIdentity, &badResult, badReceipt); err == nil {
		t.Fatal("mismatched workflow receipt must fail")
	}
	if _, found, err := store.LookupStorageHostOperationReceipt(ctx, badIdentity); err != nil || found {
		t.Fatalf("rolled-back host receipt found=%v err=%v, want confirmed absence", found, err)
	}
	if got, err := store.FindByRequirementID(ctx, badResult.Requirement.RequirementID); err != nil || got != nil {
		t.Fatalf("rolled-back workflow=%+v err=%v, want absent", got, err)
	}
}

func TestSQLiteStoreStorageHostReceiptRejectsAlteredWriterGeneration(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "workflow.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	result, receipt := storageHostReceiptFixture("sr-generation", "dedupe-generation", "AuthenticatedUser")
	identity := durableWorkflowStorageHostIdentity("workflow-op-generation", []byte("generation"), receipt)
	identity.WriterGeneration = 41
	if err := store.SaveWithReceiptForStorageHostOperation(ctx, identity, &result, receipt); err != nil {
		t.Fatalf("initial save: %v", err)
	}
	changed := identity
	changed.WriterGeneration = 42
	if _, found, err := store.LookupStorageHostOperationReceipt(ctx, changed); !errors.Is(err, ErrDurableWorkflowStorageHostOperationConflict) || found {
		t.Fatalf("altered generation lookup found=%v err=%v, want operation conflict", found, err)
	}
}

func TestSQLiteStoreMigratesV2ToStorageHostReceiptSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if _, err := store.db.Exec(`DROP TABLE durable_store_workflow_storagehost_receipt`); err != nil {
		t.Fatalf("remove version 3 receipt table: %v", err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatalf("set version 2 fixture: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close version 2 fixture: %v", err)
	}

	migrated, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("migrate version 2 store: %v", err)
	}
	defer migrated.Close()
	var version int
	if err := migrated.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("user_version=%d err=%v, want %d", version, err, schemaVersion)
	}
	assertDurableWorkflowRowCount(t, migrated, "durable_store_workflow_storagehost_receipt", 0)
}

func storageHostReceiptFixture(requirementID, dedupeKey, userScope string) (domain.WorkflowResult, domain.RequestReceipt) {
	now := time.Date(2026, 10, 3, 7, 8, 9, 0, time.UTC)
	actionID := modulecore.ActionID("act_00000000-0000-5000-8000-000000000021")
	result := domain.WorkflowResult{
		Status: domain.StatusCompleted, Lifecycle: domain.LifecycleValidated, CreatedAt: now, UpdatedAt: now,
		Requirement: domain.StorageRequirement{
			RequirementID: requirementID, DedupeKey: dedupeKey, ActionID: actionID,
			RequestedBy: "shiro", UserScope: userScope, RequestedOutcome: domain.OutcomeAssess,
			FactsToStore: []string{"x_bookmark"}, OwnerModule: "RenCrow_CORE",
		},
		Classification: domain.Classification{Class: domain.ClassExistingStore, OwnerModule: "RenCrow_CORE", Status: domain.StatusCompleted, Reason: "existing owner"},
		Reason:         "existing owner",
	}
	receipt := domain.RequestReceipt{
		ActionID: actionID, UserScope: userScope, PayloadHash: domain.HashStorageRequirement(result.Requirement),
		RequirementID: requirementID, CreatedAt: now,
	}
	return result, receipt
}

func durableWorkflowStorageHostIdentity(opID string, payload []byte, receipt domain.RequestReceipt) DurableWorkflowStorageHostOperationIdentity {
	hash := sha256.Sum256(payload)
	return DurableWorkflowStorageHostOperationIdentity{
		OpID: opID, PayloadSHA256: hex.EncodeToString(hash[:]),
		ActionID: receipt.ActionID, RequirementID: receipt.RequirementID, WriterGeneration: 1,
	}
}

func assertDurableWorkflowRowCount(t *testing.T, store *SQLiteStore, table string, want int) {
	t.Helper()
	var got int
	if err := store.db.QueryRow("SELECT count(*) FROM " + table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s rows=%d, want %d", table, got, want)
	}
}
