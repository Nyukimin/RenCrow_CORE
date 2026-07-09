package sandbox

import (
	"context"
	"errors"
	"testing"
	"time"

	aiworkflowapp "github.com/Nyukimin/RenCrow_CORE/internal/application/aiworkflow"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
)

type fakeWorktreeCreator struct {
	createOpts   aiworkflowapp.WorktreeCreateOptions
	createResult aiworkflowapp.WorktreeCreateResult
	createErr    error
	closeOpts    aiworkflowapp.WorktreeCloseOptions
	closeResult  aiworkflowapp.WorktreeCloseResult
	closeErr     error
}

func (f *fakeWorktreeCreator) Create(_ context.Context, opts aiworkflowapp.WorktreeCreateOptions) (aiworkflowapp.WorktreeCreateResult, error) {
	f.createOpts = opts
	if f.createErr != nil {
		return aiworkflowapp.WorktreeCreateResult{}, f.createErr
	}
	return f.createResult, nil
}

func (f *fakeWorktreeCreator) Close(_ context.Context, opts aiworkflowapp.WorktreeCloseOptions) (aiworkflowapp.WorktreeCloseResult, error) {
	f.closeOpts = opts
	if f.closeErr != nil {
		return aiworkflowapp.WorktreeCloseResult{}, f.closeErr
	}
	return f.closeResult, nil
}

type fakeWorktreeSandboxStore struct {
	sandbox domainsandbox.SandboxRecord
	err     error
	called  bool
}

func (s *fakeWorktreeSandboxStore) SaveSandbox(_ context.Context, record domainsandbox.SandboxRecord) error {
	s.called = true
	s.sandbox = record
	return s.err
}

func TestWorktreeSandboxManagerRequiresHumanApproval(t *testing.T) {
	manager := NewWorktreeSandboxManager(&fakeWorktreeCreator{}, &fakeWorktreeSandboxStore{})

	_, err := manager.Create(context.Background(), WorktreeSandboxCreateOptions{
		Branch: "feature/sandbox",
	})
	if err == nil {
		t.Fatal("expected human approval error")
	}
}

func TestWorktreeSandboxManagerCreatesWorktreeAndSandboxRecord(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	creator := &fakeWorktreeCreator{
		createResult: aiworkflowapp.WorktreeCreateResult{
			Worktree: domainai.WorktreeRegistry{
				WorktreeID: "worktree:repo:feature-sandbox",
				Repo:       "repo",
				Path:       "/tmp/worktrees/repo-feature-sandbox",
				Branch:     "feature/sandbox",
				Status:     "active",
				CreatedAt:  now,
			},
		},
	}
	store := &fakeWorktreeSandboxStore{}
	manager := NewWorktreeSandboxManager(creator, store)

	result, err := manager.Create(context.Background(), WorktreeSandboxCreateOptions{
		RepoRoot:      "/repo",
		BaseDir:       "/tmp/worktrees",
		RepoName:      "repo",
		Branch:        "feature/sandbox",
		PathName:      "repo-feature-sandbox",
		Purpose:       "sandbox code change",
		OwnerAgent:    "Worker",
		WorkstreamID:  "ws_1",
		GoalID:        "goal_1",
		HumanApproved: true,
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if !creator.createOpts.HumanApproved || creator.createOpts.Branch != "feature/sandbox" {
		t.Fatalf("worktree opts not forwarded: %#v", creator.createOpts)
	}
	if !store.called {
		t.Fatal("sandbox store was not called")
	}
	if store.sandbox.Type != "code_worktree" || store.sandbox.Path != "/tmp/worktrees/repo-feature-sandbox" {
		t.Fatalf("sandbox record = %#v", store.sandbox)
	}
	if store.sandbox.WorkstreamID != "ws_1" || store.sandbox.GoalID != "goal_1" {
		t.Fatalf("sandbox linkage = %#v", store.sandbox)
	}
	if result.Sandbox.SandboxID != "sandbox:worktree:repo:feature-sandbox" {
		t.Fatalf("sandbox id = %q", result.Sandbox.SandboxID)
	}
}

func TestWorktreeSandboxManagerSurfacesRegistryFailure(t *testing.T) {
	creator := &fakeWorktreeCreator{
		createResult: aiworkflowapp.WorktreeCreateResult{
			Worktree: domainai.WorktreeRegistry{
				WorktreeID: "worktree:repo:feature-sandbox",
				Path:       "/tmp/worktrees/repo-feature-sandbox",
				Branch:     "feature/sandbox",
				CreatedAt:  time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
			},
		},
	}
	store := &fakeWorktreeSandboxStore{err: errors.New("write failed")}
	manager := NewWorktreeSandboxManager(creator, store)

	_, err := manager.Create(context.Background(), WorktreeSandboxCreateOptions{
		Branch:        "feature/sandbox",
		HumanApproved: true,
	})
	if err == nil {
		t.Fatal("expected registry failure")
	}
}

func TestWorktreeSandboxManagerCloseRequiresHumanApproval(t *testing.T) {
	manager := NewWorktreeSandboxManager(&fakeWorktreeCreator{}, &fakeWorktreeSandboxStore{})

	_, err := manager.Close(context.Background(), WorktreeSandboxCloseOptions{
		WorktreePath: "/tmp/worktrees/repo-feature-sandbox",
	})
	if err == nil {
		t.Fatal("expected human approval error")
	}
}

func TestWorktreeSandboxManagerClosesWorktreeAndSandboxRecord(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	creator := &fakeWorktreeCreator{
		closeResult: aiworkflowapp.WorktreeCloseResult{
			Worktree: domainai.WorktreeRegistry{
				WorktreeID: "worktree:repo:feature-sandbox",
				Repo:       "repo",
				Path:       "/tmp/worktrees/repo-feature-sandbox",
				Branch:     "feature/sandbox",
				Status:     "closed",
				CreatedAt:  now,
				ClosedAt:   now,
			},
		},
	}
	store := &fakeWorktreeSandboxStore{}
	manager := NewWorktreeSandboxManager(creator, store)

	result, err := manager.Close(context.Background(), WorktreeSandboxCloseOptions{
		RepoRoot:      "/repo",
		BaseDir:       "/tmp/worktrees",
		RepoName:      "repo",
		WorktreeID:    "worktree:repo:feature-sandbox",
		WorktreePath:  "/tmp/worktrees/repo-feature-sandbox",
		Branch:        "feature/sandbox",
		OwnerAgent:    "Worker",
		SandboxID:     "sandbox:worktree:repo:feature-sandbox",
		WorkstreamID:  "ws_1",
		GoalID:        "goal_1",
		HumanApproved: true,
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if !creator.closeOpts.HumanApproved || creator.closeOpts.WorktreePath != "/tmp/worktrees/repo-feature-sandbox" {
		t.Fatalf("worktree close opts not forwarded: %#v", creator.closeOpts)
	}
	if !store.called {
		t.Fatal("sandbox store was not called")
	}
	if store.sandbox.Status != domainsandbox.SandboxStatusClosed || store.sandbox.ClosedAt.IsZero() {
		t.Fatalf("sandbox close record = %#v", store.sandbox)
	}
	if result.Sandbox.SandboxID != "sandbox:worktree:repo:feature-sandbox" {
		t.Fatalf("sandbox id = %q", result.Sandbox.SandboxID)
	}
}
