package advisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	advisorDomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
)

func TestAdvisorStorageHostReceiptReopenConflictAndFailClosedProof(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "advisor.db")
	store, err := NewSQLiteStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	item := advisorDomain.AdvisorAdoptionRecord{AdoptionID: "adoption-proof", RunID: "run-proof", AdvisorID: advisorDomain.AdvisorCodex, AdoptedByAgent: "shiro", Adopted: true, Outcome: "success", CreatedAt: time.Date(2026, 10, 4, 2, 0, 0, 0, time.UTC)}
	mutation := StorageHostSave{Kind: StorageHostSaveAdoption, Adoption: &item}
	identity := StorageHostOperationIdentity{OpID: "advisor-proof-op", PayloadSHA256: strings.Repeat("a", 64), WriterGeneration: 23}
	result, err := store.SaveForStorageHostOperation(ctx, identity, mutation)
	if err != nil || result.Replayed {
		t.Fatalf("save=%+v err=%v", result, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	recovered, found, err := store.LookupStorageHostOperationReceipt(ctx, identity, mutation)
	if err != nil || !found || recovered != result {
		t.Fatalf("lookup=%+v found=%v err=%v", recovered, found, err)
	}
	changed := identity
	changed.WriterGeneration++
	if _, found, err := store.LookupStorageHostOperationReceipt(ctx, changed, mutation); !errors.Is(err, ErrAdvisorStorageHostConflict) || found {
		t.Fatalf("generation conflict found=%v err=%v", found, err)
	}

	t.Run("missing proof", func(t *testing.T) {
		if _, err := store.db.Exec(`DELETE FROM advisor_storagehost_receipt WHERE op_id = ?`, identity.OpID); err != nil {
			t.Fatal(err)
		}
		if _, found, err := store.LookupStorageHostOperationReceipt(ctx, identity, mutation); err == nil || found {
			t.Fatalf("missing proof found=%v err=%v", found, err)
		}
	})
}

func TestAdvisorStorageHostReceiptRejectsSubstitutedResult(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "advisor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	item := advisorDomain.AdviceRunRecord{RunID: "run-substitution", RequestedByAgent: "shiro", AdvisorID: advisorDomain.AdvisorCodex, Status: advisorDomain.AdviceStatus(advisorDomain.StatusCompleted), FinishedAt: time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC)}
	mutation := StorageHostSave{Kind: StorageHostSaveAdviceRun, AdviceRun: &item}
	identity := StorageHostOperationIdentity{OpID: "advisor-result-op", PayloadSHA256: strings.Repeat("b", 64), WriterGeneration: 29}
	if _, err := store.SaveForStorageHostOperation(ctx, identity, mutation); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"replayed":true}`)
	hash := sha256.Sum256(raw)
	if _, err := store.db.Exec(`UPDATE advisor_storagehost_receipt SET result_json = ?, result_sha256 = ? WHERE op_id = ?`, string(raw), hex.EncodeToString(hash[:]), identity.OpID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LookupStorageHostOperationReceipt(ctx, identity, mutation); err == nil || found {
		t.Fatalf("substituted proof found=%v err=%v", found, err)
	}
}
