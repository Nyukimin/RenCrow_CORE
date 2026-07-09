package aiworkflow

import (
	"context"
	"strconv"
	"testing"
	"time"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
)

func TestJSONLStoreSaveAndListAIWorkflowRecords(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
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

func TestJSONLStoreListsLatestRegistryStatePerID(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 19, 19, 30, 0, 0, time.UTC)
	if err := store.SaveProjectMemoryIndex(ctx, domainai.ProjectMemoryIndex{ID: "mem_1", Repo: "repo", FilePath: ".ai/old.md", MemoryType: "project", UpdatedAt: now}); err != nil {
		t.Fatalf("SaveProjectMemoryIndex() error = %v", err)
	}
	if err := store.SaveProjectMemoryIndex(ctx, domainai.ProjectMemoryIndex{ID: "mem_1", Repo: "repo", FilePath: ".ai/new.md", MemoryType: "project", UpdatedAt: now.Add(time.Minute)}); err != nil {
		t.Fatalf("SaveProjectMemoryIndex() error = %v", err)
	}
	if err := store.SaveWorktreeRegistry(ctx, domainai.WorktreeRegistry{WorktreeID: "wt_1", Repo: "repo", Path: "../worktrees/old", Branch: "feature/old", Status: "active", CreatedAt: now}); err != nil {
		t.Fatalf("SaveWorktreeRegistry() error = %v", err)
	}
	if err := store.SaveWorktreeRegistry(ctx, domainai.WorktreeRegistry{WorktreeID: "wt_1", Repo: "repo", Path: "../worktrees/new", Branch: "feature/new", Status: "closed", CreatedAt: now.Add(time.Minute)}); err != nil {
		t.Fatalf("SaveWorktreeRegistry() error = %v", err)
	}
	if err := store.SaveCommandRegistry(ctx, domainai.CommandRegistry{CommandName: "/tool-harness-check", FilePath: "commands/old.md", UpdatedAt: now}); err != nil {
		t.Fatalf("SaveCommandRegistry() error = %v", err)
	}
	if err := store.SaveCommandRegistry(ctx, domainai.CommandRegistry{CommandName: "/tool-harness-check", FilePath: "commands/new.md", UpdatedAt: now.Add(time.Minute)}); err != nil {
		t.Fatalf("SaveCommandRegistry() error = %v", err)
	}
	memories, err := store.ListProjectMemoryIndexes(ctx, 10)
	if err != nil || len(memories) != 1 || memories[0].FilePath != ".ai/new.md" {
		t.Fatalf("memories=%#v err=%v", memories, err)
	}
	worktrees, err := store.ListWorktreeRegistries(ctx, 10)
	if err != nil || len(worktrees) != 1 || worktrees[0].Path != "../worktrees/new" || worktrees[0].Status != "closed" {
		t.Fatalf("worktrees=%#v err=%v", worktrees, err)
	}
	commands, err := store.ListCommandRegistries(ctx, 10)
	if err != nil || len(commands) != 1 || commands[0].FilePath != "commands/new.md" {
		t.Fatalf("commands=%#v err=%v", commands, err)
	}
}

func TestJSONLStoreCompactsOperationalContextUsage(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	for i := 0; i < contextUsageMaxRecords+3; i++ {
		if err := store.SaveContextUsage(ctx, domainai.ContextUsage{
			EventID:      "ctx_" + strconv.Itoa(i),
			JobID:        "job_compact",
			WorkstreamID: "ws_compact",
			CompactionID: "compact_1",
			Agent:        "Coder",
			InputTokens:  i,
			CreatedAt:    now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("SaveContextUsage(%d) error = %v", i, err)
		}
	}

	if err := store.CompactOperationalLogs(); err != nil {
		t.Fatalf("CompactOperationalLogs() error = %v", err)
	}
	items, err := store.ListContextUsages(ctx, contextUsageMaxRecords+10)
	if err != nil {
		t.Fatalf("ListContextUsages() error = %v", err)
	}
	if len(items) != contextUsageMaxRecords {
		t.Fatalf("context usages len=%d want %d", len(items), contextUsageMaxRecords)
	}
	if items[0].EventID != "ctx_"+strconv.Itoa(contextUsageMaxRecords+2) {
		t.Fatalf("newest context usage=%q", items[0].EventID)
	}
	if items[0].JobID != "job_compact" || items[0].WorkstreamID != "ws_compact" || items[0].CompactionID != "compact_1" {
		t.Fatalf("newest context usage lost continuity keys: %#v", items[0])
	}
}
