package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type AssetsGitStatusResponse struct {
	RepoPath         string `json:"repo_path"`
	Initialized      bool   `json:"initialized"`
	Branch           string `json:"branch"`
	LatestCommitHash string `json:"latest_commit_hash"`
}

func HandleAssetsGitStatus(repoPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		status, err := EnsureAssetsGitStatus(r.Context(), repoPath)
		if err != nil {
			http.Error(w, "failed to load assets git status", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	}
}

func EnsureAssetsGitStatus(ctx context.Context, repoPath string) (AssetsGitStatusResponse, error) {
	repoPath = filepath.Clean(strings.TrimSpace(repoPath))
	if repoPath == "." || repoPath == "" {
		return AssetsGitStatusResponse{}, errors.New("repo path is empty")
	}
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		return AssetsGitStatusResponse{}, fmt.Errorf("create assets repo: %w", err)
	}

	gitDir := filepath.Join(repoPath, ".git")
	if info, err := os.Stat(gitDir); err != nil || !info.IsDir() {
		if err := runGit(ctx, repoPath, "init"); err != nil {
			return AssetsGitStatusResponse{}, fmt.Errorf("initialize assets repo: %w", err)
		}
	}

	status := AssetsGitStatusResponse{
		RepoPath:         repoPath,
		Initialized:      true,
		Branch:           gitOutput(ctx, repoPath, "symbolic-ref", "--short", "HEAD"),
		LatestCommitHash: gitOutput(ctx, repoPath, "rev-parse", "--verify", "HEAD"),
	}
	return status, nil
}

func runGit(ctx context.Context, repoPath string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoPath}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func gitOutput(ctx context.Context, repoPath string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoPath}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}
