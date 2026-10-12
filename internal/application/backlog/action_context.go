package backlog

import (
	"context"
	"errors"
	"fmt"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// WithExecutionOwners enables direct and recovery operations to create their
// own canonical Task/Run before entering the Action owner.
func (s *Service) WithExecutionOwners(actions *actionmanager.Manager, tasks *taskmanager.Manager, actorID string) *Service {
	if s != nil {
		s.actions = actions
		s.tasks = tasks
		s.actionActorID = actorID
	}
	return s
}

func (s *Service) beginAction(ctx context.Context, name string) (context.Context, func(error) error, error) {
	if _, _, ok := domainexecution.BoundActionAttemptFromContext(ctx); ok {
		return ctx, func(error) error { return nil }, nil
	}
	if s == nil || s.actions == nil {
		return nil, nil, errors.New("Atlas Action owner is required")
	}
	identity, identityErr := domainexecution.IdentityFromContext(ctx)
	ownsRun := false
	if identityErr != nil {
		if s.tasks == nil || s.actionActorID == "" {
			return nil, nil, fmt.Errorf("Atlas execution identity: %w", identityErr)
		}
		// One transaction: a refused admission persists no Task, so no
		// Run-less queued Task is left behind.
		task, run, err := s.tasks.CreateAndStartRun(ctx, domaintask.Task{
			Title:    name,
			Route:    domaintask.RouteOperations,
			OwnerID:  s.actionActorID,
			Assignee: s.actionActorID,
			ReadOnly: name == "atlas_recover",
		}, domaintask.SharedRoleContext{CurrentPlan: name})
		if err != nil {
			return nil, nil, fmt.Errorf("start Atlas run: %w", err)
		}
		identity = domainexecution.Identity{TaskID: task.TaskID, RunID: run.RunID}
		// The Run holds the single operations slot until it is closed.
		identityCtx, err := domainexecution.WithIdentity(ctx, task.TaskID, run.RunID, modulecore.NewTraceID())
		if err != nil {
			return nil, nil, s.tasks.AbortStartedRun(ctx, identity.TaskID, identity.RunID, s.actionActorID, err)
		}
		ctx = identityCtx
		ownsRun = true
	}
	action, attempt, err := s.actions.CreateAction(ctx, actionmanager.CreateInput{
		TaskID: identity.TaskID,
		RunID:  identity.RunID,
		Kind:   domainaction.KindVerification,
		Name:   name,
	})
	if err != nil {
		err = fmt.Errorf("create Atlas action: %w", err)
		if ownsRun {
			return nil, nil, s.tasks.AbortStartedRun(ctx, identity.TaskID, identity.RunID, s.actionActorID, err)
		}
		return nil, nil, err
	}
	// finish closes the Attempt and Action, and the Run when this call started
	// it. It returns only a failure to close: the caller joins it with the
	// error of its own operation (finishAction). If the Attempt cannot be
	// closed the Run stays open with it.
	finish := func(operationErr error) error {
		completed := name + " completed"
		if closed, err := s.actions.CloseAttempt(ctx, action.ActionID, attempt.AttemptID, actionmanager.AttemptOutcomeOf(operationErr, completed), nil); !closed {
			return fmt.Errorf("complete Atlas action: %w", err)
		}
		if !ownsRun {
			return nil
		}
		if err := s.tasks.CloseStartedRun(ctx, identity.TaskID, identity.RunID, s.actionActorID, taskmanager.RunOutcomeOf(operationErr, completed), nil); err != nil {
			return fmt.Errorf("complete Atlas run: %w", err)
		}
		return nil
	}
	ownedCtx, err := domainexecution.WithBoundActionAttempt(ctx, action.ActionID, attempt.AttemptID)
	if err != nil {
		return nil, nil, finishAction(err, finish)
	}
	return ownedCtx, finish, nil
}

func finishAction(operationErr error, finish func(error) error) error {
	if finish == nil {
		return operationErr
	}
	finishErr := finish(operationErr)
	if operationErr != nil {
		return errors.Join(operationErr, finishErr)
	}
	return finishErr
}
