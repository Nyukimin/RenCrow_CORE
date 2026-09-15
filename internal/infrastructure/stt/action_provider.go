package stt

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type ActionProvider struct {
	inner   Provider
	actions *actionmanager.Manager
	tasks   *taskmanager.Manager
	actorID string
}

func NewActionProvider(inner Provider, actions *actionmanager.Manager, tasks *taskmanager.Manager, actorID string) (*ActionProvider, error) {
	if inner == nil {
		return nil, errors.New("STT provider is required")
	}
	if actions == nil || tasks == nil {
		return nil, errors.New("STT execution owners are required")
	}
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return nil, errors.New("STT actor_id is required")
	}
	return &ActionProvider{inner: inner, actions: actions, tasks: tasks, actorID: actorID}, nil
}

func (p *ActionProvider) Name() string {
	return p.inner.Name()
}

func (p *ActionProvider) Health(ctx context.Context) Health {
	return p.inner.Health(ctx)
}

func (p *ActionProvider) Transcribe(ctx context.Context, wav []byte) (result Result, resultErr error) {
	ownedCtx, identity, ownsRun, err := p.executionContext(ctx)
	if err != nil {
		return Result{}, err
	}
	action, attempt, err := p.actions.CreateAction(ownedCtx, actionmanager.CreateInput{
		TaskID: identity.TaskID,
		RunID:  identity.RunID,
		Kind:   domainaction.KindSTT,
		Name:   "stt." + p.inner.Name() + ".transcribe",
	})
	if err != nil {
		return Result{}, fmt.Errorf("create STT action: %w", err)
	}
	ownedCtx, err = domainexecution.WithChildBoundActionAttempt(ownedCtx, action.ActionID, attempt.AttemptID)
	if err != nil {
		return Result{}, err
	}
	result, resultErr = p.inner.Transcribe(ownedCtx, wav)
	resultErr = p.complete(ctx, identity, ownsRun, action, attempt, resultErr)
	return result, resultErr
}

func (p *ActionProvider) executionContext(ctx context.Context) (context.Context, domainexecution.Identity, bool, error) {
	if identity, err := domainexecution.IdentityFromContext(ctx); err == nil {
		return ctx, identity, false, nil
	}
	task, err := p.tasks.Create(ctx, domaintask.Task{
		Title:    "STT transcription",
		Route:    domaintask.RouteCHAT,
		OwnerID:  p.actorID,
		Assignee: p.actorID,
		ReadOnly: true,
	}, domaintask.SharedRoleContext{CurrentPlan: "transcribe authenticated audio input"})
	if err != nil {
		return nil, domainexecution.Identity{}, false, fmt.Errorf("create STT task: %w", err)
	}
	run, err := p.tasks.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		return nil, domainexecution.Identity{}, false, fmt.Errorf("start STT run: %w", err)
	}
	identity := domainexecution.Identity{TaskID: task.TaskID, RunID: run.RunID, TraceID: modulecore.NewTraceID()}
	ownedCtx, err := domainexecution.WithIdentity(ctx, identity.TaskID, identity.RunID, identity.TraceID)
	if err != nil {
		return nil, domainexecution.Identity{}, false, err
	}
	return ownedCtx, identity, true, nil
}

func (p *ActionProvider) complete(ctx context.Context, identity domainexecution.Identity, ownsRun bool, action domainaction.Action, attempt domainaction.Attempt, operationErr error) error {
	attemptStatus := domainaction.AttemptStatusSucceeded
	actionStatus := domainaction.StatusSucceeded
	taskStatus := domaintask.StatusSucceeded
	summary := "STT transcription completed"
	switch {
	case errors.Is(operationErr, context.Canceled):
		attemptStatus = domainaction.AttemptStatusCancelled
		actionStatus = domainaction.StatusCancelled
		taskStatus = domaintask.StatusCancelled
		summary = operationErr.Error()
	case errors.Is(operationErr, context.DeadlineExceeded):
		attemptStatus = domainaction.AttemptStatusTimedOut
		actionStatus = domainaction.StatusFailed
		taskStatus = domaintask.StatusFailed
		summary = operationErr.Error()
	case operationErr != nil:
		attemptStatus = domainaction.AttemptStatusFailed
		actionStatus = domainaction.StatusFailed
		taskStatus = domaintask.StatusFailed
		summary = operationErr.Error()
	}
	completionCtx := context.WithoutCancel(ctx)
	if _, _, err := p.actions.CompleteAttempt(completionCtx, action.ActionID, attempt.AttemptID, attemptStatus, actionStatus, summary); err != nil {
		return errors.Join(operationErr, fmt.Errorf("complete STT action: %w", err))
	}
	if ownsRun {
		if _, err := p.tasks.CompleteRun(completionCtx, identity.TaskID, identity.RunID, p.actorID, taskStatus, summary, ""); err != nil {
			return errors.Join(operationErr, fmt.Errorf("complete STT run: %w", err))
		}
	}
	return operationErr
}
