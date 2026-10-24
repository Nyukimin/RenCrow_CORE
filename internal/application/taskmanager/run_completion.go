package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const taskRunCompletionAdmissionBytes int64 = 2 << 20

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
	err := m.taskTransaction(ctx, taskID, func(txManager *Manager) error {
		if err := txManager.validateRunExecutionInTransaction(ctx, taskID, runID, actorID); err != nil {
			return err
		}
		if _, err := txManager.validateLatestRunInTransaction(ctx, taskID, runID); err != nil {
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

// AdmitRunCompletion performs the same Task-owner CAS as CompleteRun, but
// returns as soon as an indexed local store has accepted its immutable
// transaction into the shared FIFO. The returned ticket must be waited outside
// the FIFO worker before the caller claims durable completion.
func (m *Manager) AdmitRunCompletion(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	status domaintask.Status,
	summary string,
	waitingReason string,
) (uint64, error) {
	if ctx == nil {
		return 0, errors.New("run completion admission context is nil")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := taskID.Validate(); err != nil {
		return 0, fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := runID.Validate(); err != nil {
		return 0, fmt.Errorf("run_id is invalid: %w", err)
	}
	if err := validateRunCompletionInput(status, waitingReason); err != nil {
		return 0, err
	}
	queue := m.sharedPersistenceQueue()
	if queue == nil {
		return 0, errors.New("Task Manager has no shared persistence queue")
	}
	run, err := m.GetRun(ctx, runID)
	if err != nil {
		return 0, err
	}
	if err := run.Validate(); err != nil {
		return 0, fmt.Errorf("run record is invalid: %w", err)
	}
	if run.TaskID != taskID || run.RunID != runID {
		return 0, fmt.Errorf("task and run ownership do not match")
	}
	if run.Status != domaintask.RunStatusRunning {
		return 0, m.VerifyRunCompletion(ctx, taskID, runID, run.Assignee, status)
	}
	if strings.TrimSpace(run.Assignee) == "" {
		return 0, errors.New("actor_id is required")
	}
	admission, err := persistencequeue.Admit(queue, 1, taskRunCompletionAdmissionBytes)
	if err != nil {
		return 0, fmt.Errorf("reserve Task Run completion persistence capacity: %w", err)
	}
	defer func() { _ = admission.Release() }()
	admittedContext := persistencequeue.WithAdmission(ctx, admission)
	return m.taskTransactionWithTicket(admittedContext, taskID, func(txManager *Manager) error {
		if err := txManager.validateRunExecutionInTransaction(admittedContext, taskID, runID, run.Assignee); err != nil {
			return err
		}
		if _, err := txManager.validateLatestRunInTransaction(admittedContext, taskID, runID); err != nil {
			return err
		}
		_, err := txManager.updateStatusInTransaction(admittedContext, taskID, status, summary, waitingReason, nil)
		return err
	})
}

// WaitRunCompletion waits for the FIFO ticket and confirms that the Task owner
// durably contains the requested terminal pair. It must be called outside the
// queue's Apply callback.
func (m *Manager) WaitRunCompletion(ctx context.Context, ticket uint64, taskID modulecore.TaskID, runID modulecore.RunID, status domaintask.Status) error {
	if ctx == nil {
		return errors.New("run completion wait context is nil")
	}
	queue := m.sharedPersistenceQueue()
	if queue == nil {
		return errors.New("Task Manager has no shared persistence queue")
	}
	if ticket != 0 {
		if err := queue.Wait(ctx, ticket); err != nil {
			return err
		}
	}
	run, err := m.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if err := run.Validate(); err != nil {
		return fmt.Errorf("run record is invalid after persistence: %w", err)
	}
	return m.VerifyRunCompletion(ctx, taskID, runID, run.Assignee, status)
}

func validateRunCompletionInput(status domaintask.Status, waitingReason string) error {
	switch status {
	case domaintask.StatusSucceeded, domaintask.StatusFailed, domaintask.StatusCancelled, domaintask.StatusWaiting:
	default:
		return fmt.Errorf("run completion status is not accepted: %s", status)
	}
	if status == domaintask.StatusWaiting && strings.TrimSpace(waitingReason) == "" {
		return errors.New("waiting reason is required")
	}
	return nil
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

// validateLatestRunInTransaction checks the complete folded Run history for a
// Task. Callers use the returned Run only as the history-selected candidate;
// the caller remains responsible for its status and actor-specific fences.
func (m *Manager) validateLatestRunInTransaction(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID) (domaintask.Run, error) {
	if ctx == nil {
		return domaintask.Run{}, errors.New("latest run validation context is nil")
	}
	if err := ctx.Err(); err != nil {
		return domaintask.Run{}, err
	}
	if m == nil || m.store == nil {
		return domaintask.Run{}, errors.New("task manager store is unavailable")
	}
	runs, err := m.store.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
	if err != nil {
		return domaintask.Run{}, err
	}
	if len(runs) == 0 {
		return domaintask.Run{}, fmt.Errorf("%w: task has no persisted runs", ErrRunConflict)
	}

	seen := make(map[modulecore.RunID]struct{}, len(runs))
	var selected domaintask.Run
	found := false
	for index := range runs {
		candidate := runs[index]
		if err := candidate.Validate(); err != nil {
			return domaintask.Run{}, fmt.Errorf("%w: run record is invalid: %v", ErrRunConflict, err)
		}
		if candidate.TaskID != taskID {
			return domaintask.Run{}, fmt.Errorf("%w: run %s belongs to task %s, want %s", ErrRunConflict, candidate.RunID, candidate.TaskID, taskID)
		}
		if _, exists := seen[candidate.RunID]; exists {
			return domaintask.Run{}, fmt.Errorf("%w: duplicate run %s", ErrRunConflict, candidate.RunID)
		}
		seen[candidate.RunID] = struct{}{}
		if candidate.RunID == runID {
			selected = candidate
			found = true
		}
	}
	if !found {
		return domaintask.Run{}, fmt.Errorf("%w: requested run is absent from task history", ErrRunConflict)
	}

	for index := range runs {
		candidate := runs[index]
		if candidate.RunID == selected.RunID {
			continue
		}
		if candidate.StartedAt.Equal(selected.StartedAt) {
			return domaintask.Run{}, fmt.Errorf("%w: latest run is ambiguous at started_at %s", ErrRunConflict, selected.StartedAt)
		}
		if candidate.StartedAt.After(selected.StartedAt) {
			return domaintask.Run{}, fmt.Errorf("%w: requested run %s is not the latest run", ErrRunConflict, selected.RunID)
		}
		if candidate.Status == domaintask.RunStatusRunning {
			return domaintask.Run{}, fmt.Errorf("%w: task has another active run %s", ErrRunConflict, candidate.RunID)
		}
	}
	if err := ctx.Err(); err != nil {
		return domaintask.Run{}, err
	}
	return selected, nil
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

	selected, err := m.validateLatestRunInTransaction(ctx, taskID, runID)
	if err != nil {
		return err
	}
	if selected.TaskID != run.TaskID || selected.Assignee != run.Assignee || selected.Status != run.Status || !selected.StartedAt.Equal(run.StartedAt) {
		return fmt.Errorf("%w: requested run changed within task snapshot", ErrRunConflict)
	}
	return nil
}
