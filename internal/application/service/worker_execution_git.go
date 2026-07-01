package service

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/patch"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
)

// executeGitOperation はGit操作を実行
func (w *workerExecutionService) executeGitOperation(
	ctx context.Context,
	cmd patch.PatchCommand,
) (string, error) {
	timeout := time.Duration(w.config.GitTimeout) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Git操作はTargetにコマンド全体が入っている
	gitArgs := strings.Fields(cmd.Target)

	gitCmd := exec.CommandContext(ctx, "git", gitArgs...)
	gitCmd.Dir = w.config.Workspace

	output, err := gitCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git operation failed: %w, output: %s", err, string(output))
	}

	return string(output), nil
}

// autoCommitChanges はGit auto-commitを実行
func (w *workerExecutionService) autoCommitChanges(
	ctx context.Context,
	jobID task.JobID,
	message string,
) (string, error) {
	timeout := time.Duration(w.config.GitTimeout) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// git add -A
	addCmd := exec.CommandContext(ctx, "git", "add", "-A")
	addCmd.Dir = w.config.Workspace
	if output, err := addCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git add failed: %w, output: %s", err, string(output))
	}

	// git commit
	commitMsg := fmt.Sprintf("%s %s\n\nJobID: %s",
		w.config.CommitMessagePrefix, message, jobID.String())
	commitCmd := exec.CommandContext(ctx, "git", "commit", "-m", commitMsg)
	commitCmd.Dir = w.config.Workspace
	if output, err := commitCmd.CombinedOutput(); err != nil {
		// 変更がない場合は成功扱い
		if strings.Contains(string(output), "nothing to commit") {
			return "no-changes", nil
		}
		return "", fmt.Errorf("git commit failed: %w, output: %s", err, string(output))
	}

	// 最新コミットハッシュ取得
	hashCmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	hashCmd.Dir = w.config.Workspace
	hashOutput, err := hashCmd.Output()
	if err != nil {
		return "", fmt.Errorf("get commit hash failed: %w", err)
	}

	return strings.TrimSpace(string(hashOutput)), nil
}
