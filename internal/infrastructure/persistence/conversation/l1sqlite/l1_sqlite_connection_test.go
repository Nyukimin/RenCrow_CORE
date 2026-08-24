package l1sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

func TestL1SQLiteStoreConnectionPolicyPreservesWAL(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
	if got := store.readDB.Stats().MaxOpenConnections; got != l1ReadPoolSize {
		t.Fatalf("read MaxOpenConnections = %d, want %d", got, l1ReadPoolSize)
	}
	if got := store.progressDB.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("progress MaxOpenConnections = %d, want 1", got)
	}
	var queryOnly int
	if err := store.readDB.QueryRowContext(context.Background(), "PRAGMA query_only").Scan(&queryOnly); err != nil {
		t.Fatalf("query read connection query_only: %v", err)
	}
	if queryOnly != 1 {
		t.Fatalf("read connection query_only = %d, want 1", queryOnly)
	}
	if _, err := store.readDB.ExecContext(context.Background(), `UPDATE l1_profile_promotion_job SET updated_at = updated_at`); err == nil {
		t.Fatal("read connection accepted a write")
	}
	if _, err := store.progressDB.ExecContext(context.Background(), `UPDATE l1_profile_promotion_job SET updated_at = updated_at`); err == nil {
		t.Fatal("progress connection accepted a write")
	}
	var busyTimeout int
	if err := store.db.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busyTimeout)
	}
	var journalMode string
	if err := store.db.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("journal_mode = %q, want WAL", journalMode)
	}
}

func TestL1SQLiteStoreConcurrentWritesDoNotReturnBusy(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	const writes = 12
	ctx := context.Background()
	errs := make(chan error, writes)
	var wg sync.WaitGroup
	wg.Add(writes)
	for i := 0; i < writes; i++ {
		go func(i int) {
			defer wg.Done()
			sessionID := fmt.Sprintf("busy-session-%d", i)
			err := store.SaveMessage(ctx, sessionID, int64(i+1), fmt.Sprintf("conv:%d", i+1),
				domconv.NewMessage(domconv.SpeakerMio, fmt.Sprintf("message-%d", i), nil), MemoryStateObserved)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent L1 write failed: %v", err)
		}
	}
}
