package knowledgememory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestCreativeCandidateStorageHostReceiptRecoversExactResultAfterReopen(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "knowledge-memory.db")
	store, err := NewSQLiteStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	item, receipt := storageHostCandidateFixture()
	identity := KnowledgeMemoryStorageHostOperationIdentity{OpID: "knowledge-memory-op-1", PayloadSHA256: strings.Repeat("a", 64), WriterGeneration: 7}
	result, err := store.SaveCreativeCandidateForStorageHostOperation(ctx, identity, item, receipt)
	if err != nil {
		_ = store.Close()
		t.Fatalf("SaveCreativeCandidateForStorageHostOperation: %v", err)
	}
	if result.Replayed {
		_ = store.Close()
		t.Fatal("first result unexpectedly reported replay")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = NewSQLiteStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	recovered, found, err := store.LookupCreativeCandidateStorageHostOperationReceipt(ctx, identity, item, receipt)
	if err != nil || !found || recovered != result {
		t.Fatalf("LookupCreativeCandidateStorageHostOperationReceipt = %+v, %v, %v; want %+v, true, nil", recovered, found, err, result)
	}
	got, found, err := store.FindCreativeCandidateByID(ctx, item.UserID, item.ItemID)
	if err != nil || !found || !creativeCandidateEqual(got, item) {
		t.Fatalf("candidate = %+v, %v, %v", got, found, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM creative_knowledge WHERE item_id = ?`, item.ItemID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("candidate row count = %d, %v", count, err)
	}
}

func TestCreativeCandidateStorageHostReceiptRejectsSubstitutedExactResult(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "knowledge-memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	item, receipt := storageHostCandidateFixture()
	identity := KnowledgeMemoryStorageHostOperationIdentity{OpID: "knowledge-memory-op-result-substitution", PayloadSHA256: strings.Repeat("b", 64), WriterGeneration: 11}
	if _, err := store.SaveCreativeCandidateForStorageHostOperation(ctx, identity, item, receipt); err != nil {
		t.Fatal(err)
	}
	raw, hash, err := encodeKnowledgeMemoryStorageHostResult(CreativeCandidateStorageHostResult{Replayed: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE knowledge_memory_storagehost_receipts SET result_json = ?, result_sha256 = ? WHERE op_id = ?`, string(raw), hash, identity.OpID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LookupCreativeCandidateStorageHostOperationReceipt(ctx, identity, item, receipt); err == nil || found {
		t.Fatalf("substituted exact result was accepted: found=%v err=%v", found, err)
	}
}

func TestCreativeCandidateStorageHostReceiptRejectsChangedIdentityAndOwner(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "knowledge-memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	item, receipt := storageHostCandidateFixture()
	identity := KnowledgeMemoryStorageHostOperationIdentity{OpID: "knowledge-memory-op-conflict", PayloadSHA256: strings.Repeat("c", 64), WriterGeneration: 13}
	if _, err := store.SaveCreativeCandidateForStorageHostOperation(ctx, identity, item, receipt); err != nil {
		t.Fatal(err)
	}
	changedIdentity := identity
	changedIdentity.PayloadSHA256 = strings.Repeat("d", 64)
	if _, err := store.SaveCreativeCandidateForStorageHostOperation(ctx, changedIdentity, item, receipt); !errors.Is(err, ErrKnowledgeMemoryRequestConflict) {
		t.Fatalf("changed payload error = %v, want conflict", err)
	}
	changedOwner := item
	changedOwner.UserID = "other-user"
	changedReceipt := receipt
	changedReceipt.UserID = changedOwner.UserID
	if _, err := store.SaveCreativeCandidateForStorageHostOperation(ctx, identity, changedOwner, changedReceipt); !errors.Is(err, ErrKnowledgeMemoryRequestConflict) {
		t.Fatalf("changed owner error = %v, want conflict", err)
	}
	changedGeneration := identity
	changedGeneration.WriterGeneration++
	if _, found, err := store.LookupCreativeCandidateStorageHostOperationReceipt(ctx, changedGeneration, item, receipt); !errors.Is(err, ErrKnowledgeMemoryRequestConflict) || found {
		t.Fatalf("changed writer generation = found %v, err %v; want conflict", found, err)
	}
}

func TestCreativeCandidateStorageHostReceiptMissingOrAlteredProofIsUncertain(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name   string
		tamper func(*testing.T, *SQLiteStore, KnowledgeMemoryStorageHostOperationIdentity, domainkm.CreativeKnowledgeItem)
	}{
		{name: "missing operation proof", tamper: func(t *testing.T, store *SQLiteStore, identity KnowledgeMemoryStorageHostOperationIdentity, _ domainkm.CreativeKnowledgeItem) {
			if _, err := store.db.Exec(`DELETE FROM knowledge_memory_storagehost_receipts WHERE op_id = ?`, identity.OpID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "altered canonical candidate", tamper: func(t *testing.T, store *SQLiteStore, _ KnowledgeMemoryStorageHostOperationIdentity, item domainkm.CreativeKnowledgeItem) {
			if _, err := store.db.Exec(`UPDATE creative_knowledge SET payload = ? WHERE item_id = ?`, `{"item_id":"wrong"}`, item.ItemID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "knowledge-memory.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			item, receipt := storageHostCandidateFixture()
			identity := KnowledgeMemoryStorageHostOperationIdentity{OpID: "knowledge-memory-op-proof", PayloadSHA256: strings.Repeat("e", 64), WriterGeneration: 17}
			if _, err := store.SaveCreativeCandidateForStorageHostOperation(ctx, identity, item, receipt); err != nil {
				t.Fatal(err)
			}
			test.tamper(t, store, identity, item)
			if _, found, err := store.LookupCreativeCandidateStorageHostOperationReceipt(ctx, identity, item, receipt); err == nil || found {
				t.Fatalf("tampered proof accepted: found=%v err=%v", found, err)
			}
		})
	}
}

func TestCanonicalSaveStorageHostReceiptRejectsMissingAlteredOrSubstitutedProof(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 18, 0, 0, 0, time.UTC)
	item := domainkm.PersonalArchiveEntry{EntryID: "pa-storage-host-proof", UserID: "user-proof", OriginalText: "protected", Protected: true, CreatedAt: now}
	mutation := KnowledgeMemoryStorageHostSave{Kind: KnowledgeMemorySavePersonalArchive, PersonalArchive: &item}
	for _, test := range []struct {
		name   string
		tamper func(*testing.T, *SQLiteStore, KnowledgeMemoryStorageHostOperationIdentity)
	}{
		{name: "missing operation proof", tamper: func(t *testing.T, store *SQLiteStore, identity KnowledgeMemoryStorageHostOperationIdentity) {
			if _, err := store.db.Exec(`DELETE FROM knowledge_memory_storagehost_save_receipts WHERE op_id = ?`, identity.OpID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "altered canonical effect", tamper: func(t *testing.T, store *SQLiteStore, _ KnowledgeMemoryStorageHostOperationIdentity) {
			if _, err := store.db.Exec(`UPDATE personal_archive SET payload = ? WHERE entry_id = ?`, `{"entry_id":"wrong"}`, item.EntryID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "substituted result and hash", tamper: func(t *testing.T, store *SQLiteStore, identity KnowledgeMemoryStorageHostOperationIdentity) {
			raw := []byte(`{}`)
			hash := sha256.Sum256(raw)
			if _, err := store.db.Exec(`UPDATE knowledge_memory_storagehost_save_receipts SET result_json = ?, result_sha256 = ? WHERE op_id = ?`, string(raw), hex.EncodeToString(hash[:]), identity.OpID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "knowledge-memory.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			identity := KnowledgeMemoryStorageHostOperationIdentity{OpID: "knowledge-memory-save-proof", PayloadSHA256: strings.Repeat("f", 64), WriterGeneration: 19}
			if err := store.SaveKnowledgeMemoryForStorageHostOperation(ctx, identity, mutation); err != nil {
				t.Fatal(err)
			}
			test.tamper(t, store, identity)
			if found, err := store.LookupKnowledgeMemoryStorageHostOperationReceipt(ctx, identity, mutation); err == nil || found {
				t.Fatalf("tampered canonical save proof accepted: found=%v err=%v", found, err)
			}
		})
	}
}

func storageHostCandidateFixture() (domainkm.CreativeKnowledgeItem, KnowledgeMemoryRequestReceipt) {
	now := time.Date(2026, 10, 3, 14, 0, 0, 0, time.UTC)
	item := domainkm.CreativeKnowledgeItem{
		ItemID: "candidate-storage-host-1", UserID: "user-storage-host", Title: "Private idea",
		Status: "candidate", Visibility: "private", CreatedAt: now,
	}
	return item, KnowledgeMemoryRequestReceipt{
		ActionID: modulecore.ActionID("act_00000000-0000-5000-8000-000000000061"), UserID: item.UserID,
		ActorID: "shiro", PayloadHash: "sha256:private-idea", ItemID: item.ItemID, CreatedAt: now.Add(time.Second),
	}
}
