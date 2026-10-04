package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
)

func TestSandboxStorageHostReceiptReopenAndCorruptionFailClosed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sandbox.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	item := domainsandbox.PromotionGateLog{EventID: "gate-proof", PromotionID: "promotion-proof", GateStatus: domainsandbox.GateStatusPassed, Reason: "passed", CreatedAt: time.Date(2026, 10, 4, 5, 0, 0, 0, time.UTC)}
	mutation := StorageHostSave{Kind: StorageHostSaveGateLog, GateLog: &item}
	identity := StorageHostOperationIdentity{OpID: "sandbox-proof-op", PayloadSHA256: strings.Repeat("c", 64), WriterGeneration: 31}
	if err := store.SaveForStorageHostOperation(ctx, identity, mutation); err != nil {
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
	if found, err := store.LookupStorageHostOperationReceipt(ctx, identity, mutation); err != nil || !found {
		t.Fatalf("lookup found=%v err=%v", found, err)
	}
	raw := []byte(`{}`)
	h := sha256.Sum256(raw)
	if _, err := store.db.Exec(`UPDATE sandbox_storagehost_receipt SET result_json=?, result_sha256=? WHERE op_id=?`, string(raw), hex.EncodeToString(h[:]), identity.OpID); err != nil {
		t.Fatal(err)
	}
	if found, err := store.LookupStorageHostOperationReceipt(ctx, identity, mutation); err == nil || found {
		t.Fatalf("substitution found=%v err=%v", found, err)
	}
}

func TestSandboxStorageHostMissingProofDoesNotAuthorizePartialSaveRerun(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sandbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	item := domainsandbox.PromotionRequest{PromotionID: "promotion-partial", SandboxID: "sandbox-partial", TargetPath: "target.go", CreatedAt: time.Date(2026, 10, 4, 6, 0, 0, 0, time.UTC)}
	mutation := StorageHostSave{Kind: StorageHostSavePromotion, Promotion: &item}
	identity := StorageHostOperationIdentity{OpID: "sandbox-partial-op", PayloadSHA256: strings.Repeat("d", 64), WriterGeneration: 37}
	if err := store.SaveForStorageHostOperation(ctx, identity, mutation); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM sandbox_storagehost_receipt WHERE op_id=?`, identity.OpID); err != nil {
		t.Fatal(err)
	}
	if found, err := store.LookupStorageHostOperationReceipt(ctx, identity, mutation); err == nil || found {
		t.Fatalf("missing proof found=%v err=%v", found, err)
	}
}
