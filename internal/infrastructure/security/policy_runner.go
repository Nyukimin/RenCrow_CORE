package security

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	executionapp "github.com/Nyukimin/RenCrow_CORE/internal/application/execution"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// PolicyRunner は RunnerV2 をポリシー適用付きでラップする
type PolicyRunner struct {
	inner        tool.RunnerV2
	execService  *executionapp.Service
	actions      *actionmanager.Manager
	toolMetaByID map[string]tool.ToolMetadata
	requestedBy  string
}

func NewPolicyRunner(inner tool.RunnerV2, engine *PolicyEngine, repo domainexecution.Repository, actions *actionmanager.Manager, requestedBy string) (*PolicyRunner, error) {
	if inner == nil {
		return nil, fmt.Errorf("inner runner is required")
	}
	if engine == nil {
		return nil, fmt.Errorf("policy engine is required")
	}
	if actions == nil {
		return nil, fmt.Errorf("action manager is required")
	}
	metas, err := inner.ListTools(context.Background())
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	metaMap := make(map[string]tool.ToolMetadata, len(metas))
	for _, m := range metas {
		metaMap[m.ToolID] = m
	}
	svc := executionapp.NewService(engine, inner, repo)
	return &PolicyRunner{
		inner:        inner,
		execService:  svc,
		actions:      actions,
		toolMetaByID: metaMap,
		requestedBy:  requestedBy,
	}, nil
}

func (r *PolicyRunner) ExecuteV2(ctx context.Context, toolName string, args map[string]any) (response *tool.ToolResponse, runErr error) {
	if !r.hasTool(ctx, toolName) {
		return nil, fmt.Errorf("unknown tool: %s", toolName)
	}

	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if r.actions == nil {
		return nil, fmt.Errorf("action manager is required")
	}
	var actionID modulecore.ActionID
	var attemptID modulecore.AttemptID
	if boundActionID, boundAttemptID, ok := domainexecution.BoundActionAttemptFromContext(ctx); ok {
		actionID = boundActionID
		attemptID = boundAttemptID
		if err := r.actions.ValidateToolAttempt(ctx, actionID, attemptID, identity.TaskID, identity.RunID, toolName); err != nil {
			return nil, fmt.Errorf("validate bound tool attempt: %w", err)
		}
	} else {
		createdAction, createdAttempt, err := r.actions.CreateAction(ctx, actionmanager.CreateInput{
			TaskID: identity.TaskID,
			RunID:  identity.RunID,
			Kind:   domainaction.KindTool,
			Name:   toolName,
		})
		if err != nil {
			return nil, fmt.Errorf("create action: %w", err)
		}
		actionID = createdAction.ActionID
		attemptID = createdAttempt.AttemptID
		defer func() {
			if completionErr := r.actions.CompleteToolAttempt(ctx, actionID, attemptID, response, runErr); completionErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("complete tool attempt: %w", completionErr))
			}
		}()
		boundCtx, bindErr := domainexecution.WithBoundActionAttempt(ctx, actionID, attemptID)
		if bindErr != nil {
			return nil, bindErr
		}
		ctx = boundCtx
	}
	action := domainexecution.Action{
		TaskID:      identity.TaskID,
		TraceID:     identity.TraceID,
		ActionID:    actionID,
		AttemptID:   attemptID,
		Tool:        toolName,
		Arguments:   args,
		RequestedBy: r.requestedBy,
		RequestedAt: time.Now().UTC(),
	}
	result, runErr := r.execService.RequestToolExecution(ctx, action)
	if runErr != nil {
		if result != nil {
			return result.Response, runErr
		}
		return nil, runErr
	}
	if result == nil {
		return nil, fmt.Errorf("execution service returned nil result")
	}

	switch result.Record.Status {
	case domainexecution.StatusDenied:
		return tool.NewError(tool.ErrPermissionDenied, result.Record.Reason, map[string]any{"rule": "policy_deny"}), nil
	default:
		if result.Response != nil {
			return result.Response, nil
		}
		return tool.NewError(tool.ErrInternalError, "empty tool response", nil), nil
	}
}

func (r *PolicyRunner) ListTools(ctx context.Context) ([]tool.ToolMetadata, error) {
	return r.inner.ListTools(ctx)
}

func (r *PolicyRunner) hasTool(ctx context.Context, toolName string) bool {
	if _, exists := r.toolMetaByID[toolName]; exists {
		return true
	}
	metas, err := r.inner.ListTools(ctx)
	if err != nil {
		return false
	}
	for _, m := range metas {
		r.toolMetaByID[m.ToolID] = m
	}
	_, exists := r.toolMetaByID[toolName]
	return exists
}
