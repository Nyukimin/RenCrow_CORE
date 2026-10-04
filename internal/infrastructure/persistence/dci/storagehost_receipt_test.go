package dci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
)

func TestStorageHostReceiptRecoversExactEffectAfterOwnerReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dci.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	result := validSQLiteSearchResult()
	identity := dciTestIdentity("reopen", 17)
	mutation := StorageHostSave{Kind: StorageHostSaveResult, Result: &result, IdempotencyKey: result.Trace.IdempotencyKey}
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
	if err := store.SaveForStorageHostOperation(context.Background(), changedGeneration, mutation); !errors.Is(err, ErrDCIStorageHostConflict) {
		t.Fatalf("changed generation error=%v", err)
	}
	changedPayload := identity
	changedPayload.PayloadSHA256 = dciTestIdentity("other", 17).PayloadSHA256
	if err := store.SaveForStorageHostOperation(context.Background(), changedPayload, mutation); !errors.Is(err, ErrDCIStorageHostConflict) {
		t.Fatalf("changed payload error=%v", err)
	}
}

func TestStorageHostReceiptBindsTraceIdempotencyKeyOutsideDomainJSON(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "dci.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	result := validSQLiteSearchResult()
	trace := result.Trace
	trace.Steps = nil
	trace.FinalEvidenceCount = 0
	identity := dciTestIdentity("trace-key", 3)
	mutation := StorageHostSave{Kind: StorageHostSaveTrace, Trace: &trace, IdempotencyKey: trace.IdempotencyKey}
	if err := store.SaveForStorageHostOperation(context.Background(), identity, mutation); err != nil {
		t.Fatal(err)
	}
	changed := mutation
	changed.IdempotencyKey += "-changed"
	if _, err := store.LookupStorageHostOperationReceipt(context.Background(), identity, changed); !errors.Is(err, ErrDCIStorageHostConflict) {
		t.Fatalf("changed idempotency key error=%v", err)
	}
}

func TestStorageHostReceiptMissingCorruptOrSubstitutedProofFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *SQLiteStore, StorageHostOperationIdentity)
	}{
		{"missing", func(t *testing.T, s *SQLiteStore, id StorageHostOperationIdentity) {
			if _, err := s.db.Exec(`DELETE FROM dci_storagehost_receipt WHERE op_id=?`, id.OpID); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt result proof", func(t *testing.T, s *SQLiteStore, id StorageHostOperationIdentity) {
			if _, err := s.db.Exec(`UPDATE dci_storagehost_receipt SET result_sha256='corrupt' WHERE op_id=?`, id.OpID); err != nil {
				t.Fatal(err)
			}
		}},
		{"substituted effect", func(t *testing.T, s *SQLiteStore, id StorageHostOperationIdentity) {
			if _, err := s.db.Exec(`UPDATE dci_search_trace SET user_query='substituted canonical effect'`); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "dci.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			result := validSQLiteSearchResult()
			identity := dciTestIdentity("proof-"+test.name, 9)
			mutation := StorageHostSave{Kind: StorageHostSaveResult, Result: &result, IdempotencyKey: result.Trace.IdempotencyKey}
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

func dciTestIdentity(seed string, generation int64) StorageHostOperationIdentity {
	h := sha256.Sum256([]byte(seed))
	return StorageHostOperationIdentity{OpID: "dci-" + hex.EncodeToString(h[:8]), PayloadSHA256: hex.EncodeToString(h[:]), WriterGeneration: generation}
}
