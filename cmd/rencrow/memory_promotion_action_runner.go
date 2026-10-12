package main

import (
	"context"
	"errors"

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
	task, run, err := r.tasks.CreateAndStartRun(ctx, domaintask.Task{
		Title:    "Memory Promotion",
		Route:    domaintask.RouteResearch,
		OwnerID:  r.actorID,
		Assignee: r.actorID,
		ReadOnly: false,
	}, domaintask.SharedRoleContext{CurrentPlan: "promote canonical memory candidates"})
	if err != nil {
		return memorypromotionapp.RunResult{}, err
	}
	// From here on the Task is running with its first Run and holds the single
	// long-research slot, so every failure that stops the run before the inner
	// runner executes must close it. A Run left open defers every later Memory
	// Promotion until the next restart.
	ownedCtx, err := domainexecution.WithIdentity(ctx, task.TaskID, run.RunID, modulecore.NewTraceID())
	if err != nil {
		return memorypromotionapp.RunResult{}, r.tasks.AbortStartedRun(ctx, task.TaskID, run.RunID, r.actorID, err)
	}
	action, attempt, err := r.actions.CreateAction(ownedCtx, actionmanager.CreateInput{
		TaskID: task.TaskID,
		RunID:  run.RunID,
		Kind:   domainaction.KindMemoryPromotion,
		Name:   "memory_promotion",
	})
	if err != nil {
		return memorypromotionapp.RunResult{}, r.tasks.AbortStartedRun(ctx, task.TaskID, run.RunID, r.actorID, err)
	}
	ownedCtx, err = domainexecution.WithChildBoundActionAttempt(ownedCtx, action.ActionID, attempt.AttemptID)
	if err != nil {
		// The Action and Attempt exist, so they are closed together with the Run.
		return memorypromotionapp.RunResult{}, r.complete(ctx, task.TaskID, run.RunID, action, attempt, err)
	}
	result, operationErr := r.inner.RunOne(ownedCtx)
	return result, r.complete(ctx, task.TaskID, run.RunID, action, attempt, operationErr)
}

// complete closes the Attempt and Action, then the Run and Task. If the Attempt
// cannot be closed the Run stays open with it, and the error says so.
func (r *actionMemoryPromotionRunner) complete(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, action domainaction.Action, attempt domainaction.Attempt, operationErr error) error {
	const completed = "Memory Promotion completed"
	if closed, err := r.actions.CloseAttempt(ctx, action.ActionID, attempt.AttemptID, actionmanager.AttemptOutcomeOf(operationErr, completed), operationErr); !closed {
		return err
	}
	return r.tasks.CloseStartedRun(ctx, taskID, runID, r.actorID, taskmanager.RunOutcomeOf(operationErr, completed), operationErr)
}

// memoryPromotionRunLeftOpen reports whether err carries a failed terminal write:
// the Run is still running and holds the long-research slot. The Attempt's own
// marker counts, because the Run is not closed after a failed Attempt.
func memoryPromotionRunLeftOpen(err error) bool {
	return errors.Is(err, taskmanager.ErrStartedRunNotClosed) || errors.Is(err, actionmanager.ErrAttemptNotClosed)
}
