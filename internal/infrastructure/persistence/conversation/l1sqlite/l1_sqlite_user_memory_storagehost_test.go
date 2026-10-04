package l1sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"runtime"
	"testing"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

func TestUserMemoryStorageHostConcurrentSameOperationHasOneExactResult(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(t.TempDir() + "/l1.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.db.SetMaxOpenConns(1)
	identityPayload, err := json.Marshal(domainmemory.CreateUserMemoryInput{
		UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "Concurrent operation",
		EvidenceEventIDs: []string{"event-1"}, Confidence: .8, Sensitivity: "normal", Scope: "all_personas",
	})
	if err != nil {
		t.Fatal(err)
	}
	payloadHash := sha256.Sum256(identityPayload)
	identity, err := NewUserMemoryStorageHostOperationIdentity("same-op-concurrent", "create_user_memory", hex.EncodeToString(payloadHash[:]), 1)
	if err != nil {
		t.Fatal(err)
	}
	input := domainmemory.CreateUserMemoryInput{
		UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "Concurrent operation",
		EvidenceEventIDs: []string{"event-1"}, Confidence: .8, Sensitivity: "normal", Scope: "all_personas",
	}
	const callers = 8
	blocker, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	baselineWaits := store.db.Stats().WaitCount
	start := make(chan struct{})
	type callResult struct {
		item *domainmemory.UserMemory
		err  error
	}
	results := make(chan callResult, callers)
	ready := make(chan struct{}, callers)
	for i := 0; i < callers; i++ {
		go func() {
			ready <- struct{}{}
			<-start
			item, err := store.CreateUserMemoryForStorageHostOperation(ctx, identity, input)
			results <- callResult{item: item, err: err}
		}()
	}
	for i := 0; i < callers; i++ {
		<-ready
	}
	close(start)
	deadline := time.Now().Add(5 * time.Second)
	for store.db.Stats().WaitCount < baselineWaits+callers {
		if time.Now().After(deadline) {
			_ = blocker.Rollback()
			t.Fatalf("queued owner calls=%d, want %d", store.db.Stats().WaitCount-baselineWaits, callers)
		}
		runtime.Gosched()
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	var canonical []byte
	for i := 0; i < callers; i++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent same-op call failed: %v", result.err)
		}
		encoded, err := json.Marshal(result.item)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			canonical = encoded
		} else if !bytes.Equal(encoded, canonical) {
			t.Fatalf("concurrent result=%s, want exact %s", encoded, canonical)
		}
	}
	items, err := store.ListUserMemories(ctx, "ren", "", true, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("owner effects=%d err=%v, want exactly one", len(items), err)
	}
	var auditCount, receiptCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_event_log WHERE event_type='memory.user_created'`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM user_memory_storagehost_operation_receipt WHERE op_id=?`, identity.OpID).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 || receiptCount != 1 {
		t.Fatalf("audit=%d receipt=%d, want one each", auditCount, receiptCount)
	}
}

func TestUserMemoryStorageHostOperationReceiptBindsPayload(t *testing.T) {
	store, err := NewL1SQLiteStore(t.TempDir() + "/l1.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	payload := sha256.Sum256([]byte("one payload"))
	first, err := NewUserMemoryStorageHostOperationIdentity("payload-bound-op", "create_user_memory", hex.EncodeToString(payload[:]), 1)
	if err != nil {
		t.Fatal(err)
	}
	input := domainmemory.CreateUserMemoryInput{UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "Payload binding", EvidenceEventIDs: []string{"event-1"}}
	if _, err := store.CreateUserMemoryForStorageHostOperation(context.Background(), first, input); err != nil {
		t.Fatal(err)
	}
	otherPayload := sha256.Sum256([]byte("different payload"))
	second, err := NewUserMemoryStorageHostOperationIdentity("payload-bound-op", "create_user_memory", hex.EncodeToString(otherPayload[:]), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LookupUserMemoryStorageHostOperationReceipt(context.Background(), second); err != ErrUserMemoryStorageHostOperationConflict {
		t.Fatalf("same op_id/different payload error=%v, want receipt conflict", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_memory_event WHERE message = ?`, input.Statement).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("effect count=%d, want one", count)
	}
}

func TestUserMemoryStorageHostInvalidCreateReleasesAdmissionTransaction(t *testing.T) {
	store, err := NewL1SQLiteStore(t.TempDir() + "/l1.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.db.SetMaxOpenConns(1)
	makeIdentity := func(opID, statement string) UserMemoryStorageHostOperationIdentity {
		hash := sha256.Sum256([]byte(statement))
		identity, err := NewUserMemoryStorageHostOperationIdentity(opID, "create_user_memory", hex.EncodeToString(hash[:]), 1)
		if err != nil {
			t.Fatal(err)
		}
		return identity
	}
	invalid := makeIdentity("invalid-create", " ")
	if _, err := store.CreateUserMemoryForStorageHostOperation(context.Background(), invalid, domainmemory.CreateUserMemoryInput{
		UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: " ",
	}); err == nil {
		t.Fatal("invalid semantic input unexpectedly succeeded")
	}
	var invalidReceipts, invalidEffects int
	if err := store.db.QueryRow(`SELECT count(*) FROM user_memory_storagehost_operation_receipt WHERE op_id = ?`, invalid.OpID).Scan(&invalidReceipts); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM l1_memory_event WHERE message = ' '`).Scan(&invalidEffects); err != nil {
		t.Fatal(err)
	}
	if invalidReceipts != 0 || invalidEffects != 0 {
		t.Fatalf("invalid receipt=%d effect=%d, want none", invalidReceipts, invalidEffects)
	}
	valid := makeIdentity("valid-create-after-invalid", "Valid statement")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	item, err := store.CreateUserMemoryForStorageHostOperation(ctx, valid, domainmemory.CreateUserMemoryInput{
		UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "Valid statement", EvidenceEventIDs: []string{"event-1"},
	})
	if err != nil || item == nil {
		t.Fatalf("valid follow-up item=%+v err=%v; admitted transaction must be released", item, err)
	}
}
