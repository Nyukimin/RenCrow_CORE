package service

import (
	"context"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/patch"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/proposal"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
)

// WorkerExecutionService はPatch実行サービスのインターフェース
type WorkerExecutionService interface {
	ExecuteProposal(ctx context.Context, jobID task.JobID, p *proposal.Proposal) (*patch.PatchExecutionResult, error)
	ExecuteObservation(ctx context.Context, actions []ObservationAction) ([]ObservationActionResult, error)
}

type WorkspaceOverrideWorkerExecutionService interface {
	ExecuteProposalInWorkspace(ctx context.Context, jobID task.JobID, p *proposal.Proposal, workspace string) (*patch.PatchExecutionResult, error)
}

// MCPToolCaller は MCP プロトコル経由でツールを呼び出すインターフェース
type MCPToolCaller interface {
	CallTool(ctx context.Context, toolName string, args map[string]any) (string, error)
}

// workerExecutionService はWorkerExecutionServiceの実装
type workerExecutionService struct {
	config    config.WorkerConfig
	mcpCaller MCPToolCaller // optional: Serena MCP ツール呼び出し
}

// NewWorkerExecutionService は新しいWorkerExecutionServiceを作成
func NewWorkerExecutionService(cfg config.WorkerConfig) *workerExecutionService {
	return &workerExecutionService{
		config: cfg,
	}
}

// SetMCPToolCaller は MCP ツールランナーを注入する（オプション）
func (w *workerExecutionService) SetMCPToolCaller(caller MCPToolCaller) {
	w.mcpCaller = caller
}

// ExecuteProposal はProposalのPatchを解析・実行する
func (w *workerExecutionService) ExecuteProposal(
	ctx context.Context,
	jobID task.JobID,
	p *proposal.Proposal,
) (*patch.PatchExecutionResult, error) {
	commands, err := w.parseProposalCommands(p)
	if err != nil {
		return nil, err
	}
	commands = w.normalizeParsedCommands(commands)

	w.showExecutionSummaryIfEnabled(jobID, commands)
	if err := w.validateCommandsBeforeExecution(commands); err != nil {
		return nil, err
	}
	if err := w.autoCommitBeforeExecution(ctx, jobID); err != nil {
		return nil, err
	}

	result := w.executeCommands(ctx, jobID, commands)
	w.autoCommitAfterExecution(ctx, jobID, result)
	return w.finalizeExecutionResult(commands, result), nil
}

func (w *workerExecutionService) ExecuteProposalInWorkspace(
	ctx context.Context,
	jobID task.JobID,
	p *proposal.Proposal,
	workspace string,
) (*patch.PatchExecutionResult, error) {
	if workspace == "" || workspace == w.config.Workspace {
		return w.ExecuteProposal(ctx, jobID, p)
	}
	clone := *w
	clone.config = w.config
	clone.config.Workspace = workspace
	return clone.ExecuteProposal(ctx, jobID, p)
}
