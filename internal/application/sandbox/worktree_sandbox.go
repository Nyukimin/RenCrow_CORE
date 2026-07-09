package sandbox

import (
	"context"
	"fmt"
	"strings"
	"time"

	aiworkflowapp "github.com/Nyukimin/RenCrow_CORE/internal/application/aiworkflow"
	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
)

type WorktreeCreator interface {
	Create(ctx context.Context, opts aiworkflowapp.WorktreeCreateOptions) (aiworkflowapp.WorktreeCreateResult, error)
}

type WorktreeCloser interface {
	Close(ctx context.Context, opts aiworkflowapp.WorktreeCloseOptions) (aiworkflowapp.WorktreeCloseResult, error)
}

type WorktreeSandboxStore interface {
	SaveSandbox(ctx context.Context, record domainsandbox.SandboxRecord) error
}

type WorktreeSandboxCreateOptions struct {
	RepoRoot      string
	BaseDir       string
	RepoName      string
	Branch        string
	PathName      string
	Purpose       string
	OwnerAgent    string
	WorkstreamID  string
	GoalID        string
	HumanApproved bool
	Now           func() time.Time
}

type WorktreeSandboxCreateResult struct {
	Worktree aiworkflowapp.WorktreeCreateResult `json:"worktree_result"`
	Sandbox  domainsandbox.SandboxRecord        `json:"sandbox"`
}

type WorktreeSandboxCloseOptions struct {
	RepoRoot      string
	BaseDir       string
	RepoName      string
	WorktreeID    string
	WorktreePath  string
	Branch        string
	OwnerAgent    string
	SandboxID     string
	WorkstreamID  string
	GoalID        string
	HumanApproved bool
	Now           func() time.Time
}

type WorktreeSandboxCloseResult struct {
	Worktree aiworkflowapp.WorktreeCloseResult `json:"worktree_result"`
	Sandbox  domainsandbox.SandboxRecord       `json:"sandbox"`
}

type WorktreeSandboxManager struct {
	worktrees interface {
		WorktreeCreator
		WorktreeCloser
	}
	store WorktreeSandboxStore
}

func NewWorktreeSandboxManager(worktrees interface {
	WorktreeCreator
	WorktreeCloser
}, store WorktreeSandboxStore) *WorktreeSandboxManager {
	return &WorktreeSandboxManager{worktrees: worktrees, store: store}
}

func (m *WorktreeSandboxManager) Create(ctx context.Context, opts WorktreeSandboxCreateOptions) (WorktreeSandboxCreateResult, error) {
	if m == nil || m.worktrees == nil {
		return WorktreeSandboxCreateResult{}, fmt.Errorf("worktree manager unavailable")
	}
	if m.store == nil {
		return WorktreeSandboxCreateResult{}, fmt.Errorf("sandbox store unavailable")
	}
	if !opts.HumanApproved {
		return WorktreeSandboxCreateResult{}, fmt.Errorf("human_approved=true is required to create a worktree sandbox")
	}
	worktreeResult, err := m.worktrees.Create(ctx, aiworkflowapp.WorktreeCreateOptions{
		RepoRoot:      opts.RepoRoot,
		BaseDir:       opts.BaseDir,
		RepoName:      opts.RepoName,
		Branch:        opts.Branch,
		PathName:      opts.PathName,
		Purpose:       opts.Purpose,
		OwnerAgent:    opts.OwnerAgent,
		HumanApproved: opts.HumanApproved,
		Now:           opts.Now,
	})
	if err != nil {
		return WorktreeSandboxCreateResult{}, err
	}
	createdAt := worktreeResult.Worktree.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	sandboxID := "sandbox:" + strings.TrimSpace(worktreeResult.Worktree.WorktreeID)
	if sandboxID == "sandbox:" {
		sandboxID = "sandbox:worktree:" + strings.TrimSpace(worktreeResult.Worktree.Branch)
	}
	record := domainsandbox.SandboxRecord{
		SandboxID:    sandboxID,
		WorkstreamID: strings.TrimSpace(opts.WorkstreamID),
		GoalID:       strings.TrimSpace(opts.GoalID),
		Type:         "code_worktree",
		Path:         worktreeResult.Worktree.Path,
		BaseRef:      strings.TrimSpace(opts.Branch),
		CreatedBy:    strings.TrimSpace(opts.OwnerAgent),
		Status:       domainsandbox.SandboxStatusActive,
		CreatedAt:    createdAt,
	}
	if err := m.store.SaveSandbox(ctx, record); err != nil {
		return WorktreeSandboxCreateResult{}, fmt.Errorf("worktree created but sandbox registry save failed: %w", err)
	}
	return WorktreeSandboxCreateResult{
		Worktree: worktreeResult,
		Sandbox:  record,
	}, nil
}

func (m *WorktreeSandboxManager) Close(ctx context.Context, opts WorktreeSandboxCloseOptions) (WorktreeSandboxCloseResult, error) {
	if m == nil || m.worktrees == nil {
		return WorktreeSandboxCloseResult{}, fmt.Errorf("worktree manager unavailable")
	}
	if m.store == nil {
		return WorktreeSandboxCloseResult{}, fmt.Errorf("sandbox store unavailable")
	}
	if !opts.HumanApproved {
		return WorktreeSandboxCloseResult{}, fmt.Errorf("human_approved=true is required to close a worktree sandbox")
	}
	worktreeResult, err := m.worktrees.Close(ctx, aiworkflowapp.WorktreeCloseOptions{
		RepoRoot:      opts.RepoRoot,
		BaseDir:       opts.BaseDir,
		RepoName:      opts.RepoName,
		WorktreeID:    opts.WorktreeID,
		WorktreePath:  opts.WorktreePath,
		Branch:        opts.Branch,
		OwnerAgent:    opts.OwnerAgent,
		HumanApproved: opts.HumanApproved,
		Now:           opts.Now,
	})
	if err != nil {
		return WorktreeSandboxCloseResult{}, err
	}
	closedAt := worktreeResult.Worktree.ClosedAt
	if closedAt.IsZero() {
		closedAt = worktreeResult.Worktree.CreatedAt
	}
	if closedAt.IsZero() {
		closedAt = time.Now().UTC()
	}
	sandboxID := strings.TrimSpace(opts.SandboxID)
	if sandboxID == "" {
		sandboxID = "sandbox:" + strings.TrimSpace(worktreeResult.Worktree.WorktreeID)
	}
	if sandboxID == "sandbox:" {
		sandboxID = "sandbox:worktree:" + strings.TrimSpace(worktreeResult.Worktree.Branch)
	}
	record := domainsandbox.SandboxRecord{
		SandboxID:    sandboxID,
		WorkstreamID: strings.TrimSpace(opts.WorkstreamID),
		GoalID:       strings.TrimSpace(opts.GoalID),
		Type:         "code_worktree",
		Path:         worktreeResult.Worktree.Path,
		BaseRef:      strings.TrimSpace(worktreeResult.Worktree.Branch),
		CreatedBy:    strings.TrimSpace(opts.OwnerAgent),
		Status:       domainsandbox.SandboxStatusClosed,
		CreatedAt:    worktreeResult.Worktree.CreatedAt,
		ClosedAt:     closedAt,
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = closedAt
	}
	if err := m.store.SaveSandbox(ctx, record); err != nil {
		return WorktreeSandboxCloseResult{}, fmt.Errorf("worktree closed but sandbox registry save failed: %w", err)
	}
	return WorktreeSandboxCloseResult{
		Worktree: worktreeResult,
		Sandbox:  record,
	}, nil
}
