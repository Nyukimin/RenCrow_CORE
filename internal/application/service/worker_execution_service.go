package service

import (
	"context"
	"fmt"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// WorkerExecutionService はPatch実行サービスのインターフェース
type WorkerExecutionService interface {
	ExecuteProposal(ctx context.Context, taskID modulecore.TaskID, p *proposal.Proposal) (*patch.PatchExecutionResult, error)
	ExecuteObservation(ctx context.Context, actions []ObservationAction) ([]ObservationActionResult, error)
}

type WorkspaceOverrideWorkerExecutionService interface {
	ExecuteProposalInWorkspace(ctx context.Context, taskID modulecore.TaskID, p *proposal.Proposal, workspace string) (*patch.PatchExecutionResult, error)
}

// MCPToolCaller は MCP プロトコル経由でツールを呼び出すインターフェース
type MCPToolCaller interface {
	CallTool(ctx context.Context, toolName string, args map[string]any) (string, error)
}

// workerExecutionService はWorkerExecutionServiceの実装
type workerExecutionService struct {
	config    config.WorkerConfig
	mcpCaller MCPToolCaller // optional: Serena MCP ツール呼び出し
	owner     *taskmanager.Manager
}

// NewWorkerExecutionService は新しいWorkerExecutionServiceを作成
func NewWorkerExecutionService(cfg config.WorkerConfig, owners ...*taskmanager.Manager) *workerExecutionService {
	var owner *taskmanager.Manager
	if len(owners) == 1 {
		owner = owners[0]
	}
	return &workerExecutionService{
		config: cfg,
		owner:  owner,
	}
}

// SetMCPToolCaller は MCP ツールランナーを注入する（オプション）
func (w *workerExecutionService) SetMCPToolCaller(caller MCPToolCaller) {
	w.mcpCaller = caller
}

// ExecuteProposal はProposalのPatchを解析・実行する
func (w *workerExecutionService) ExecuteProposal(
	ctx context.Context,
	taskID modulecore.TaskID,
	p *proposal.Proposal,
) (*patch.PatchExecutionResult, error) {
	if w == nil {
		return nil, fmt.Errorf("worker execution service is nil")
	}
	if w.owner == nil {
		return nil, fmt.Errorf("worker proposal execution owner is unavailable")
	}
	identity, err := w.owner.ValidateExecutionContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("worker proposal execution admission denied: %w", err)
	}
	if identity.TaskID != taskID {
		return nil, fmt.Errorf("worker proposal task identity mismatch: context=%s argument=%s", identity.TaskID, taskID)
	}

	commands, err := w.parseProposalCommands(p)
	if err != nil {
		return nil, err
	}
	commands = w.normalizeParsedCommands(commands)

	w.showExecutionSummaryIfEnabled(taskID, commands)
	if err := w.validateCommandsBeforeExecution(commands); err != nil {
		return nil, err
	}
	testImpactScope, err := w.inspectTestImpactScope()
	if err != nil {
		return nil, err
	}
	if testImpactScope.status == "blocked" {
		return nil, fmt.Errorf("test-impact blocked before patch execution: %s", testImpactScope.reason)
	}
	if err := w.autoCommitBeforeExecution(ctx, taskID); err != nil {
		return nil, err
	}

	result := w.executeCommands(ctx, taskID, commands)
	result = w.finalizeExecutionResult(commands, result)
	if result.Success {
		result = w.executeTestImpact(ctx, taskID, result, testImpactScope)
	} else {
		// Patch commands may have changed the workspace before a later command
		// failed. Preserve that disclosure while making the skipped verification
		// explicit in the final result.
		result.TestStatus = "not_run"
	}
	result = w.finalizeTestImpactResult(result)
	if result.Success {
		// A post-execution commit is allowed only after the owner test route has
		// completed (or explicitly reported not_applicable).
		w.autoCommitAfterExecution(ctx, taskID, result)
	}
	return result, nil
}

func (w *workerExecutionService) ExecuteProposalInWorkspace(
	ctx context.Context,
	taskID modulecore.TaskID,
	p *proposal.Proposal,
	workspace string,
) (*patch.PatchExecutionResult, error) {
	if w == nil {
		return nil, fmt.Errorf("worker execution service is nil")
	}
	if workspace == "" || workspace == w.config.Workspace {
		return w.ExecuteProposal(ctx, taskID, p)
	}
	clone := *w
	clone.config = w.config
	clone.config.Workspace = workspace
	return clone.ExecuteProposal(ctx, taskID, p)
}
