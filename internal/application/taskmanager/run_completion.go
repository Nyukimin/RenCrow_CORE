package taskmanager

import (
	"context"
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
