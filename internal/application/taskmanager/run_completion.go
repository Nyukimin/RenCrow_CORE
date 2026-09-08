package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// CompleteRun closes one active Run and updates its owning Task as one
// CAS-like transaction. It validates the current owner at commit time; it does
// not reserve a lifetime execution fence for downstream I/O.
func (m *Manager) CompleteRun(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	actorID string,
	status domaintask.Status,
	summary string,
	waitingReason string,
) (domaintask.Task, error) {
	if err := taskID.Validate(); err != nil {
		return domaintask.Task{}, fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := runID.Validate(); err != nil {
		return domaintask.Task{}, fmt.Errorf("run_id is invalid: %w", err)
	}
	if strings.TrimSpace(actorID) == "" {
		return domaintask.Task{}, fmt.Errorf("actor_id is required")
	}
	switch status {
	case domaintask.StatusSucceeded, domaintask.StatusFailed, domaintask.StatusCancelled, domaintask.StatusWaiting:
	default:
		return domaintask.Task{}, fmt.Errorf("run completion status is not accepted: %s", status)
	}

	var completed domaintask.Task
	err := m.transaction(ctx, func(txManager *Manager) error {
		if err := txManager.validateRunExecutionInTransaction(ctx, taskID, runID, actorID); err != nil {
			return err
		}
		var err error
		completed, err = txManager.updateStatusInTransaction(ctx, taskID, status, summary, waitingReason, nil)
		return err
	})
	if err != nil {
		return domaintask.Task{}, err
	}
	return completed, nil
}

// VerifyRunCompletion validates an already terminal Run and its owning Task
// in one owner read transaction. It is a read-only completion CAS check: it
// does not change either record, and it does not require the current writer
// generation because the terminal state is already persisted.
func (m *Manager) VerifyRunCompletion(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	actorID string,
	status domaintask.Status,
) error {
	if ctx == nil {
		return errors.New("run completion verification context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := taskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := runID.Validate(); err != nil {
		return fmt.Errorf("run_id is invalid: %w", err)
	}
	if strings.TrimSpace(actorID) == "" {
		return errors.New("actor_id is required")
	}
	switch status {
	case domaintask.StatusSucceeded, domaintask.StatusFailed, domaintask.StatusCancelled, domaintask.StatusWaiting:
	default:
		return fmt.Errorf("run completion status is not accepted: %s", status)
	}
	expectedRunStatus, _ := runStatusForTaskStatus(status)

	return m.readTransaction(ctx, func(txManager *Manager) error {
		return txManager.verifyRunCompletionInTransaction(ctx, taskID, runID, actorID, status, expectedRunStatus)
	})
}

func (m *Manager) verifyRunCompletionInTransaction(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	actorID string,
	status domaintask.Status,
	expectedRunStatus domaintask.RunStatus,
) error {
	task, err := m.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task.TaskID != taskID {
		return fmt.Errorf("%w: task identity does not match requested task", ErrRunConflict)
	}
	if err := task.Validate(); err != nil {
		return fmt.Errorf("%w: task record is invalid: %v", ErrRunConflict, err)
	}

	run, err := m.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if err := run.Validate(); err != nil {
		return fmt.Errorf("%w: run record is invalid: %v", ErrRunConflict, err)
	}
	if run.RunID != runID || run.TaskID != taskID || run.TaskID != task.TaskID {
		return fmt.Errorf("%w: task and run ownership do not match", ErrRunConflict)
	}
	if strings.TrimSpace(task.Assignee) == "" || strings.TrimSpace(run.Assignee) == "" || task.Assignee != run.Assignee || task.Assignee != actorID {
		return fmt.Errorf("%w: task, run, and actor ownership do not match", ErrRunConflict)
	}
	if task.Status != status {
		return fmt.Errorf("%w: task status is %s, want %s", ErrRunConflict, task.Status, status)
	}
	if run.Status != expectedRunStatus {
		return fmt.Errorf("%w: run status is %s, want %s", ErrRunConflict, run.Status, expectedRunStatus)
	}

	runs, err := m.store.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return fmt.Errorf("%w: task has no persisted runs", ErrRunConflict)
	}

	seen := make(map[modulecore.RunID]struct{}, len(runs))
	var selected *domaintask.Run
	for index := range runs {
		candidate := &runs[index]
		if err := candidate.Validate(); err != nil {
			return fmt.Errorf("%w: run record is invalid: %v", ErrRunConflict, err)
		}
		if candidate.TaskID != taskID {
			return fmt.Errorf("%w: run %s belongs to task %s, want %s", ErrRunConflict, candidate.RunID, candidate.TaskID, taskID)
		}
		if _, exists := seen[candidate.RunID]; exists {
			return fmt.Errorf("%w: duplicate run %s", ErrRunConflict, candidate.RunID)
		}
		seen[candidate.RunID] = struct{}{}
		if candidate.RunID == runID {
			selected = candidate
		}
	}
	if selected == nil {
		return fmt.Errorf("%w: requested run is absent from task history", ErrRunConflict)
	}
	if selected.TaskID != run.TaskID || selected.Assignee != run.Assignee || selected.Status != run.Status || !selected.StartedAt.Equal(run.StartedAt) {
		return fmt.Errorf("%w: requested run changed within task snapshot", ErrRunConflict)
	}

	for index := range runs {
		candidate := &runs[index]
		if candidate.RunID == selected.RunID {
			continue
		}
		if candidate.StartedAt.Equal(selected.StartedAt) {
			return fmt.Errorf("%w: latest run is ambiguous at started_at %s", ErrRunConflict, selected.StartedAt)
		}
		if candidate.StartedAt.After(selected.StartedAt) {
			return fmt.Errorf("%w: requested run %s is not the latest run", ErrRunConflict, selected.RunID)
		}
		if candidate.Status == domaintask.RunStatusRunning {
			return fmt.Errorf("%w: task has another active run %s", ErrRunConflict, candidate.RunID)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
