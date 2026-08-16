package archivesqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

func TestArchiveSQLiteStoreConnectionPolicyPreservesWAL(t *testing.T) {
	store, err := NewArchiveSQLiteStore(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatalf("NewArchiveSQLiteStore failed: %v", err)
	}
	defer store.Close()

	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
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

func TestArchiveSQLiteStoreConcurrentWritesDoNotReturnBusy(t *testing.T) {
	store, err := NewArchiveSQLiteStore(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatalf("NewArchiveSQLiteStore failed: %v", err)
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
			now := time.Date(2026, 8, 14, 0, 0, i, 0, time.UTC)
			summary := &domconv.ThreadSummary{
				ThreadID:  int64(i + 1),
				SessionID: fmt.Sprintf("archive-session-%d", i),
				StartTime: now,
				EndTime:   now.Add(time.Minute),
				Domain:    "parallel",
				Summary:   fmt.Sprintf("summary-%d", i),
				Keywords:  []string{"parallel", "thread", "summary"},
				Roles:     []string{"user"},
			}
			errs <- store.SaveThreadSummaryWithReceipt(ctx, summary, receiptForRoles(summary.Roles))
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent archive write failed: %v", err)
		}
	}
}
