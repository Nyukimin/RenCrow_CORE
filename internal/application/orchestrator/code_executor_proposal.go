package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
)

func shouldUseProposalPath(route routing.Route, target codeTarget) bool {
	switch route {
	case routing.RouteCODE1, routing.RouteCODE2, routing.RouteCODE3, routing.RouteCODE4:
		return true
	}
	return false
}

// tryExecuteProposalPath はProposal生成→Worker実行パスを試行
func (e *DefaultCodeExecutor) tryExecuteProposalPath(
	ctx context.Context,
	req CodeExecutionRequest,
	target codeTarget,
) (CodeExecutionResponse, bool, error) {
	coderWithProposal, ok := proposalCoderForTarget(target)
	if !ok {
		return CodeExecutionResponse{}, false, nil
	}

	p, err := e.generateProposalForTarget(ctx, req, target, coderWithProposal)
	if err != nil {
		return CodeExecutionResponse{}, true, err
	}

	if err := e.validateGeneratedProposal(req, target, p); err != nil {
		return CodeExecutionResponse{}, true, err
	}

	e.emitProposalPlan(req, target, p)
	result, err := e.executeProposalWithWorker(ctx, req, p)
	if err != nil {
		e.recordCoderProposalEvidence(ctx, req, target, p, nil, "", err)
		return CodeExecutionResponse{}, true, err
	}

	formatted := formatExecutionResult(p, result)
	e.recordCoderProposalEvidence(ctx, req, target, p, result, formatted, nil)
	e.emitProposalExecutionResult(req, formatted)

	return buildProposalHandledResponse(formatted), true, nil
}

func proposalCoderForTarget(target codeTarget) (CoderAgentWithProposal, bool) {
	coderWithProposal, ok := target.coder.(CoderAgentWithProposal)
	return coderWithProposal, ok
}

func (e *DefaultCodeExecutor) generateProposalForTarget(
	ctx context.Context,
	req CodeExecutionRequest,
	target codeTarget,
	coderWithProposal CoderAgentWithProposal,
) (*proposal.Proposal, error) {
	p, err := coderWithProposal.GenerateProposal(ctx, req.Task)
	if err != nil {
		if p, ok := synthesizeNoChangeProposalForRequest(req, err); ok {
			e.emit("agent.response", target.name, "shiro", "変更なしの診断結果として処理します: "+err.Error(), req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
			return p, nil
		}
		if retryableProposalFailure(err) {
			retryTask := req.Task.WithUserMessage(appendProposalRetryInstruction(req.Task.UserMessage(), err))
			p, retryErr := coderWithProposal.GenerateProposal(ctx, retryTask)
			if retryErr == nil {
				e.emit("agent.response", target.name, "shiro", "Proposal 形式不正を検出し、1回だけ再生成しました", req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
				return p, nil
			}
			err = fmt.Errorf("%w; retry failed: %v", err, retryErr)
		}
		e.emit("agent.response", target.name, "shiro", "エラー: "+err.Error(), req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
		e.emitProposalFailure(req, target, "Proposal生成失敗: "+err.Error())
		return nil, fmt.Errorf("%s proposal generation failed: %w", target.name, err)
	}
	return p, nil
}

func synthesizeNoChangeProposalForRequest(req CodeExecutionRequest, err error) (*proposal.Proposal, bool) {
	if !isNoChangeCodeRequest(req.Task.UserMessage()) || !retryableProposalFailure(err) {
		return nil, false
	}
	plan := strings.TrimSpace(req.Task.UserMessage())
	if plan == "" {
		plan = "No-change diagnostic request"
	}
	return proposal.NewProposal(
		"変更禁止または診断のみの依頼として扱う。\n- Worker/Coder 経路への到達確認を記録する。\n- ファイル変更、コマンド実行、外部書き込みは行わない。\n\n依頼:\n"+plan,
		"[]",
		"No file changes and no command execution.",
		"No-op",
	), true
}

func isNoChangeCodeRequest(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	positive := []string{
		"診断",
		"確認だけ",
		"経路確認",
		"届いたことだけ",
		"報告してください",
		"ファイル変更やコマンド実行はせず",
		"ファイル変更なし",
		"変更禁止",
		"変更しない",
		"コマンド実行は禁止",
		"read-only",
		"read only",
		"no file changes",
		"do not change files",
	}
	strongNoChange := []string{
		"ファイル変更やコマンド実行はせず",
		"ファイル変更なし",
		"変更禁止",
		"変更しない",
		"コマンド実行は禁止",
		"read-only",
		"read only",
		"no file changes",
		"do not change files",
	}
	negative := []string{
		"作成",
		"修正",
		"実装",
		"変更して",
		"追加して",
		"削除して",
		"create",
		"modify",
		"implement",
		"fix",
		"patch",
	}
	hasPositive := false
	for _, keyword := range positive {
		if strings.Contains(lower, strings.ToLower(keyword)) {
			hasPositive = true
			break
		}
	}
	if !hasPositive {
		return false
	}
	for _, keyword := range strongNoChange {
		if strings.Contains(lower, strings.ToLower(keyword)) {
			return true
		}
	}
	for _, keyword := range negative {
		if strings.Contains(lower, strings.ToLower(keyword)) && !strings.Contains(lower, "変更禁止") && !strings.Contains(lower, "do not change") {
			return false
		}
	}
	return true
}

func retryableProposalFailure(err error) bool {
	if _, _, retryable, ok := agent.ProposalFailureInfo(err); ok {
		return retryable
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, agent.ProposalFailureEmpty) ||
		strings.Contains(text, agent.ProposalFailureMissingPlan) ||
		strings.Contains(text, agent.ProposalFailureMissingPatch) ||
		strings.Contains(text, agent.ProposalFailureInvalidPatch)
}

func appendProposalRetryInstruction(message string, err error) string {
	return strings.TrimSpace(message) + ` 

## Coder Proposal Retry
The previous Coder response was not executable.
Failure: ` + err.Error() + `

Return exactly these sections:

## Plan
- Concrete steps.

## Patch
Use a runnable JSON patch array or Markdown patch code blocks.
If the request is diagnostic/read-only and requires no changes, return an empty JSON array:
[]

## Risk
- Concrete risk or "No file changes."
`
}

func (e *DefaultCodeExecutor) validateGeneratedProposal(
	req CodeExecutionRequest,
	target codeTarget,
	p *proposal.Proposal,
) error {
	if p != nil && p.IsValid() {
		return nil
	}
	e.emit("agent.response", target.name, "shiro", "無効な Proposal が返されました", req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
	e.emitProposalFailure(req, target, "Proposal形式不正")
	return fmt.Errorf("%s proposal generation failed: invalid proposal", target.name)
}

func (e *DefaultCodeExecutor) emitProposalFailure(req CodeExecutionRequest, target codeTarget, report string) {
	e.emit("agent.report", target.name, "shiro", formatAgentHandoffCompletionSpeech("shiro", target.name, report), req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
	e.emit("agent.report", "shiro", "mio", formatShiroToMioReport(req.Route, req.JobID, report), req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
}

func (e *DefaultCodeExecutor) emitProposalPlan(req CodeExecutionRequest, target codeTarget, p *proposal.Proposal) {
	e.emit("agent.response", target.name, "shiro", "## Plan\n"+p.Plan(), req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
	e.emit("agent.report", target.name, "shiro", formatAgentHandoffCompletionSpeech("shiro", target.name, "Planを生成しました。"+p.Plan()), req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
}

func (e *DefaultCodeExecutor) executeProposalWithWorker(
	ctx context.Context,
	req CodeExecutionRequest,
	p *proposal.Proposal,
) (*patch.PatchExecutionResult, error) {
	e.emit("agent.start", "shiro", "mio", "Patch を実行中...", req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
	e.emit("worker.request", "shiro", "worker", formatShiroToWorkerInstruction(req, p), req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)

	result, err := e.executeProposalWithResolvedWorkspace(ctx, req, p)
	if err != nil {
		e.emit("worker.result", "worker", "shiro", formatWorkerToShiroResult(nil, err), req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
		e.emit("agent.response", "shiro", "mio", "実行失敗: "+err.Error(), req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
		e.emit("agent.report", "shiro", "mio", formatShiroToMioReport(req.Route, req.JobID, "実行失敗: "+err.Error()), req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
		return nil, fmt.Errorf("worker execution failed: %w", err)
	}
	e.emit("worker.result", "worker", "shiro", formatWorkerToShiroResult(result, nil), req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
	return result, nil
}

func (e *DefaultCodeExecutor) executeProposalWithResolvedWorkspace(
	ctx context.Context,
	req CodeExecutionRequest,
	p *proposal.Proposal,
) (*patch.PatchExecutionResult, error) {
	if req.Module.Found() && req.Module.Module.Root != "" {
		if worker, ok := e.workerExecution.(service.WorkspaceOverrideWorkerExecutionService); ok {
			e.emit("worker.workspace", "shiro", "worker", req.Module.Summary(), req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
			return worker.ExecuteProposalInWorkspace(ctx, req.Task.JobID(), p, req.Module.Module.Root)
		}
		e.emit("worker.workspace_unavailable", "shiro", "worker", req.Module.Summary(), req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
	}
	return e.workerExecution.ExecuteProposal(ctx, req.Task.JobID(), p)
}

func (e *DefaultCodeExecutor) emitProposalExecutionResult(req CodeExecutionRequest, formatted string) {
	e.emit("agent.response", "shiro", "mio", formatted, req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
	e.emit("agent.report", "shiro", "mio", formatShiroToMioReport(req.Route, req.JobID, formatted), req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
}

func (e *DefaultCodeExecutor) recordCoderProposalEvidence(
	ctx context.Context,
	req CodeExecutionRequest,
	target codeTarget,
	p *proposal.Proposal,
	result *patch.PatchExecutionResult,
	formatted string,
	runErr error,
) {
	if e.proposalEvidence == nil || p == nil {
		return
	}
	evidence := domainskill.CoderProposalEvidence{
		JobID:           req.JobID,
		SessionID:       req.SessionID,
		Route:           req.Route.String(),
		Agent:           target.name,
		TaskText:        req.Task.UserMessage(),
		Plan:            p.Plan(),
		Patch:           p.Patch(),
		Risk:            p.Risk(),
		CostHint:        p.CostHint(),
		FormattedResult: formatted,
		Success:         runErr == nil,
	}
	if result != nil {
		evidence.ExecutionSummary = result.Summary
		evidence.Success = result.Success
	}
	if runErr != nil {
		evidence.ExecutionError = runErr.Error()
	}
	paths, err := e.proposalEvidence.SaveCoderProposalEvidence(ctx, evidence)
	if err != nil {
		log.Printf("WARN: failed to save coder proposal evidence job=%s route=%s: %v", req.JobID, req.Route, err)
		return
	}
	if paths.SkillDiffPath != "" || paths.AgentTranscriptPath != "" {
		log.Printf("Coder proposal evidence saved job=%s skill_diff=%s agent_transcript=%s", req.JobID, paths.SkillDiffPath, paths.AgentTranscriptPath)
	}
}
