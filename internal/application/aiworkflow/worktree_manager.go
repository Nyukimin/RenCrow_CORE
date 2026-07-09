package aiworkflow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
)

type WorktreeStore interface {
	SaveWorktreeRegistry(ctx context.Context, item domainai.WorktreeRegistry) error
	SaveWorkflowEvent(ctx context.Context, item domainai.WorkflowEvent) error
}

type WorktreeCreateOptions struct {
	RepoRoot      string
	BaseDir       string
	RepoName      string
	Branch        string
	PathName      string
	Purpose       string
	OwnerAgent    string
	HumanApproved bool
	Now           func() time.Time
}

type WorktreeCreateResult struct {
	Worktree domainai.WorktreeRegistry `json:"worktree"`
	Event    domainai.WorkflowEvent    `json:"event"`
	Command  string                    `json:"command"`
}

type WorktreeCloseOptions struct {
	RepoRoot      string
	BaseDir       string
	RepoName      string
	WorktreeID    string
	WorktreePath  string
	Branch        string
	OwnerAgent    string
	HumanApproved bool
	Now           func() time.Time
}

type WorktreeCloseResult struct {
	Worktree domainai.WorktreeRegistry `json:"worktree"`
	Event    domainai.WorkflowEvent    `json:"event"`
	Command  string                    `json:"command"`
}

type WorktreeManager struct {
	store WorktreeStore
}

func NewWorktreeManager(store WorktreeStore) *WorktreeManager {
	return &WorktreeManager{store: store}
}

func (m *WorktreeManager) Create(ctx context.Context, opts WorktreeCreateOptions) (WorktreeCreateResult, error) {
	if !opts.HumanApproved {
		return WorktreeCreateResult{}, fmt.Errorf("human_approved=true is required to create a worktree")
	}
	repoRoot := strings.TrimSpace(opts.RepoRoot)
	if repoRoot == "" {
		repoRoot = "."
	}
	absRepo, err := filepath.Abs(repoRoot)
	if err != nil {
		return WorktreeCreateResult{}, err
	}
	if err := ensureGitRepo(absRepo); err != nil {
		return WorktreeCreateResult{}, err
	}
	baseDir := strings.TrimSpace(opts.BaseDir)
	if baseDir == "" {
		baseDir = "../worktrees"
	}
	if !filepath.IsAbs(baseDir) {
		baseDir = filepath.Join(absRepo, baseDir)
	}
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return WorktreeCreateResult{}, err
	}
	if err := rejectWorktreeBase(absBase); err != nil {
		return WorktreeCreateResult{}, err
	}
	branch := strings.TrimSpace(opts.Branch)
	if branch == "" {
		return WorktreeCreateResult{}, fmt.Errorf("branch is required")
	}
	if isProtectedBranch(branch) {
		return WorktreeCreateResult{}, fmt.Errorf("refusing to create worktree for protected branch %q", branch)
	}
	pathName := strings.TrimSpace(opts.PathName)
	if pathName == "" {
		pathName = safePathName(branch)
	}
	if pathName == "" || filepath.IsAbs(pathName) || strings.Contains(pathName, "..") {
		return WorktreeCreateResult{}, fmt.Errorf("path_name must be a safe relative name")
	}
	worktreePath := filepath.Join(absBase, pathName)
	rel, err := filepath.Rel(absBase, worktreePath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return WorktreeCreateResult{}, fmt.Errorf("worktree path must stay under base_dir")
	}
	if _, err := os.Stat(worktreePath); err == nil {
		return WorktreeCreateResult{}, fmt.Errorf("worktree path already exists: %s", worktreePath)
	}
	if err := os.MkdirAll(absBase, 0755); err != nil {
		return WorktreeCreateResult{}, err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", absRepo, "worktree", "add", "-b", branch, worktreePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return WorktreeCreateResult{}, fmt.Errorf("git worktree add failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	at := now().UTC()
	repoName := strings.TrimSpace(opts.RepoName)
	if repoName == "" {
		repoName = filepath.Base(absRepo)
	}
	worktree := domainai.WorktreeRegistry{
		WorktreeID: "worktree:" + repoName + ":" + safePathName(branch),
		Repo:       repoName,
		Path:       worktreePath,
		Branch:     branch,
		Purpose:    strings.TrimSpace(opts.Purpose),
		OwnerAgent: strings.TrimSpace(opts.OwnerAgent),
		Status:     "active",
		CreatedAt:  at,
	}
	event := domainai.WorkflowEvent{
		EventID:    "worktree_created:" + repoName + ":" + safePathName(branch) + ":" + at.Format("20060102T150405Z"),
		EventType:  "worktree_created",
		Agent:      strings.TrimSpace(opts.OwnerAgent),
		Repo:       repoName,
		WorktreeID: worktree.WorktreeID,
		Status:     "completed",
		CreatedAt:  at,
		Summary:    "created git worktree " + worktreePath,
	}
	if event.Agent == "" {
		event.Agent = "Worker"
	}
	if m.store != nil {
		if err := m.store.SaveWorktreeRegistry(ctx, worktree); err != nil {
			return WorktreeCreateResult{}, err
		}
		if err := m.store.SaveWorkflowEvent(ctx, event); err != nil {
			return WorktreeCreateResult{}, err
		}
	}
	return WorktreeCreateResult{
		Worktree: worktree,
		Event:    event,
		Command:  "git -C " + absRepo + " worktree add -b " + branch + " " + worktreePath,
	}, nil
}

func (m *WorktreeManager) Close(ctx context.Context, opts WorktreeCloseOptions) (WorktreeCloseResult, error) {
	if !opts.HumanApproved {
		return WorktreeCloseResult{}, fmt.Errorf("human_approved=true is required to close a worktree")
	}
	repoRoot := strings.TrimSpace(opts.RepoRoot)
	if repoRoot == "" {
		repoRoot = "."
	}
	absRepo, err := filepath.Abs(repoRoot)
	if err != nil {
		return WorktreeCloseResult{}, err
	}
	if err := ensureGitRepo(absRepo); err != nil {
		return WorktreeCloseResult{}, err
	}
	baseDir := strings.TrimSpace(opts.BaseDir)
	if baseDir == "" {
		baseDir = "../worktrees"
	}
	if !filepath.IsAbs(baseDir) {
		baseDir = filepath.Join(absRepo, baseDir)
	}
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return WorktreeCloseResult{}, err
	}
	if err := rejectWorktreeBase(absBase); err != nil {
		return WorktreeCloseResult{}, err
	}
	worktreePath := strings.TrimSpace(opts.WorktreePath)
	if worktreePath == "" {
		return WorktreeCloseResult{}, fmt.Errorf("worktree_path is required")
	}
	if !filepath.IsAbs(worktreePath) {
		worktreePath = filepath.Join(absBase, worktreePath)
	}
	absWorktree, err := filepath.Abs(worktreePath)
	if err != nil {
		return WorktreeCloseResult{}, err
	}
	rel, err := filepath.Rel(absBase, absWorktree)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return WorktreeCloseResult{}, fmt.Errorf("worktree_path must stay under base_dir")
	}
	if err := rejectWorktreeBase(absWorktree); err != nil {
		return WorktreeCloseResult{}, err
	}
	if _, err := os.Stat(absWorktree); err != nil {
		return WorktreeCloseResult{}, err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", absRepo, "worktree", "remove", absWorktree)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return WorktreeCloseResult{}, fmt.Errorf("git worktree remove failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	at := now().UTC()
	repoName := strings.TrimSpace(opts.RepoName)
	if repoName == "" {
		repoName = filepath.Base(absRepo)
	}
	branch := strings.TrimSpace(opts.Branch)
	if branch == "" {
		branch = "unknown"
	}
	worktreeID := strings.TrimSpace(opts.WorktreeID)
	if worktreeID == "" {
		worktreeID = "worktree:" + repoName + ":" + safePathName(branch)
	}
	worktree := domainai.WorktreeRegistry{
		WorktreeID: worktreeID,
		Repo:       repoName,
		Path:       absWorktree,
		Branch:     branch,
		OwnerAgent: strings.TrimSpace(opts.OwnerAgent),
		Status:     "closed",
		CreatedAt:  at,
		ClosedAt:   at,
	}
	event := domainai.WorkflowEvent{
		EventID:    "worktree_closed:" + repoName + ":" + safePathName(branch) + ":" + at.Format("20060102T150405Z"),
		EventType:  "worktree_closed",
		Agent:      strings.TrimSpace(opts.OwnerAgent),
		Repo:       repoName,
		WorktreeID: worktree.WorktreeID,
		Status:     "completed",
		CreatedAt:  at,
		Summary:    "closed git worktree " + absWorktree,
	}
	if event.Agent == "" {
		event.Agent = "Worker"
	}
	if m.store != nil {
		if err := m.store.SaveWorktreeRegistry(ctx, worktree); err != nil {
			return WorktreeCloseResult{}, err
		}
		if err := m.store.SaveWorkflowEvent(ctx, event); err != nil {
			return WorktreeCloseResult{}, err
		}
	}
	return WorktreeCloseResult{
		Worktree: worktree,
		Event:    event,
		Command:  "git -C " + absRepo + " worktree remove " + absWorktree,
	}, nil
}

func ensureGitRepo(path string) error {
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return nil
	}
	cmd := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("repo_root must be a git repository: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func rejectWorktreeBase(absBase string) error {
	clean := filepath.Clean(absBase)
	switch clean {
	case "/", "/home", "/tmp", "/var", "/etc", "/usr", "/System", "/Applications":
		return fmt.Errorf("refusing broad or system worktree base: %s", clean)
	}
	return nil
}

func isProtectedBranch(branch string) bool {
	switch strings.TrimSpace(branch) {
	case "main", "master", "develop", "production":
		return true
	default:
		return false
	}
}

func safePathName(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "refs/heads/")
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-")
	value = replacer.Replace(value)
	value = strings.Trim(value, ".-")
	return value
}
