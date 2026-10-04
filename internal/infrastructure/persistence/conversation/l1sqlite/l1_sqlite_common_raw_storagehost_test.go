package l1sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

func TestCommonRawPendingWithoutEffectCanResumeSameIdentity(t *testing.T) {
	root := t.TempDir()
	store, err := NewL1SQLiteStore(filepath.Join(root, "raw.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	objectRoot := filepath.Join(root, "raw-objects")
	if err := os.MkdirAll(objectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCommonRawSourceRoot(objectRoot); err != nil {
		t.Fatal(err)
	}
	requestID := "raw-pending-no-effect"
	input := commonRawTestInput([]domainmemory.CommonRawRecord{commonRawTestRecord("record-1", []byte("resume after proven no effect"))}, nil)
	payloadHash := sha256.Sum256([]byte("typed common raw request"))
	identity, err := NewCommonRawStorageHostOperationIdentity("raw-pending-op", hex.EncodeToString(payloadHash[:]), 7)
	if err != nil {
		t.Fatal(err)
	}
	ctx := commonRawTestContext(t, requestID)
	state, created, _, err := store.beginCommonRawStorageHostOperation(ctx, identity, requestID, commonRawTestOwner, commonRawTestOwner, input)
	if err != nil || !created || state != CommonRawStorageHostReceiptPending {
		t.Fatalf("reserve state=%v created=%v err=%v", state, created, err)
	}
	state, _, err = store.LookupCommonRawStorageHostOperationReceipt(ctx, identity, requestID, commonRawTestOwner, commonRawTestOwner, input)
	if err != nil || state != CommonRawStorageHostReceiptAbsent {
		t.Fatalf("owner failed to prove pending operation had no effect: state=%v err=%v", state, err)
	}

	receipt, err := store.IntakeCommonRawStorageHost(ctx, identity, requestID, commonRawTestOwner, commonRawTestOwner, input)
	if err != nil {
		t.Fatalf("resume after exact no-effect proof: %v", err)
	}
	state, stored, err := store.LookupCommonRawStorageHostOperationReceipt(ctx, identity, requestID, commonRawTestOwner, commonRawTestOwner, input)
	if err != nil || state != CommonRawStorageHostReceiptCommitted || receipt.ManifestID != stored.ManifestID || receipt.IdempotentReplay || stored.IdempotentReplay {
		t.Fatalf("resumed result/state do not match: state=%v receipt=%+v stored=%+v err=%v", state, receipt, stored, err)
	}
	var count int
	if err := store.db.QueryRowContext(context.Background(), `SELECT count(*) FROM l1_raw_source_manifest`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("manifest count=%d err=%v", count, err)
	}
}

func TestCommonRawPendingWithObjectOnlyDoesNotReexecute(t *testing.T) {
	root := t.TempDir()
	store, err := NewL1SQLiteStore(filepath.Join(root, "raw.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	objectRoot := filepath.Join(root, "raw-objects")
	if err := os.MkdirAll(objectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCommonRawSourceRoot(objectRoot); err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("partial object boundary "), 4_000)
	input := commonRawTestInput([]domainmemory.CommonRawRecord{commonRawTestRecord("record-1", content)}, nil)
	requestID := "raw-pending-object-only"
	payloadHash := sha256.Sum256([]byte("typed pending object request"))
	identity, err := NewCommonRawStorageHostOperationIdentity("raw-pending-object-op", hex.EncodeToString(payloadHash[:]), 8)
	if err != nil {
		t.Fatal(err)
	}
	ctx := commonRawTestContext(t, requestID)
	if _, created, _, err := store.beginCommonRawStorageHostOperation(ctx, identity, requestID, commonRawTestOwner, commonRawTestOwner, input); err != nil || !created {
		t.Fatalf("reserve pending operation: created=%v err=%v", created, err)
	}
	prepared, err := prepareCommonRawIntake(commonRawTestOwner, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := writeCommonRawObject(objectRoot, prepared.records[0].input.Content, prepared.records[0].hash); err != nil {
		t.Fatal(err)
	}
	state, _, err := store.LookupCommonRawStorageHostOperationReceipt(ctx, identity, requestID, commonRawTestOwner, commonRawTestOwner, input)
	if err != nil || state != CommonRawStorageHostReceiptPending {
		t.Fatalf("partial canonical object state should remain unresolved: state=%v err=%v", state, err)
	}
	if _, err := store.IntakeCommonRawStorageHost(ctx, identity, requestID, commonRawTestOwner, commonRawTestOwner, input); domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorUnavailable {
		t.Fatalf("ambiguous object-only attempt must not be retried: err=%v", err)
	}
	var manifests, records int
	if err := store.db.QueryRowContext(context.Background(), `SELECT count(*) FROM l1_raw_source_manifest`).Scan(&manifests); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(context.Background(), `SELECT count(*) FROM l1_raw_record`).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if manifests != 0 || records != 0 {
		t.Fatalf("ambiguous recovery falsely committed data: manifests=%d records=%d", manifests, records)
	}
}

func TestCommonRawPendingWithPartialObjectTempStaysUnknown(t *testing.T) {
	root := t.TempDir()
	store, err := NewL1SQLiteStore(filepath.Join(root, "raw.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	objectRoot := filepath.Join(root, "raw-objects")
	if err := os.MkdirAll(objectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCommonRawSourceRoot(objectRoot); err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("partial raw temp "), 5_000)
	input := commonRawTestInput([]domainmemory.CommonRawRecord{commonRawTestRecord("record-1", content)}, nil)
	requestID := "raw-pending-partial-temp"
	payloadHash := sha256.Sum256([]byte("typed pending partial temp request"))
	identity, err := NewCommonRawStorageHostOperationIdentity("raw-pending-temp-op", hex.EncodeToString(payloadHash[:]), 9)
	if err != nil {
		t.Fatal(err)
	}
	ctx := commonRawTestContext(t, requestID)
	if _, created, _, err := store.beginCommonRawStorageHostOperation(ctx, identity, requestID, commonRawTestOwner, commonRawTestOwner, input); err != nil || !created {
		t.Fatalf("reserve pending operation: created=%v err=%v", created, err)
	}
	prepared, err := prepareCommonRawIntake(commonRawTestOwner, input)
	if err != nil {
		t.Fatal(err)
	}
	// The canonical object and its hash-prefix directory do not exist yet, so
	// derive the generated content-address path directly under this private test
	// root instead of asking the verifier to resolve a missing leaf.
	target := filepath.Join(objectRoot, filepath.FromSlash(objectObjectRef(prepared.records[0].hash)))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	partial, err := os.CreateTemp(filepath.Dir(target), ".common-raw-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := partial.Write([]byte("partial")); err != nil {
		partial.Close()
		t.Fatal(err)
	}
	if err := partial.Close(); err != nil {
		t.Fatal(err)
	}
	state, _, err := store.LookupCommonRawStorageHostOperationReceipt(ctx, identity, requestID, commonRawTestOwner, commonRawTestOwner, input)
	if err != nil || state != CommonRawStorageHostReceiptPending {
		t.Fatalf("partial temp object should remain unresolved: state=%v err=%v", state, err)
	}
	if _, err := store.IntakeCommonRawStorageHost(ctx, identity, requestID, commonRawTestOwner, commonRawTestOwner, input); domainmemory.CommonRawErrorCodeOf(err) != domainmemory.CommonRawErrorUnavailable {
		t.Fatalf("ambiguous partial temp must not be retried: err=%v", err)
	}
}
