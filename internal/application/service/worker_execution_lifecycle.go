package service

import (
	"context"
	"fmt"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/patch"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/proposal"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
)

func (w *workerExecutionService) parseProposalCommands(p *proposal.Proposal) ([]patch.PatchCommand, error) {
	commands, err := patch.ParsePatch(p.Patch())
	if err != nil {
		return nil, fmt.Errorf("patch parse error: %w", err)
	}
	return commands, nil
}

func (w *workerExecutionService) showExecutionSummaryIfEnabled(jobID task.JobID, commands []patch.PatchCommand) {
	if w.config.ShowExecutionSummary {
		w.showExecutionSummary(jobID, commands)
	}
}

func (w *workerExecutionService) autoCommitBeforeExecution(ctx context.Context, jobID task.JobID) error {
	if !w.config.AutoCommit {
		return nil
	}
	preCommitHash, err := w.autoCommitChanges(ctx, jobID, "Before patch execution")
	if err != nil {
		return fmt.Errorf("pre-execution auto-commit failed: %w", err)
	}
	fmt.Printf("[Worker] Pre-commit succeeded: %s\n", preCommitHash)
	return nil
}

func (w *workerExecutionService) autoCommitAfterExecution(ctx context.Context, jobID task.JobID, result *patch.PatchExecutionResult) {
	if !w.config.AutoCommit || result.ExecutedCmds == 0 {
		return
	}
	postCommitHash, err := w.autoCommitChanges(ctx, jobID,
		fmt.Sprintf("Patch execution: %d commands", result.ExecutedCmds))
	if err == nil {
		result.WithGitCommit(postCommitHash)
		return
	}
	fmt.Printf("[Worker] Post-commit failed: %v\n", err)
}

func (w *workerExecutionService) finalizeExecutionResult(commands []patch.PatchCommand, result *patch.PatchExecutionResult) *patch.PatchExecutionResult {
	summary := fmt.Sprintf("実行: %d 件, 成功: %d 件, 失敗: %d 件",
		len(commands), result.ExecutedCmds, result.FailedCmds)
	w.classifyExecutionFailure(result)
	result = result.WithSummary(summary)

	fmt.Printf("[Worker] Patch execution completed: %s\n", summary)

	return result
}
