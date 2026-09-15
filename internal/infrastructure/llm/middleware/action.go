package middleware

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type actionProvider struct {
	inner     llm.LLMProvider
	actions   *actionmanager.Manager
	tasks     *taskmanager.Manager
	transport *transportmanager.Manager
	actorID   string
}

type actionToolCallingProvider struct {
	*actionProvider
	toolInner llm.ToolCallingProvider
}

// WithActionOwner records every physical provider call through the canonical
// Action/Attempt owner while preserving whether the provider supports tools.
func WithActionOwner(inner llm.LLMProvider, actions *actionmanager.Manager) (llm.LLMProvider, error) {
	return withActionExecutionOwners(inner, actions, nil, nil, "")
}

func WithActionExecutionOwners(inner llm.LLMProvider, actions *actionmanager.Manager, tasks *taskmanager.Manager, transport *transportmanager.Manager, actorID string) (llm.LLMProvider, error) {
	if tasks == nil || transport == nil || strings.TrimSpace(actorID) == "" {
		return nil, errors.New("LLM Task transport owners and actor_id are required")
	}
	return withActionExecutionOwners(inner, actions, tasks, transport, strings.TrimSpace(actorID))
}

func withActionExecutionOwners(inner llm.LLMProvider, actions *actionmanager.Manager, tasks *taskmanager.Manager, transport *transportmanager.Manager, actorID string) (llm.LLMProvider, error) {
	if inner == nil {
		return nil, errors.New("LLM provider is required")
	}
	if actions == nil {
		return nil, errors.New("LLM Action owner is required")
	}
	base := &actionProvider{inner: inner, actions: actions, tasks: tasks, transport: transport, actorID: actorID}
	if toolInner, ok := inner.(llm.ToolCallingProvider); ok {
		return &actionToolCallingProvider{actionProvider: base, toolInner: toolInner}, nil
	}
	return base, nil
}

func (p *actionProvider) Name() string {
	return p.inner.Name()
}

func (p *actionProvider) Generate(ctx context.Context, req llm.GenerateRequest) (response llm.GenerateResponse, resultErr error) {
	ownedCtx, finish, err := p.begin(ctx, "generate")
	if err != nil {
		return llm.GenerateResponse{}, err
	}
	response, resultErr = p.inner.Generate(ownedCtx, req)
	resultErr = finish(resultErr)
	return response, resultErr
}

func (p *actionToolCallingProvider) Chat(ctx context.Context, req llm.ChatRequest) (response llm.ChatResponse, resultErr error) {
	ownedCtx, finish, err := p.begin(ctx, "chat")
	if err != nil {
		return llm.ChatResponse{}, err
	}
	response, resultErr = p.toolInner.Chat(ownedCtx, req)
	resultErr = finish(resultErr)
	return response, resultErr
}

func (p *actionProvider) begin(ctx context.Context, operation string) (context.Context, func(error) error, error) {
	identity, err := domainexecution.IdentityFromContext(ctx)
	ownsRun := false
	if err != nil {
		if p.tasks == nil || p.actorID == "" {
			return nil, nil, fmt.Errorf("LLM execution identity: %w", err)
		}
		task, createErr := p.tasks.Create(ctx, domaintask.Task{
			Title:    "LLM provider call",
			Route:    domaintask.RouteGeneral,
			OwnerID:  p.actorID,
			Assignee: p.actorID,
			ReadOnly: true,
		}, domaintask.SharedRoleContext{CurrentPlan: "execute canonical LLM provider operation"})
		if createErr != nil {
			return nil, nil, fmt.Errorf("create LLM task: %w", createErr)
		}
		run, startErr := p.tasks.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonFirst)
		if startErr != nil {
			return nil, nil, fmt.Errorf("start LLM run: %w", startErr)
		}
		identity = domainexecution.Identity{TaskID: task.TaskID, RunID: run.RunID, TraceID: modulecore.NewTraceID()}
		ctx, err = domainexecution.WithIdentity(ctx, identity.TaskID, identity.RunID, identity.TraceID)
		if err != nil {
			return nil, nil, err
		}
		ownsRun = true
	}
	name := strings.TrimSpace(p.inner.Name())
	if name == "" {
		name = "unknown"
	}
	action, attempt, err := p.actions.CreateAction(ctx, actionmanager.CreateInput{
		TaskID: identity.TaskID,
		RunID:  identity.RunID,
		Kind:   domainaction.KindLLM,
		Name:   "llm." + name + "." + operation,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create LLM action: %w", err)
	}
	ownedCtx, err := domainexecution.WithChildBoundActionAttempt(ctx, action.ActionID, attempt.AttemptID)
	if err != nil {
		return nil, nil, err
	}
	if p.transport != nil {
		ownedCtx, err = domaintransport.WithReceiptOwner(ownedCtx, p.transport)
		if err != nil {
			return nil, nil, err
		}
	}
	finish := func(callErr error) error {
		attemptStatus := domainaction.AttemptStatusSucceeded
		actionStatus := domainaction.StatusSucceeded
		summary := "LLM " + operation + " completed"
		switch {
		case errors.Is(callErr, context.Canceled):
			attemptStatus = domainaction.AttemptStatusCancelled
			actionStatus = domainaction.StatusCancelled
			summary = callErr.Error()
		case errors.Is(callErr, context.DeadlineExceeded):
			attemptStatus = domainaction.AttemptStatusTimedOut
			actionStatus = domainaction.StatusFailed
			summary = callErr.Error()
		case callErr != nil:
			attemptStatus = domainaction.AttemptStatusFailed
			actionStatus = domainaction.StatusFailed
			summary = callErr.Error()
		}
		completionCtx := context.WithoutCancel(ctx)
		if _, _, err := p.actions.CompleteAttempt(completionCtx, action.ActionID, attempt.AttemptID, attemptStatus, actionStatus, summary); err != nil {
			if callErr != nil {
				return errors.Join(callErr, fmt.Errorf("complete LLM action: %w", err))
			}
			return fmt.Errorf("complete LLM action: %w", err)
		}
		if ownsRun {
			taskStatus := domaintask.StatusSucceeded
			if actionStatus == domainaction.StatusCancelled {
				taskStatus = domaintask.StatusCancelled
			} else if actionStatus != domainaction.StatusSucceeded {
				taskStatus = domaintask.StatusFailed
			}
			if _, err := p.tasks.CompleteRun(completionCtx, identity.TaskID, identity.RunID, p.actorID, taskStatus, summary, ""); err != nil {
				if callErr != nil {
					return errors.Join(callErr, fmt.Errorf("complete LLM run: %w", err))
				}
				return fmt.Errorf("complete LLM run: %w", err)
			}
		}
		return callErr
	}
	return ownedCtx, finish, nil
}
