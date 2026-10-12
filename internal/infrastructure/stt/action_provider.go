package stt

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
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type ActionProvider struct {
	inner     Provider
	actions   *actionmanager.Manager
	tasks     *taskmanager.Manager
	transport *transportmanager.Manager
	actorID   string
}

func NewActionProvider(inner Provider, actions *actionmanager.Manager, tasks *taskmanager.Manager, transport *transportmanager.Manager, actorID string) (*ActionProvider, error) {
	if inner == nil {
		return nil, errors.New("STT provider is required")
	}
	if actions == nil || tasks == nil || transport == nil {
		return nil, errors.New("STT execution owners are required")
	}
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return nil, errors.New("STT actor_id is required")
	}
	return &ActionProvider{inner: inner, actions: actions, tasks: tasks, transport: transport, actorID: actorID}, nil
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
		err = fmt.Errorf("create STT action: %w", err)
		if ownsRun {
			// The Run this call started holds an execution slot, and no Action
			// exists to close it later.
			return Result{}, p.tasks.AbortStartedRun(ctx, identity.TaskID, identity.RunID, p.actorID, err)
		}
		return Result{}, err
	}
	ownedCtx, err = domainexecution.WithChildBoundActionAttempt(ownedCtx, action.ActionID, attempt.AttemptID)
	if err != nil {
		return Result{}, p.complete(ctx, identity, ownsRun, action, attempt, err)
	}
	ownedCtx, err = domaintransport.WithReceiptOwner(ownedCtx, p.transport)
	if err != nil {
		return Result{}, p.complete(ctx, identity, ownsRun, action, attempt, err)
	}
	result, resultErr = p.inner.Transcribe(ownedCtx, wav)
	resultErr = p.complete(ctx, identity, ownsRun, action, attempt, resultErr)
	return result, resultErr
}

func (p *ActionProvider) executionContext(ctx context.Context) (context.Context, domainexecution.Identity, bool, error) {
	if identity, err := domainexecution.IdentityFromContext(ctx); err == nil {
		return ctx, identity, false, nil
	}
	task, run, err := p.tasks.CreateAndStartRun(ctx, domaintask.Task{
		Title:    "STT transcription",
		Route:    domaintask.RouteCHAT,
		OwnerID:  p.actorID,
		Assignee: p.actorID,
		ReadOnly: true,
	}, domaintask.SharedRoleContext{CurrentPlan: "transcribe authenticated audio input"})
	if err != nil {
		return nil, domainexecution.Identity{}, false, fmt.Errorf("start STT run: %w", err)
	}
	identity := domainexecution.Identity{TaskID: task.TaskID, RunID: run.RunID, TraceID: modulecore.NewTraceID()}
	ownedCtx, err := domainexecution.WithIdentity(ctx, identity.TaskID, identity.RunID, identity.TraceID)
	if err != nil {
		return nil, domainexecution.Identity{}, false, p.tasks.AbortStartedRun(ctx, identity.TaskID, identity.RunID, p.actorID, err)
	}
	return ownedCtx, identity, true, nil
}

// complete closes the Attempt and Action, and the Run when this call started it.
// If the Attempt cannot be closed the Run stays open with it.
func (p *ActionProvider) complete(ctx context.Context, identity domainexecution.Identity, ownsRun bool, action domainaction.Action, attempt domainaction.Attempt, operationErr error) error {
	const completed = "STT transcription completed"
	if closed, err := p.actions.CloseAttempt(ctx, action.ActionID, attempt.AttemptID, actionmanager.AttemptOutcomeOf(operationErr, completed), operationErr); !closed {
		return err
	}
	if !ownsRun {
		return operationErr
	}
	return p.tasks.CloseStartedRun(ctx, identity.TaskID, identity.RunID, p.actorID, taskmanager.RunOutcomeOf(operationErr, completed), operationErr)
}
