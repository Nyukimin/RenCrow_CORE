package aiworkflow

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domainai "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/aiworkflow"
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
