package aiworkflow

import (
	"strings"
	"testing"
	"time"
)

func TestValidateAIWorkflowRecords(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 10, 0, 0, time.UTC)
	if err := ValidateWorkflowEvent(WorkflowEvent{EventID: "evt_1", EventType: "project_init_started", Status: "completed", CreatedAt: now}); err != nil {
		t.Fatalf("ValidateWorkflowEvent() error = %v", err)
	}
	if err := ValidateProjectMemoryIndex(ProjectMemoryIndex{ID: "mem_1", Repo: "repo", FilePath: ".ai/PROJECT_MEMORY.md", MemoryType: "project", UpdatedAt: now}); err != nil {
		t.Fatalf("ValidateProjectMemoryIndex() error = %v", err)
	}
	if err := ValidateWorktreeRegistry(WorktreeRegistry{WorktreeID: "wt_1", Repo: "repo", Path: "../worktrees/repo-feature", Branch: "feature/a", Status: "active", CreatedAt: now}); err != nil {
		t.Fatalf("ValidateWorktreeRegistry() error = %v", err)
	}
	if err := ValidateCommandRegistry(CommandRegistry{CommandName: "/review-architecture", FilePath: "commands/review-architecture.md", UpdatedAt: now}); err != nil {
		t.Fatalf("ValidateCommandRegistry() error = %v", err)
	}
	if err := ValidateContextUsage(ContextUsage{EventID: "ctx_1", Agent: "Coder", InputTokens: 1, CreatedAt: now}); err != nil {
		t.Fatalf("ValidateContextUsage() error = %v", err)
	}
}

func TestValidateContextUsageRejectsNegativeCounts(t *testing.T) {
	err := ValidateContextUsage(ContextUsage{EventID: "ctx_1", Agent: "Coder", InputTokens: -1})
	if err == nil {
		t.Fatal("expected negative counts to fail")
	}
}

func TestValidateAIWorkflowRejectsMissingTimestamp(t *testing.T) {
	cases := []struct {
		name string
		err  string
		run  func() error
	}{
		{
			name: "workflow event",
			err:  "created_at",
			run: func() error {
				return ValidateWorkflowEvent(WorkflowEvent{EventID: "evt_1", EventType: "project_init_started", Status: "completed"})
			},
		},
		{
			name: "project memory",
			err:  "updated_at",
			run: func() error {
				return ValidateProjectMemoryIndex(ProjectMemoryIndex{ID: "mem_1", Repo: "repo", FilePath: ".ai/PROJECT_MEMORY.md", MemoryType: "project"})
			},
		},
		{
			name: "worktree",
			err:  "created_at",
			run: func() error {
				return ValidateWorktreeRegistry(WorktreeRegistry{WorktreeID: "wt_1", Repo: "repo", Path: "../worktrees/repo-feature", Branch: "feature/a", Status: "active"})
			},
		},
		{
			name: "command",
			err:  "updated_at",
			run: func() error {
				return ValidateCommandRegistry(CommandRegistry{CommandName: "/review-architecture", FilePath: "commands/review-architecture.md"})
			},
		},
		{
			name: "context usage",
			err:  "created_at",
			run: func() error {
				return ValidateContextUsage(ContextUsage{EventID: "ctx_1", Agent: "Coder", InputTokens: 1})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatalf("expected %s error", tc.err)
			}
			if !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("expected error to contain %q, got %v", tc.err, err)
			}
		})
	}
}

func TestValidateAIWorkflowRejectsMissingRequiredFields(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 10, 0, 0, time.UTC)
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "workflow missing event id", err: ValidateWorkflowEvent(WorkflowEvent{EventType: "project_init_started", Status: "completed", CreatedAt: now}), want: "event_id"},
		{name: "workflow missing event type", err: ValidateWorkflowEvent(WorkflowEvent{EventID: "evt_1", Status: "completed", CreatedAt: now}), want: "event_type"},
		{name: "workflow missing status", err: ValidateWorkflowEvent(WorkflowEvent{EventID: "evt_1", EventType: "project_init_started", CreatedAt: now}), want: "status"},
		{name: "project memory missing id", err: ValidateProjectMemoryIndex(ProjectMemoryIndex{Repo: "repo", FilePath: ".ai/PROJECT_MEMORY.md", MemoryType: "project", UpdatedAt: now}), want: "id"},
		{name: "project memory missing repo", err: ValidateProjectMemoryIndex(ProjectMemoryIndex{ID: "mem_1", FilePath: ".ai/PROJECT_MEMORY.md", MemoryType: "project", UpdatedAt: now}), want: "repo"},
		{name: "project memory missing file path", err: ValidateProjectMemoryIndex(ProjectMemoryIndex{ID: "mem_1", Repo: "repo", MemoryType: "project", UpdatedAt: now}), want: "file_path"},
		{name: "project memory missing type", err: ValidateProjectMemoryIndex(ProjectMemoryIndex{ID: "mem_1", Repo: "repo", FilePath: ".ai/PROJECT_MEMORY.md", UpdatedAt: now}), want: "memory_type"},
		{name: "worktree missing id", err: ValidateWorktreeRegistry(WorktreeRegistry{Repo: "repo", Path: "../worktrees/repo-feature", Branch: "feature/a", Status: "active", CreatedAt: now}), want: "worktree_id"},
		{name: "worktree missing repo", err: ValidateWorktreeRegistry(WorktreeRegistry{WorktreeID: "wt_1", Path: "../worktrees/repo-feature", Branch: "feature/a", Status: "active", CreatedAt: now}), want: "repo"},
		{name: "worktree missing path", err: ValidateWorktreeRegistry(WorktreeRegistry{WorktreeID: "wt_1", Repo: "repo", Branch: "feature/a", Status: "active", CreatedAt: now}), want: "path"},
		{name: "worktree missing branch", err: ValidateWorktreeRegistry(WorktreeRegistry{WorktreeID: "wt_1", Repo: "repo", Path: "../worktrees/repo-feature", Status: "active", CreatedAt: now}), want: "branch"},
		{name: "worktree missing status", err: ValidateWorktreeRegistry(WorktreeRegistry{WorktreeID: "wt_1", Repo: "repo", Path: "../worktrees/repo-feature", Branch: "feature/a", CreatedAt: now}), want: "status"},
		{name: "command missing name", err: ValidateCommandRegistry(CommandRegistry{FilePath: "commands/review-architecture.md", UpdatedAt: now}), want: "command_name"},
		{name: "command missing path", err: ValidateCommandRegistry(CommandRegistry{CommandName: "/review-architecture", UpdatedAt: now}), want: "file_path"},
		{name: "context usage missing event id", err: ValidateContextUsage(ContextUsage{Agent: "Coder", CreatedAt: now}), want: "event_id"},
		{name: "context usage missing agent", err: ValidateContextUsage(ContextUsage{EventID: "ctx_1", CreatedAt: now}), want: "agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil || !strings.Contains(tc.err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", tc.err, tc.want)
			}
		})
	}
}
