package aiworkflow

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestSQLiteStoreSaveAndListAIWorkflowRecords(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "ai_workflow.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	memoryID := modulecore.NewMemoryID()
	if err := store.SaveProjectMemoryIndex(ctx, domainai.ProjectMemoryIndex{MemoryID: memoryID, Repo: "repo", FilePath: ".ai/PROJECT_MEMORY.md", MemoryType: "project", UpdatedAt: now}); err != nil {
		t.Fatalf("SaveProjectMemoryIndex() error = %v", err)
	}
	if err := store.SaveWorktreeRegistry(ctx, domainai.WorktreeRegistry{WorktreeID: "wt_1", Repo: "repo", Path: "../worktrees/repo-feature", Branch: "feature/a", Status: "active", CreatedAt: now}); err != nil {
		t.Fatalf("SaveWorktreeRegistry() error = %v", err)
	}
	if err := store.SaveCommandRegistry(ctx, domainai.CommandRegistry{CommandName: "/review-architecture", FilePath: "commands/review-architecture.md", UpdatedAt: now}); err != nil {
		t.Fatalf("SaveCommandRegistry() error = %v", err)
	}
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	if err := store.SaveContextUsage(ctx, domainai.ContextUsage{EventID: "ctx_1", SessionID: "session_1", TaskID: taskID, RunID: runID, WorkstreamID: "ws_1", CompactionID: "compact_1", Agent: "Coder", InputTokens: 1, CreatedAt: now}); err != nil {
		t.Fatalf("SaveContextUsage() error = %v", err)
	}
	if items, err := store.ListProjectMemoryIndexes(ctx, 10); err != nil || len(items) != 1 || items[0].MemoryID != memoryID {
		t.Fatalf("memories=%#v err=%v", items, err)
	}
	if items, err := store.ListWorktreeRegistries(ctx, 10); err != nil || len(items) != 1 || items[0].WorktreeID != "wt_1" {
		t.Fatalf("worktrees=%#v err=%v", items, err)
	}
	if items, err := store.ListCommandRegistries(ctx, 10); err != nil || len(items) != 1 || items[0].CommandName != "/review-architecture" {
		t.Fatalf("commands=%#v err=%v", items, err)
	}
	if items, err := store.ListContextUsages(ctx, 10); err != nil || len(items) != 1 || items[0].EventID != "ctx_1" || items[0].TaskID != taskID || items[0].RunID != runID || items[0].WorkstreamID != "ws_1" || items[0].CompactionID != "compact_1" {
		t.Fatalf("contexts=%#v err=%v", items, err)
	}
}

func TestSQLiteStoreDoesNotCreateLegacyEventTable(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "ai_workflow.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='ai_workflow_event'`).Scan(&count); err != nil {
		t.Fatalf("inspect schema: %v", err)
	}
	if count != 0 {
		t.Fatal("legacy ai_workflow_event table was created")
	}
}

func TestSQLiteStoreConfiguresSingleConnectionBusyTimeoutAndPreservesWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai_workflow.db")
	seed, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = seed.Close()
		t.Fatalf("seed WAL: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("max open connections=%d want=1", got)
	}
	var busyTimeout int
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy timeout=%d want=5000", busyTimeout)
	}
	var journalMode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Fatalf("journal mode=%q want=wal", journalMode)
	}
}

func TestSQLiteStoreConfiguresWALPragmasForEveryConnection(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "fresh_ai_workflow.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
	store.db.SetMaxIdleConns(0)
	for connection := 0; connection < 2; connection++ {
		var walAutoCheckpoint int
		if err := store.db.QueryRow(`PRAGMA wal_autocheckpoint`).Scan(&walAutoCheckpoint); err != nil {
			t.Fatalf("connection %d wal_autocheckpoint: %v", connection, err)
		}
		if walAutoCheckpoint != 0 {
			t.Fatalf("connection %d wal_autocheckpoint = %d, want 0 to keep checkpoints off the append path", connection, walAutoCheckpoint)
		}
		var journalMode string
		if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
			t.Fatalf("connection %d journal_mode: %v", connection, err)
		}
		if !strings.EqualFold(journalMode, "wal") {
			t.Fatalf("connection %d journal_mode = %q, want wal", connection, journalMode)
		}
		var synchronous int
		if err := store.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
			t.Fatalf("connection %d synchronous: %v", connection, err)
		}
		if synchronous != 1 {
			t.Fatalf("connection %d synchronous = %d, want NORMAL (1)", connection, synchronous)
		}
	}
}

func TestSQLiteStoreConcurrentContextUsageSaves(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "ai_workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const writers = 8
	const iterations = 20
	ctx := context.Background()
	errs := make(chan error, writers*iterations+4)
	var wg sync.WaitGroup
	for writer := 0; writer < writers; writer++ {
		for iteration := 0; iteration < iterations; iteration++ {
			writer, iteration := writer, iteration
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := store.SaveContextUsage(ctx, domainai.ContextUsage{
					EventID:   "ctx-stress-" + testSQLiteStoreIndex(writer, iteration),
					Agent:     "Shiro",
					CreatedAt: time.Date(2026, 8, 14, 7, 0, 0, 0, time.UTC),
				}); err != nil {
					errs <- err
				}
			}()
		}
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.checkpointWorker.Checkpoint(ctx); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent save error: %v", err)
	}
}

func TestSQLiteStoreReopensSavedAIWorkflowDataAfterFinalCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai-workflow-reopen.db")
	first, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	item := domainai.ContextUsage{
		EventID:   "context-reopen",
		SessionID: "session-reopen",
		Agent:     "Shiro",
		Model:     "model-reopen",
		CreatedAt: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC),
	}
	if err := first.SaveContextUsage(context.Background(), item); err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("repeated Close() = %v, want nil", err)
	}
	status := first.CheckpointStatus()
	if status.LastSuccessAt.IsZero() || !status.CheckpointAttempted {
		t.Fatalf("closed checkpoint status = %+v, want final success", status)
	}

	second, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	items, err := second.ListContextUsages(context.Background(), 10)
	if err != nil || len(items) != 1 || items[0].EventID != item.EventID {
		t.Fatalf("reopened context usages=%#v err=%v, want saved context usage", items, err)
	}
}

func TestSQLiteStorePropagatesWriteErrorAfterSQLiteBusyTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai_workflow.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	blocker, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	blocker.SetMaxOpenConns(1)
	defer blocker.Close()
	tx, err := blocker.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("CREATE TABLE busy_lock (id INTEGER)"); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	err = store.SaveContextUsage(ctx, domainai.ContextUsage{
		EventID:   "busy-timeout",
		Agent:     "Shiro",
		CreatedAt: time.Now().UTC(),
	})
	_ = tx.Rollback()
	if err == nil {
		t.Fatal("expected locked write error")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	if !strings.Contains(strings.ToLower(err.Error()), "locked") {
		t.Fatalf("locked write error=%v", err)
	}
}

func testSQLiteStoreIndex(writer, iteration int) string {
	return string(rune('a'+writer)) + "-" + string(rune('a'+iteration))
}
