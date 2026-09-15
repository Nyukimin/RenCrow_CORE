package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	memorypromotionapp "github.com/Nyukimin/RenCrow_CORE/internal/application/memorypromotion"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type actionMemoryPromotionRunner struct {
	inner   memoryPromotionRunner
	actions *actionmanager.Manager
	tasks   *taskmanager.Manager
	actorID string
}

func newActionMemoryPromotionRunner(inner memoryPromotionRunner, actions *actionmanager.Manager, tasks *taskmanager.Manager, actorID string) (memoryPromotionRunner, error) {
	if inner == nil {
		return nil, errors.New("Memory Promotion runner is required")
	}
	if actions == nil || tasks == nil || actorID == "" {
		return nil, errors.New("Memory Promotion execution owners are required")
	}
	return &actionMemoryPromotionRunner{inner: inner, actions: actions, tasks: tasks, actorID: actorID}, nil
}

func (r *actionMemoryPromotionRunner) RunOne(ctx context.Context) (memorypromotionapp.RunResult, error) {
	task, err := r.tasks.Create(ctx, domaintask.Task{
		Title:    "Memory Promotion",
		Route:    domaintask.RouteResearch,
		OwnerID:  r.actorID,
		Assignee: r.actorID,
		ReadOnly: false,
	}, domaintask.SharedRoleContext{CurrentPlan: "promote canonical memory candidates"})
	if err != nil {
		return memorypromotionapp.RunResult{}, err
	}
	run, err := r.tasks.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		return memorypromotionapp.RunResult{}, err
	}
	ownedCtx, err := domainexecution.WithIdentity(ctx, task.TaskID, run.RunID, modulecore.NewTraceID())
	if err != nil {
		return memorypromotionapp.RunResult{}, err
	}
	action, attempt, err := r.actions.CreateAction(ownedCtx, actionmanager.CreateInput{
		TaskID: task.TaskID,
		RunID:  run.RunID,
		Kind:   domainaction.KindMemoryPromotion,
		Name:   "memory_promotion",
	})
	if err != nil {
		return memorypromotionapp.RunResult{}, err
	}
	ownedCtx, err = domainexecution.WithChildBoundActionAttempt(ownedCtx, action.ActionID, attempt.AttemptID)
	if err != nil {
		return memorypromotionapp.RunResult{}, err
	}
	result, operationErr := r.inner.RunOne(ownedCtx)
	return result, r.complete(ctx, task.TaskID, run.RunID, action, attempt, operationErr)
}

func (r *actionMemoryPromotionRunner) complete(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, action domainaction.Action, attempt domainaction.Attempt, operationErr error) error {
	attemptStatus := domainaction.AttemptStatusSucceeded
	actionStatus := domainaction.StatusSucceeded
	taskStatus := domaintask.StatusSucceeded
	summary := "Memory Promotion completed"
	switch {
	case errors.Is(operationErr, context.Canceled):
		attemptStatus, actionStatus, taskStatus, summary = domainaction.AttemptStatusCancelled, domainaction.StatusCancelled, domaintask.StatusCancelled, operationErr.Error()
	case errors.Is(operationErr, context.DeadlineExceeded):
		attemptStatus, actionStatus, taskStatus, summary = domainaction.AttemptStatusTimedOut, domainaction.StatusFailed, domaintask.StatusFailed, operationErr.Error()
	case operationErr != nil:
		attemptStatus, actionStatus, taskStatus, summary = domainaction.AttemptStatusFailed, domainaction.StatusFailed, domaintask.StatusFailed, operationErr.Error()
	}
	completionCtx := context.WithoutCancel(ctx)
	if _, _, err := r.actions.CompleteAttempt(completionCtx, action.ActionID, attempt.AttemptID, attemptStatus, actionStatus, summary); err != nil {
		return errors.Join(operationErr, fmt.Errorf("complete Memory Promotion action: %w", err))
	}
	if _, err := r.tasks.CompleteRun(completionCtx, taskID, runID, r.actorID, taskStatus, summary, ""); err != nil {
		return errors.Join(operationErr, fmt.Errorf("complete Memory Promotion run: %w", err))
	}
	return operationErr
}
