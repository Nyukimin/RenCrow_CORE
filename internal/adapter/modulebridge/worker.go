package modulebridge

import (
	"context"
	"fmt"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/service"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/patch"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/proposal"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
	moduleworker "github.com/Nyukimin/picoclaw_multiLLM/modules/worker"
)

type WorkerExecutorAdapter struct {
	service service.WorkerExecutionService
}

func NewWorkerExecutorAdapter(service service.WorkerExecutionService) *WorkerExecutorAdapter {
	return &WorkerExecutorAdapter{service: service}
}

func NewRuntimeWorkerExecutor(service service.WorkerExecutionService) moduleworker.Executor {
	return NewWorkerExecutorAdapter(service)
}

func (a *WorkerExecutorAdapter) Health(context.Context) core.HealthReport {
	if a == nil || a.service == nil {
		return moduleworker.BuildExecutorHealth(moduleworker.ExecutorHealthSnapshot{})
	}
	return moduleworker.BuildExecutorHealth(moduleworker.ExecutorHealthSnapshot{Ready: true})
}

func (a *WorkerExecutorAdapter) Execute(ctx context.Context, action moduleworker.Action) (moduleworker.Result, error) {
	startedAt := time.Now().UTC()
	if a == nil || a.service == nil {
		err := fmt.Errorf("worker execution service is nil")
		return moduleworker.BuildFailedResult(action.JobID, "", err.Error(), startedAt, time.Now().UTC()), err
	}
	patchArgs, err := moduleworker.ExtractProposalPatchArgs(action)
	if err != nil {
		return moduleworker.BuildActionErrorResult(action, err, startedAt, time.Now().UTC()), err
	}
	jobID, err := task.ParseJobID(string(action.JobID))
	if err != nil {
		return moduleworker.BuildFailedResult(action.JobID, "", err.Error(), startedAt, time.Now().UTC()), err
	}
	p := proposal.NewProposal(
		patchArgs.Plan,
		patchArgs.Patch,
		patchArgs.Risk,
		patchArgs.CostHint,
	)
	if !p.IsValid() {
		err := fmt.Errorf("proposal action requires plan and patch")
		return moduleworker.BuildFailedResult(action.JobID, "", err.Error(), startedAt, time.Now().UTC()), err
	}

	execResult, err := a.service.ExecuteProposal(ctx, jobID, p)
	finishedAt := time.Now().UTC()
	if err != nil {
		return moduleworker.BuildFailedResult(action.JobID, "", err.Error(), startedAt, finishedAt), err
	}

	summary := toWorkerPatchExecutionSummary(execResult)
	return moduleworker.BuildPatchExecutionResult(action.JobID, summary, startedAt, finishedAt), nil
}

func toWorkerPatchExecutionSummary(result *patch.PatchExecutionResult) *moduleworker.PatchExecutionSummary {
	if result == nil {
		return nil
	}
	return &moduleworker.PatchExecutionSummary{
		Success:       result.Success,
		Summary:       result.Summary,
		ExecutedCmds:  result.ExecutedCmds,
		FailedCmds:    result.FailedCmds,
		GitCommit:     result.GitCommit,
		FailureKind:   result.FailureKind,
		FailureReason: result.FailureReason,
		Retryable:     result.Retryable,
		FailedIndex:   result.FailedIndex,
	}
}
