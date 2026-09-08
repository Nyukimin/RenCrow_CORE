package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// StartRunFromCheckpoint issues one successor Run only when the supplied Run
// is still the unique latest predecessor for the same Task. The predecessor
// check and the successor append share the task owner's write transaction, so a
// concurrent caller using the same predecessor loses with ErrRunConflict and
// cannot mutate the Task or Run history. The checkpoint digest is immutable
// source evidence on the successor; it is not a Run identity or parent link.
func (m *Manager) StartRunFromCheckpoint(
	ctx context.Context,
	taskID modulecore.TaskID,
	expectedRunID modulecore.RunID,
	actorID string,
	reason domaintask.RunStartReason,
	checkpointSHA256 string,
) (domaintask.Run, error) {
	if ctx == nil {
		return domaintask.Run{}, errors.New("checkpoint start context is nil")
	}
	if err := ctx.Err(); err != nil {
		return domaintask.Run{}, err
	}
	if err := taskID.Validate(); err != nil {
		return domaintask.Run{}, fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := expectedRunID.Validate(); err != nil {
		return domaintask.Run{}, fmt.Errorf("expected_run_id is invalid: %w", err)
	}
	if strings.TrimSpace(actorID) == "" {
		return domaintask.Run{}, errors.New("actor_id is required")
	}
	if reason != domaintask.RunStartReasonCheckpointResume && reason != domaintask.RunStartReasonExplicitRerun {
		return domaintask.Run{}, fmt.Errorf("checkpoint start reason is not accepted: %s", reason)
	}
	if strings.TrimSpace(checkpointSHA256) == "" {
		return domaintask.Run{}, errors.New("checkpoint_sha256 is required")
	}
	if err := domaintask.ValidateStartCheckpointSHA256(checkpointSHA256); err != nil {
		return domaintask.Run{}, err
	}

	var issued domaintask.Run
	err := m.transaction(ctx, func(txManager *Manager) error {
		predecessor, err := validateCheckpointPredecessor(ctx, txManager, taskID, expectedRunID, actorID, reason)
		if err != nil {
			return err
		}
		_, issued, err = txManager.startWithReasonAndCheckpoint(ctx, taskID, reason, checkpointSHA256)
		if err != nil {
			return err
		}
		return validateCheckpointSuccessorTimestamp(predecessor, issued)
	})
	if err != nil {
		return domaintask.Run{}, err
	}
	return issued, nil
}

func validateCheckpointPredecessor(
	ctx context.Context,
	manager *Manager,
	taskID modulecore.TaskID,
	expectedRunID modulecore.RunID,
	actorID string,
	reason domaintask.RunStartReason,
) (domaintask.Run, error) {
	if manager == nil || manager.store == nil {
		return domaintask.Run{}, errors.New("task manager store is unavailable")
	}
	task, err := manager.store.GetTask(ctx, taskID)
	if err != nil {
		return domaintask.Run{}, err
	}
	if task.TaskID != taskID {
		return domaintask.Run{}, fmt.Errorf("%w: task identity does not match requested task", ErrRunConflict)
	}
	if err := task.Validate(); err != nil {
		return domaintask.Run{}, fmt.Errorf("%w: task record is invalid: %v", ErrRunConflict, err)
	}
	if strings.TrimSpace(task.Assignee) == "" || task.Assignee != actorID {
		return domaintask.Run{}, fmt.Errorf("%w: task and actor ownership do not match", ErrRunConflict)
	}

	runs, err := manager.store.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
	if err != nil {
		return domaintask.Run{}, err
	}
	if len(runs) == 0 {
		return domaintask.Run{}, fmt.Errorf("%w: task has no predecessor Run", ErrRunConflict)
	}

	seen := make(map[modulecore.RunID]struct{}, len(runs))
	var predecessor domaintask.Run
	foundPredecessor := false
	latest := runs[0]
	latestCount := 0
	activeCount := 0
	for _, run := range runs {
		if err := run.Validate(); err != nil {
			return domaintask.Run{}, fmt.Errorf("%w: run record is invalid: %v", ErrRunConflict, err)
		}
		if run.TaskID != taskID {
			return domaintask.Run{}, fmt.Errorf("%w: run %s belongs to task %s, want %s", ErrRunConflict, run.RunID, run.TaskID, taskID)
		}
		if _, exists := seen[run.RunID]; exists {
			return domaintask.Run{}, fmt.Errorf("%w: duplicate run %s", ErrRunConflict, run.RunID)
		}
		seen[run.RunID] = struct{}{}
		if run.RunID == expectedRunID {
			predecessor = run
			foundPredecessor = true
		}
		if run.Status == domaintask.RunStatusRunning {
			activeCount++
		}
		if run.StartedAt.After(latest.StartedAt) {
			latest = run
			latestCount = 1
		} else if run.StartedAt.Equal(latest.StartedAt) {
			latestCount++
		}
	}
	if !foundPredecessor {
		return domaintask.Run{}, fmt.Errorf("%w: expected predecessor Run is absent", ErrRunConflict)
	}
	if latestCount != 1 {
		return domaintask.Run{}, fmt.Errorf("%w: latest predecessor Run is ambiguous at started_at %s", ErrRunConflict, latest.StartedAt)
	}
	if latest.RunID != expectedRunID {
		return domaintask.Run{}, fmt.Errorf("%w: expected predecessor Run is not the latest Run", ErrRunConflict)
	}
	if activeCount != 0 {
		return domaintask.Run{}, fmt.Errorf("%w: task has an active Run", ErrRunConflict)
	}
	if strings.TrimSpace(predecessor.Assignee) == "" || predecessor.Assignee != actorID || predecessor.Assignee != task.Assignee {
		return domaintask.Run{}, fmt.Errorf("%w: task, predecessor, and actor ownership do not match", ErrRunConflict)
	}
	if !domaintask.CanStartRunFromCheckpoint(predecessor.Status, reason) {
		return domaintask.Run{}, fmt.Errorf("%w: predecessor status %s is not valid for %s", ErrRunConflict, predecessor.Status, reason)
	}
	if err := ctx.Err(); err != nil {
		return domaintask.Run{}, err
	}
	return predecessor, nil
}

func validateCheckpointSuccessorTimestamp(predecessor, successor domaintask.Run) error {
	if !successor.StartedAt.After(predecessor.StartedAt) {
		return fmt.Errorf("%w: successor Run started_at %s is not after predecessor started_at %s", ErrRunConflict, successor.StartedAt, predecessor.StartedAt)
	}
	if predecessor.CompletedAt != nil && successor.StartedAt.Before(*predecessor.CompletedAt) {
		return fmt.Errorf("%w: successor Run started_at %s precedes predecessor completed_at %s", ErrRunConflict, successor.StartedAt, predecessor.CompletedAt)
	}
	return nil
}

// ExecuteRunEffect admits one synchronous external leaf effect while the
// task-store read transaction remains open. The callback must honor ctx, stay
// synchronous, and must not call the Task owner, complete the Run, or launch
// detached work. This fences Task-store supersession until the callback
// returns; a process crash or a remote effect cannot be cancelled by this API.
func (m *Manager) ExecuteRunEffect(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	actorID string,
	effect func(context.Context) error,
) error {
	if ctx == nil {
		return errors.New("execution effect context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if m == nil {
		return errors.New("task manager is nil")
	}
	if m.store == nil {
		return errors.New("task manager store is unavailable")
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
	if effect == nil {
		return errors.New("execution effect callback is required")
	}
	return m.readTransaction(ctx, func(txManager *Manager) error {
		if err := txManager.validateRunExecutionInTransaction(ctx, taskID, runID, actorID); err != nil {
			return err
		}
		return effect(ctx)
	})
}
