package l1sqlite

import (
	"context"
	"path/filepath"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

func TestL1SQLiteStoreOwnerProposeBindsOperatorEvidenceAndReceipt(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "owner.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	result, err := store.OwnerProposeUserMemory(ctx, "owner-request-1", "ren", "ren", domainmemory.UserMemoryTypePreference, "short answers", "operator confirmed")
	if err != nil {
		t.Fatalf("owner propose failed: %v", err)
	}
	if result.Item.State != domainmemory.MemoryStateCandidate || result.Item.Confidence != 0.5 || result.Item.Sensitivity != "normal" || result.Item.PersonaScope != "all_personas" || len(result.Item.EvidenceEventIDs) != 1 || result.Receipt.Status != "completed" || result.Receipt.IdempotencyKey != "owner-request-1" {
		t.Fatalf("owner propose result=%+v", result)
	}
	if got := countL1Rows(t, ctx, store, `SELECT count(*) FROM l1_memory_event WHERE id = ?`, result.Item.ID); got != 1 {
		t.Fatalf("candidate rows=%d", got)
	}
	if got := countL1Rows(t, ctx, store, `SELECT count(*) FROM l1_memory_event WHERE id = ?`, result.Item.EvidenceEventIDs[0]); got != 1 {
		t.Fatalf("operator evidence rows=%d", got)
	}
	if got := countL1Rows(t, ctx, store, `SELECT count(*) FROM l1_event_log WHERE namespace = ?`, "user:ren"); got != 1 {
		t.Fatalf("owner audit events=%d want=1", got)
	}
	if got := countL1Rows(t, ctx, store, `SELECT count(*) FROM l1_memory_owner_receipt WHERE request_id = ?`, "owner-request-1"); got != 1 {
		t.Fatalf("owner receipts=%d", got)
	}
	items, err := store.OwnerListUserMemories(ctx, "ren", "", false, 20)
	if err != nil || len(items) != 1 || items[0].ID != result.Item.ID || items[0].Type != domainmemory.UserMemoryTypePreference {
		t.Fatalf("owner list leaked evidence or omitted candidate: items=%+v err=%v", items, err)
	}
}

func TestL1SQLiteStoreOwnerProposeReplaysAndConflictsByRequestID(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "owner-replay.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	first, err := store.OwnerProposeUserMemory(ctx, "owner-request-2", "ren", "ren", domainmemory.UserMemoryTypePreference, "short answers", "operator confirmed")
	if err != nil {
		t.Fatalf("first owner propose failed: %v", err)
	}
	replay, err := store.OwnerProposeUserMemory(ctx, "owner-request-2", "ren", "ren", domainmemory.UserMemoryTypePreference, "short answers", "operator confirmed")
	if err != nil || !replay.Receipt.IdempotentReplay || replay.Item.ID != first.Item.ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if _, err := store.OwnerProposeUserMemory(ctx, "owner-request-2", "ren", "ren", domainmemory.UserMemoryTypePreference, "different statement", "operator confirmed"); err == nil {
		t.Fatal("same request with different payload must conflict")
	}
	if got := countL1Rows(t, ctx, store, `SELECT count(*) FROM l1_memory_event WHERE namespace = ?`, "user:ren"); got != 2 {
		t.Fatalf("conflicting request mutated rows=%d", got)
	}
}

func TestL1SQLiteStoreOwnerTransitionsEnforceOwnerAndState(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "owner-transition.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	proposed, err := store.OwnerProposeUserMemory(ctx, "owner-request-3", "ren", "ren", domainmemory.UserMemoryTypePreference, "short answers", "operator confirmed")
	if err != nil {
		t.Fatalf("owner propose failed: %v", err)
	}
	confirmed, err := store.OwnerTransitionUserMemory(ctx, "owner-request-4", "ren", "ren", proposed.Item.ID, domainmemory.UserMemoryOwnerOperationConfirm, "", "reviewed")
	if err != nil || confirmed.Item.State != domainmemory.MemoryStateConfirmed || confirmed.Receipt.AuditReference != proposed.Item.ID {
		t.Fatalf("confirm=%+v err=%v", confirmed, err)
	}
	pinned, err := store.OwnerTransitionUserMemory(ctx, "owner-request-5", "ren", "ren", proposed.Item.ID, domainmemory.UserMemoryOwnerOperationPin, "", "keep fixed")
	if err != nil || pinned.Item.State != domainmemory.MemoryStatePinned {
		t.Fatalf("pin=%+v err=%v", pinned, err)
	}
	if _, err := store.OwnerTransitionUserMemory(ctx, "owner-request-6", "other-user", "other-user", proposed.Item.ID, domainmemory.UserMemoryOwnerOperationForget, "", "not mine"); err == nil {
		t.Fatal("other owner must not mutate memory")
	}
	if _, err := store.OwnerTransitionUserMemory(ctx, "owner-request-7", "ren", "ren", proposed.Item.ID, domainmemory.UserMemoryOwnerOperationConfirm, "", "invalid repeat"); err == nil {
		t.Fatal("invalid state transition must fail")
	}
}

func TestL1SQLiteStoreOwnerListAppliesActiveFilterBeforeLimit(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "owner-list-limit.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	older, err := store.OwnerProposeUserMemory(ctx, "owner-list-older", "ren", "ren", domainmemory.UserMemoryTypePreference, "older active item", "operator confirmed")
	if err != nil {
		t.Fatalf("older owner propose failed: %v", err)
	}
	newer, err := store.OwnerProposeUserMemory(ctx, "owner-list-newer", "ren", "ren", domainmemory.UserMemoryTypeProject, "newer inactive item", "operator confirmed")
	if err != nil {
		t.Fatalf("newer owner propose failed: %v", err)
	}
	if _, err := store.OwnerTransitionUserMemory(ctx, "owner-list-forget", "ren", "ren", newer.Item.ID, domainmemory.UserMemoryOwnerOperationForget, "", "remove newer item"); err != nil {
		t.Fatalf("forget newer item failed: %v", err)
	}

	items, err := store.OwnerListUserMemories(ctx, "ren", "", false, 1)
	if err != nil {
		t.Fatalf("owner list failed: %v", err)
	}
	if len(items) != 1 || items[0].ID != older.Item.ID {
		t.Fatalf("active list after limit=%+v, want older active item %q", items, older.Item.ID)
	}
}
