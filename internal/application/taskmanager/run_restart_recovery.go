package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// RecoverRunAfterProcessRestart atomically closes an exact active Run whose
// writer generation predates this manager's process lease. Acquiring the new
// writer generation proves that the previous store writer released its OS
// lock; the caller remains responsible for proving that its domain-specific
// external effects are quiescent before invoking this boundary.
//
// A current-generation or already-terminal Run is not an error and returns
// recovered=false. The method never rewrites a foreign, ambiguous, or
// future-generation Run.
func (m *Manager) RecoverRunAfterProcessRestart(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	actorID string,
	status domaintask.Status,
	summary string,
	waitingReason string,
) (domaintask.Task, bool, error) {
	if ctx == nil {
		return domaintask.Task{}, false, errors.New("process restart recovery context is nil")
	}
	if err := ctx.Err(); err != nil {
		return domaintask.Task{}, false, err
	}
	if err := taskID.Validate(); err != nil {
		return domaintask.Task{}, false, fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := runID.Validate(); err != nil {
		return domaintask.Task{}, false, fmt.Errorf("run_id is invalid: %w", err)
	}
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return domaintask.Task{}, false, errors.New("actor_id is required")
	}
	switch status {
	case domaintask.StatusSucceeded, domaintask.StatusFailed, domaintask.StatusCancelled, domaintask.StatusWaiting:
	default:
		return domaintask.Task{}, false, fmt.Errorf("run recovery status is not accepted: %s", status)
	}
	if status == domaintask.StatusWaiting && strings.TrimSpace(waitingReason) == "" {
		return domaintask.Task{}, false, errors.New("waiting reason is required")
	}

	var completed domaintask.Task
	recovered := false
	err := m.taskTransaction(ctx, taskID, func(txManager *Manager) error {
		generation, err := txManager.store.WriterGeneration()
		if err != nil {
			return fmt.Errorf("task writer ownership unavailable: %w", err)
		}
		if generation == 0 {
			return fmt.Errorf("%w: task writer generation is required", ErrRunConflict)
		}
		task, err := txManager.store.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		if err := task.Validate(); err != nil {
			return fmt.Errorf("%w: task record is invalid: %v", ErrRunConflict, err)
		}
		run, err := txManager.validateLatestRunInTransaction(ctx, taskID, runID)
		if err != nil {
			return err
		}
		if task.TaskID != taskID || run.TaskID != taskID {
			return fmt.Errorf("%w: task and run ownership do not match", ErrRunConflict)
		}
		if run.Status != domaintask.RunStatusRunning || run.CompletedAt != nil {
			return nil
		}
		if task.Status != domaintask.StatusRunning {
			return fmt.Errorf("%w: process restart predecessor Task is not active", ErrRunConflict)
		}
		if task.Assignee == "" || run.Assignee == "" || task.Assignee != actorID || run.Assignee != actorID {
			return fmt.Errorf("%w: task, run, and actor ownership do not match", ErrRunConflict)
		}
		if run.WriterGeneration == generation {
			return nil
		}
		if run.WriterGeneration == 0 || run.WriterGeneration > generation {
			return fmt.Errorf("%w: process restart predecessor generation is invalid", ErrRunConflict)
		}

		completed, err = txManager.updateStatusInTransaction(
			ctx,
			taskID,
			status,
			strings.TrimSpace(summary),
			strings.TrimSpace(waitingReason),
			nil,
		)
		if err != nil {
			return err
		}
		recovered = true
		return nil
	})
	if err != nil {
		return domaintask.Task{}, false, err
	}
	return completed, recovered, nil
}
