package skillgovernance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
)

func TestNewSQLiteStoreRejectsCompositeReceiptPrimaryKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skill.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE skill_storagehost_receipt (
		op_id TEXT NOT NULL,
		operation TEXT NOT NULL,
		payload_sha256 TEXT NOT NULL,
		writer_generation INTEGER NOT NULL,
		effect_id TEXT NOT NULL,
		effect_sha256 TEXT NOT NULL,
		proof_sha256 TEXT NOT NULL,
		result_json TEXT NOT NULL,
		result_sha256 TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (op_id, operation)
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteStore(path)
	if store != nil {
		_ = store.Close()
		t.Fatal("NewSQLiteStore accepted a composite receipt primary key")
	}
	if err == nil {
		t.Fatal("NewSQLiteStore accepted a composite receipt primary key without error")
	}
}

func TestStorageHostReceiptRecoversExactEffectAfterOwnerReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skill.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	item := skillReceiptManifest()
	identity := skillTestIdentity("reopen", 21)
	mutation := StorageHostSave{Kind: StorageHostSaveManifest, Manifest: &item}
	if err := store.SaveForStorageHostOperation(context.Background(), identity, mutation); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if found, err := store.LookupStorageHostOperationReceipt(context.Background(), identity, mutation); err != nil || !found {
		t.Fatalf("reopened receipt found=%v err=%v", found, err)
	}
	if err := store.SaveForStorageHostOperation(context.Background(), identity, mutation); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	changedGeneration := identity
	changedGeneration.WriterGeneration++
	if err := store.SaveForStorageHostOperation(context.Background(), changedGeneration, mutation); !errors.Is(err, ErrSkillStorageHostConflict) {
		t.Fatalf("changed generation error=%v", err)
	}
	changedPayload := identity
	changedPayload.PayloadSHA256 = skillTestIdentity("changed", 21).PayloadSHA256
	if err := store.SaveForStorageHostOperation(context.Background(), changedPayload, mutation); !errors.Is(err, ErrSkillStorageHostConflict) {
		t.Fatalf("changed payload error=%v", err)
	}
}

func TestStorageHostReceiptMissingCorruptOrSubstitutedProofFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *SQLiteStore, StorageHostOperationIdentity)
	}{
		{"missing", func(t *testing.T, s *SQLiteStore, id StorageHostOperationIdentity) {
			if _, err := s.db.Exec(`DELETE FROM skill_storagehost_receipt WHERE op_id=?`, id.OpID); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt", func(t *testing.T, s *SQLiteStore, id StorageHostOperationIdentity) {
			if _, err := s.db.Exec(`UPDATE skill_storagehost_receipt SET proof_sha256='corrupt' WHERE op_id=?`, id.OpID); err != nil {
				t.Fatal(err)
			}
		}},
		{"substituted", func(t *testing.T, s *SQLiteStore, _ StorageHostOperationIdentity) {
			if _, err := s.db.Exec(`UPDATE skill_registry SET updated_at='2026-10-05T00:00:00Z'`); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "skill.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			item := skillReceiptManifest()
			identity := skillTestIdentity("proof-"+test.name, 5)
			mutation := StorageHostSave{Kind: StorageHostSaveManifest, Manifest: &item}
			if err := store.SaveForStorageHostOperation(context.Background(), identity, mutation); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, store, identity)
			if found, err := store.LookupStorageHostOperationReceipt(context.Background(), identity, mutation); err == nil || found {
				t.Fatalf("proof accepted found=%v err=%v", found, err)
			}
		})
	}
}

func skillReceiptManifest() domainskill.SkillManifest {
	return domainskill.SkillManifest{SkillID: "core.receipt", Name: "Receipt", Scope: domainskill.ScopeCore, Version: "1.0.0", Path: "skills/core/receipt", Enabled: true, UpdatedAt: time.Date(2026, 10, 4, 14, 0, 0, 0, time.UTC)}
}

func skillTestIdentity(seed string, generation int64) StorageHostOperationIdentity {
	h := sha256.Sum256([]byte(seed))
	return StorageHostOperationIdentity{OpID: "skill-" + hex.EncodeToString(h[:8]), PayloadSHA256: hex.EncodeToString(h[:]), WriterGeneration: generation}
}
