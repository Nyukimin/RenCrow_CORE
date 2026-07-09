package aiworkflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
)

type memoryWorktreeStore struct {
	worktrees []domainai.WorktreeRegistry
	events    []domainai.WorkflowEvent
}

func (s *memoryWorktreeStore) SaveWorktreeRegistry(_ context.Context, item domainai.WorktreeRegistry) error {
	s.worktrees = append(s.worktrees, item)
	return nil
}

func (s *memoryWorktreeStore) SaveWorkflowEvent(_ context.Context, item domainai.WorkflowEvent) error {
	s.events = append(s.events, item)
	return nil
}

func TestWorktreeManagerRequiresHumanApproval(t *testing.T) {
	manager := NewWorktreeManager(nil)

	if _, err := manager.Create(context.Background(), WorktreeCreateOptions{
		RepoRoot: "repo",
		Branch:   "feature/test",
	}); err == nil {
		t.Fatal("expected missing human approval to fail")
	}
}

func TestWorktreeManagerRejectsProtectedBranch(t *testing.T) {
	repo := initGitRepo(t)
	manager := NewWorktreeManager(nil)

	if _, err := manager.Create(context.Background(), WorktreeCreateOptions{
		RepoRoot:      repo,
		BaseDir:       filepath.Join(t.TempDir(), "worktrees"),
		Branch:        "main",
		HumanApproved: true,
	}); err == nil {
		t.Fatal("expected protected branch to fail")
	}
}

func TestWorktreeManagerCreatesGitWorktreeAndRegistry(t *testing.T) {
	repo := initGitRepo(t)
	base := filepath.Join(t.TempDir(), "worktrees")
	store := &memoryWorktreeStore{}
	manager := NewWorktreeManager(store)
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	result, err := manager.Create(context.Background(), WorktreeCreateOptions{
		RepoRoot:      repo,
		BaseDir:       base,
		RepoName:      "example",
		Branch:        "feature/project-init",
		Purpose:       "Project Init test",
		OwnerAgent:    "Worker",
		HumanApproved: true,
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if result.Worktree.Status != "active" || result.Worktree.Branch != "feature/project-init" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(store.worktrees) != 1 || len(store.events) != 1 {
		t.Fatalf("store not updated: worktrees=%+v events=%+v", store.worktrees, store.events)
	}
	if _, err := os.Stat(filepath.Join(base, "feature-project-init", ".git")); err != nil {
		t.Fatalf("worktree was not created: %v", err)
	}
}

func TestWorktreeManagerClosesGitWorktreeAndRegistry(t *testing.T) {
	repo := initGitRepo(t)
	base := filepath.Join(t.TempDir(), "worktrees")
	store := &memoryWorktreeStore{}
	manager := NewWorktreeManager(store)
	created, err := manager.Create(context.Background(), WorktreeCreateOptions{
		RepoRoot:      repo,
		BaseDir:       base,
		RepoName:      "example",
		Branch:        "feature/close-me",
		HumanApproved: true,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	closed, err := manager.Close(context.Background(), WorktreeCloseOptions{
		RepoRoot:      repo,
		BaseDir:       base,
		RepoName:      "example",
		WorktreeID:    created.Worktree.WorktreeID,
		WorktreePath:  created.Worktree.Path,
		Branch:        created.Worktree.Branch,
		HumanApproved: true,
	})
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if closed.Worktree.Status != "closed" || closed.Event.EventType != "worktree_closed" {
		t.Fatalf("unexpected close result: %+v", closed)
	}
	if _, err := os.Stat(created.Worktree.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists or unexpected stat error: %v", err)
	}
	if len(store.worktrees) != 2 || store.worktrees[1].Status != "closed" {
		t.Fatalf("store not updated with closed status: %+v", store.worktrees)
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}
