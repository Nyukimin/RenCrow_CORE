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
		task, err := s.tasks.Create(ctx, domaintask.Task{
			Title:    name,
			Route:    domaintask.RouteOperations,
			OwnerID:  s.actionActorID,
			Assignee: s.actionActorID,
			ReadOnly: name == "atlas_recover",
		}, domaintask.SharedRoleContext{CurrentPlan: name})
		if err != nil {
			return nil, nil, fmt.Errorf("create Atlas task: %w", err)
		}
		run, err := s.tasks.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonFirst)
		if err != nil {
			return nil, nil, fmt.Errorf("start Atlas run: %w", err)
		}
		ctx, err = domainexecution.WithIdentity(ctx, task.TaskID, run.RunID, modulecore.NewTraceID())
		if err != nil {
			return nil, nil, err
		}
		identity = domainexecution.Identity{TaskID: task.TaskID, RunID: run.RunID}
		ownsRun = true
	}
	action, attempt, err := s.actions.CreateAction(ctx, actionmanager.CreateInput{
		TaskID: identity.TaskID,
		RunID:  identity.RunID,
		Kind:   domainaction.KindVerification,
		Name:   name,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create Atlas action: %w", err)
	}
	ownedCtx, err := domainexecution.WithBoundActionAttempt(ctx, action.ActionID, attempt.AttemptID)
	if err != nil {
		return nil, nil, err
	}
	finish := func(operationErr error) error {
		attemptStatus := domainaction.AttemptStatusSucceeded
		actionStatus := domainaction.StatusSucceeded
		summary := name + " completed"
		switch {
		case errors.Is(operationErr, context.Canceled):
			attemptStatus = domainaction.AttemptStatusCancelled
			actionStatus = domainaction.StatusCancelled
			summary = operationErr.Error()
		case errors.Is(operationErr, context.DeadlineExceeded):
			attemptStatus = domainaction.AttemptStatusTimedOut
			actionStatus = domainaction.StatusFailed
			summary = operationErr.Error()
		case operationErr != nil:
			attemptStatus = domainaction.AttemptStatusFailed
			actionStatus = domainaction.StatusFailed
			summary = operationErr.Error()
		}
		completionCtx := context.WithoutCancel(ctx)
		_, _, err := s.actions.CompleteAttempt(completionCtx, action.ActionID, attempt.AttemptID, attemptStatus, actionStatus, summary)
		if err != nil {
			return fmt.Errorf("complete Atlas action: %w", err)
		}
		if ownsRun {
			taskStatus := domaintask.StatusSucceeded
			if actionStatus == domainaction.StatusCancelled {
				taskStatus = domaintask.StatusCancelled
			} else if actionStatus != domainaction.StatusSucceeded {
				taskStatus = domaintask.StatusFailed
			}
			if _, err := s.tasks.CompleteRun(completionCtx, identity.TaskID, identity.RunID, s.actionActorID, taskStatus, summary, ""); err != nil {
				return fmt.Errorf("complete Atlas run: %w", err)
			}
		}
		return nil
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
