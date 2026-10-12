package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	memorypromotionapp "github.com/Nyukimin/RenCrow_CORE/internal/application/memorypromotion"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// memoryPromotionTerminalWriteTimeout is the budget of one terminal write of a
// Memory Promotion run (Attempt, Action, or Task and Run). It is detached from
// the caller's context, because a cancelled or expired tick must still record
// how the run ended, so it must outlast the stores: every Task and Action
// transaction reads the whole history, and on the production host one write
// took 7 to 11 seconds (2026-10-12). It is the same rule and value as the other
// terminal writes (Heartbeat, OPS ingress, Tool attempts). Each write gets its
// own budget.
const memoryPromotionTerminalWriteTimeout = 60 * time.Second

// errMemoryPromotionRunNotClosed marks an error that carries a failed terminal
// write: the Task is still running and holds the long-research slot. The worker
// does not report a plain cancellation as a failure, and a cancelled run is the
// likely moment for the terminal write to fail too, so the worker checks for this
// marker before it stays silent.
var errMemoryPromotionRunNotClosed = errors.New("Memory Promotion run was not closed")

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
		return memorypromotionapp.RunResult{}, r.closeRunWithoutAction(ctx, task.TaskID, run.RunID, err)
	}
	action, attempt, err := r.actions.CreateAction(ownedCtx, actionmanager.CreateInput{
		TaskID: task.TaskID,
		RunID:  run.RunID,
		Kind:   domainaction.KindMemoryPromotion,
		Name:   "memory_promotion",
	})
	if err != nil {
		return memorypromotionapp.RunResult{}, r.closeRunWithoutAction(ctx, task.TaskID, run.RunID, err)
	}
	ownedCtx, err = domainexecution.WithChildBoundActionAttempt(ownedCtx, action.ActionID, attempt.AttemptID)
	if err != nil {
		// The Action and Attempt exist, so they are closed together with the Run.
		return memorypromotionapp.RunResult{}, r.complete(ctx, task.TaskID, run.RunID, action, attempt, err)
	}
	result, operationErr := r.inner.RunOne(ownedCtx)
	return result, r.complete(ctx, task.TaskID, run.RunID, action, attempt, operationErr)
}

// closeRunWithoutAction closes a Run that was issued but never got an Action.
// The write is detached from ctx and bounded. If it fails, the failure is
// returned together with the cause, never dropped: the Task stays running and
// the caller has to know.
func (r *actionMemoryPromotionRunner) closeRunWithoutAction(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, cause error) error {
	_, _, taskStatus, summary := memoryPromotionTerminalState(cause)
	closeCtx, cancel := memoryPromotionTerminalContext(ctx)
	defer cancel()
	if _, err := r.tasks.CompleteRun(closeCtx, taskID, runID, r.actorID, taskStatus, summary, ""); err != nil {
		return errors.Join(cause, fmt.Errorf("%w: close Memory Promotion run: %w", errMemoryPromotionRunNotClosed, err))
	}
	return cause
}

func (r *actionMemoryPromotionRunner) complete(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, action domainaction.Action, attempt domainaction.Attempt, operationErr error) error {
	attemptStatus, actionStatus, taskStatus, summary := memoryPromotionTerminalState(operationErr)
	attemptCtx, cancelAttempt := memoryPromotionTerminalContext(ctx)
	defer cancelAttempt()
	if _, _, err := r.actions.CompleteAttempt(attemptCtx, action.ActionID, attempt.AttemptID, attemptStatus, actionStatus, summary); err != nil {
		return errors.Join(operationErr, fmt.Errorf("%w: complete Memory Promotion action: %w", errMemoryPromotionRunNotClosed, err))
	}
	runCtx, cancelRun := memoryPromotionTerminalContext(ctx)
	defer cancelRun()
	if _, err := r.tasks.CompleteRun(runCtx, taskID, runID, r.actorID, taskStatus, summary, ""); err != nil {
		return errors.Join(operationErr, fmt.Errorf("%w: complete Memory Promotion run: %w", errMemoryPromotionRunNotClosed, err))
	}
	return operationErr
}

// memoryPromotionTerminalState maps how the run ended to the terminal states of
// its Attempt, Action, and Task. A nil error is success; a cancelled caller is
// cancelled; an expired caller or any other error is a failure.
func memoryPromotionTerminalState(operationErr error) (domainaction.AttemptStatus, domainaction.Status, domaintask.Status, string) {
	switch {
	case errors.Is(operationErr, context.Canceled):
		return domainaction.AttemptStatusCancelled, domainaction.StatusCancelled, domaintask.StatusCancelled, operationErr.Error()
	case errors.Is(operationErr, context.DeadlineExceeded):
		return domainaction.AttemptStatusTimedOut, domainaction.StatusFailed, domaintask.StatusFailed, operationErr.Error()
	case operationErr != nil:
		return domainaction.AttemptStatusFailed, domainaction.StatusFailed, domaintask.StatusFailed, operationErr.Error()
	}
	return domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, domaintask.StatusSucceeded, "Memory Promotion completed"
}

// memoryPromotionTerminalContext detaches a terminal write from the caller's
// cancellation and gives it a finite budget of its own.
func memoryPromotionTerminalContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), memoryPromotionTerminalWriteTimeout)
}
