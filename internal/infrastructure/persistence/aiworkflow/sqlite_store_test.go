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
)

func TestSQLiteStoreSaveAndListAIWorkflowRecords(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "ai_workflow.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	if err := store.SaveWorkflowEvent(ctx, domainai.WorkflowEvent{EventID: "evt_1", RunID: "run_1", WorkstreamID: "ws_1", EventType: "project_init_started", Status: "completed", CreatedAt: now}); err != nil {
		t.Fatalf("SaveWorkflowEvent() error = %v", err)
	}
	if err := store.SaveProjectMemoryIndex(ctx, domainai.ProjectMemoryIndex{ID: "mem_1", Repo: "repo", FilePath: ".ai/PROJECT_MEMORY.md", MemoryType: "project", UpdatedAt: now}); err != nil {
		t.Fatalf("SaveProjectMemoryIndex() error = %v", err)
	}
	if err := store.SaveWorktreeRegistry(ctx, domainai.WorktreeRegistry{WorktreeID: "wt_1", Repo: "repo", Path: "../worktrees/repo-feature", Branch: "feature/a", Status: "active", CreatedAt: now}); err != nil {
		t.Fatalf("SaveWorktreeRegistry() error = %v", err)
	}
	if err := store.SaveCommandRegistry(ctx, domainai.CommandRegistry{CommandName: "/review-architecture", FilePath: "commands/review-architecture.md", UpdatedAt: now}); err != nil {
		t.Fatalf("SaveCommandRegistry() error = %v", err)
	}
	if err := store.SaveContextUsage(ctx, domainai.ContextUsage{EventID: "ctx_1", SessionID: "session_1", RunID: "run_1", WorkstreamID: "ws_1", JobID: "job_1", CompactionID: "compact_1", Agent: "Coder", InputTokens: 1, CreatedAt: now}); err != nil {
		t.Fatalf("SaveContextUsage() error = %v", err)
	}
	if items, err := store.ListWorkflowEvents(ctx, 10); err != nil || len(items) != 1 || items[0].EventID != "evt_1" || items[0].RunID != "run_1" || items[0].WorkstreamID != "ws_1" {
		t.Fatalf("events=%#v err=%v", items, err)
	}
	if items, err := store.ListProjectMemoryIndexes(ctx, 10); err != nil || len(items) != 1 || items[0].ID != "mem_1" {
		t.Fatalf("memories=%#v err=%v", items, err)
	}
	if items, err := store.ListWorktreeRegistries(ctx, 10); err != nil || len(items) != 1 || items[0].WorktreeID != "wt_1" {
		t.Fatalf("worktrees=%#v err=%v", items, err)
	}
	if items, err := store.ListCommandRegistries(ctx, 10); err != nil || len(items) != 1 || items[0].CommandName != "/review-architecture" {
		t.Fatalf("commands=%#v err=%v", items, err)
	}
	if items, err := store.ListContextUsages(ctx, 10); err != nil || len(items) != 1 || items[0].EventID != "ctx_1" || items[0].JobID != "job_1" || items[0].RunID != "run_1" || items[0].WorkstreamID != "ws_1" || items[0].CompactionID != "compact_1" {
		t.Fatalf("contexts=%#v err=%v", items, err)
	}
}

func TestSQLiteStoreFindWorkflowEventByIDUsesPrimaryKeyAndRejectsMalformedPayload(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "ai_workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := domainai.WorkflowEvent{EventID: "evt_1", EventType: "started", Status: "running", CreatedAt: now}
	completed := started
	completed.Status = "completed"
	completed.CompletedAt = now.Add(time.Minute)
	if err := store.SaveWorkflowEvent(ctx, started); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorkflowEvent(ctx, completed); err != nil {
		t.Fatal(err)
	}
	item, found, err := store.FindWorkflowEventByID(ctx, "evt_1")
	if err != nil || !found || item.Status != "completed" {
		t.Fatalf("item=%#v found=%v err=%v", item, found, err)
	}
	missing, found, err := store.FindWorkflowEventByID(ctx, "missing")
	if err != nil || found || missing.EventID != "" {
		t.Fatalf("missing=%#v found=%v err=%v", missing, found, err)
	}

	prefixStore, err := NewSQLiteStore(filepath.Join(t.TempDir(), "ai_workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer prefixStore.Close()
	if err := prefixStore.SaveWorkflowEvent(ctx, domainai.WorkflowEvent{EventID: "evt_10", EventType: "started", Status: "running", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	item, found, err = prefixStore.FindWorkflowEventByID(ctx, "evt_1")
	if err != nil || found || item.EventID != "" {
		t.Fatalf("prefix match item=%#v found=%v err=%v", item, found, err)
	}

	if _, err := store.db.ExecContext(ctx, "UPDATE ai_workflow_event SET payload = ? WHERE event_id = ?", "{", "evt_1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.FindWorkflowEventByID(ctx, "evt_1"); err == nil {
		t.Fatal("expected malformed payload error")
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

func TestSQLiteStoreConcurrentContextUsageAndWorkflowEventSaves(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "ai_workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const writers = 8
	const iterations = 20
	ctx := context.Background()
	errs := make(chan error, writers*iterations*2)
	var wg sync.WaitGroup
	for writer := 0; writer < writers; writer++ {
		for iteration := 0; iteration < iterations; iteration++ {
			writer, iteration := writer, iteration
			wg.Add(2)
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
			go func() {
				defer wg.Done()
				if err := store.SaveWorkflowEvent(ctx, domainai.WorkflowEvent{
					EventID:   "evt-stress-" + testSQLiteStoreIndex(writer, iteration),
					EventType: "context_usage_recorded",
					Status:    "completed",
					CreatedAt: time.Date(2026, 8, 14, 7, 0, 0, 0, time.UTC),
				}); err != nil {
					errs <- err
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent save error: %v", err)
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
	err = store.SaveWorkflowEvent(ctx, domainai.WorkflowEvent{
		EventID:   "busy-timeout",
		EventType: "busy_test",
		Status:    "running",
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
