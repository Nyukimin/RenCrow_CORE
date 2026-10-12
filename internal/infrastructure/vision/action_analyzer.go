package vision

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
	domainvision "github.com/Nyukimin/RenCrow_CORE/internal/domain/vision"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type ActionAnalyzer struct {
	inner     domainvision.Analyzer
	actions   *actionmanager.Manager
	tasks     *taskmanager.Manager
	transport *transportmanager.Manager
	actorID   string
}

func NewActionAnalyzer(inner domainvision.Analyzer, actions *actionmanager.Manager, tasks *taskmanager.Manager, transport *transportmanager.Manager, actorID string) (*ActionAnalyzer, error) {
	if inner == nil || actions == nil || tasks == nil || transport == nil {
		return nil, errors.New("Vision analyzer and execution owners are required")
	}
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return nil, errors.New("Vision actor_id is required")
	}
	return &ActionAnalyzer{inner: inner, actions: actions, tasks: tasks, transport: transport, actorID: actorID}, nil
}

func (a *ActionAnalyzer) Analyze(ctx context.Context, request domainvision.AnalyzeRequest) (result domainvision.AnalyzeResult, resultErr error) {
	ownedCtx, identity, ownsRun, err := a.executionContext(ctx)
	if err != nil {
		return domainvision.AnalyzeResult{}, err
	}
	action, attempt, err := a.actions.CreateAction(ownedCtx, actionmanager.CreateInput{
		TaskID: identity.TaskID, RunID: identity.RunID,
		Kind: domainaction.KindVision, Name: "vision.analyze",
	})
	if err != nil {
		err = fmt.Errorf("create Vision action: %w", err)
		if ownsRun {
			// The Run this call started holds an execution slot, and no Action
			// exists to close it later.
			return domainvision.AnalyzeResult{}, a.tasks.AbortStartedRun(ctx, identity.TaskID, identity.RunID, a.actorID, err)
		}
		return domainvision.AnalyzeResult{}, err
	}
	ownedCtx, err = domainexecution.WithChildBoundActionAttempt(ownedCtx, action.ActionID, attempt.AttemptID)
	if err != nil {
		return domainvision.AnalyzeResult{}, a.complete(ctx, identity, ownsRun, action, attempt, err)
	}
	ownedCtx, err = domaintransport.WithReceiptOwner(ownedCtx, a.transport)
	if err != nil {
		return domainvision.AnalyzeResult{}, a.complete(ctx, identity, ownsRun, action, attempt, err)
	}
	result, resultErr = a.inner.Analyze(ownedCtx, request)
	resultErr = a.complete(ctx, identity, ownsRun, action, attempt, resultErr)
	return result, resultErr
}

func (a *ActionAnalyzer) Health(ctx context.Context) (domainvision.HealthReport, error) {
	return a.inner.Health(ctx)
}

func (a *ActionAnalyzer) executionContext(ctx context.Context) (context.Context, domainexecution.Identity, bool, error) {
	if identity, err := domainexecution.IdentityFromContext(ctx); err == nil {
		return ctx, identity, false, nil
	}
	task, run, err := a.tasks.CreateAndStartRun(ctx, domaintask.Task{
		Title: "Vision analysis", Route: domaintask.RouteCHAT,
		OwnerID: a.actorID, Assignee: a.actorID, ReadOnly: true,
	}, domaintask.SharedRoleContext{CurrentPlan: "analyze authenticated visual input"})
	if err != nil {
		return nil, domainexecution.Identity{}, false, err
	}
	identity := domainexecution.Identity{TaskID: task.TaskID, RunID: run.RunID, TraceID: modulecore.NewTraceID()}
	ownedCtx, err := domainexecution.WithIdentity(ctx, identity.TaskID, identity.RunID, identity.TraceID)
	if err != nil {
		return nil, domainexecution.Identity{}, false, a.tasks.AbortStartedRun(ctx, identity.TaskID, identity.RunID, a.actorID, err)
	}
	return ownedCtx, identity, true, nil
}

// complete closes the Attempt and Action, and the Run when this call started it.
// If the Attempt cannot be closed the Run stays open with it.
func (a *ActionAnalyzer) complete(ctx context.Context, identity domainexecution.Identity, ownsRun bool, action domainaction.Action, attempt domainaction.Attempt, operationErr error) error {
	const completed = "Vision analysis completed"
	if closed, err := a.actions.CloseAttempt(ctx, action.ActionID, attempt.AttemptID, actionmanager.AttemptOutcomeOf(operationErr, completed), operationErr); !closed {
		return err
	}
	if !ownsRun {
		return operationErr
	}
	return a.tasks.CloseStartedRun(ctx, identity.TaskID, identity.RunID, a.actorID, taskmanager.RunOutcomeOf(operationErr, completed), operationErr)
}
