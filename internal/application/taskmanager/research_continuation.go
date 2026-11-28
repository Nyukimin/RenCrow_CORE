package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"

	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var ErrResearchContinuationConflict = errors.New("Research continuation no longer matches its Task owner")

// StartResearchContinuation atomically rechecks the authenticated user's
// immutable Research data owner and the exact completed Task Run before using
// the Task Manager's canonical Run-start path. The actor is server-resolved
// from the Task and prior Run; callers must not source it from request input.
func (m *Manager) StartResearchContinuation(
	ctx context.Context,
	taskID modulecore.TaskID,
	previousRunID modulecore.RunID,
	ownerID string,
	actorID string,
) (domaintask.Run, error) {
	if m == nil || m.store == nil || ctx == nil {
		return domaintask.Run{}, errors.New("Research continuation owner is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return domaintask.Run{}, err
	}
	if err := taskID.Validate(); err != nil {
		return domaintask.Run{}, fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := previousRunID.Validate(); err != nil {
		return domaintask.Run{}, fmt.Errorf("previous run_id is invalid: %w", err)
	}
	if err := domainsession.ValidateOwnerID(ownerID); err != nil {
		return domaintask.Run{}, fmt.Errorf("Research user owner is invalid: %w", err)
	}
	if strings.TrimSpace(actorID) == "" || actorID != strings.TrimSpace(actorID) {
		return domaintask.Run{}, errors.New("Research continuation actor is invalid")
	}

	start := func(txManager *Manager) (domaintask.Task, domaintask.Run, error) {
		task, err := txManager.store.GetTask(ctx, taskID)
		if err != nil {
			return domaintask.Task{}, domaintask.Run{}, err
		}
		if domaintask.IsOwnerManagedTask(task) || task.Activity != nil && task.Activity.Passive ||
			!domaintask.ResearchResultDataScopeMatches(task, "user", ownerID) ||
			task.Assignee != actorID || task.Status == domaintask.StatusRunning || task.ResearchResult == nil {
			return domaintask.Task{}, domaintask.Run{}, ErrResearchContinuationConflict
		}
		projection := task.ResearchResult
		if err := projection.Validate(task); err != nil || projection.DataScope != "user" || projection.OwnerID != ownerID ||
			projection.ActorID != actorID || projection.ExecutionRunID != previousRunID {
			return domaintask.Task{}, domaintask.Run{}, ErrResearchContinuationConflict
		}

		runs, err := txManager.store.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
		if err != nil {
			return domaintask.Task{}, domaintask.Run{}, err
		}
		if len(runs) == 0 {
			return domaintask.Task{}, domaintask.Run{}, ErrResearchContinuationConflict
		}
		latest := runs[0]
		seen := make(map[modulecore.RunID]struct{}, len(runs))
		for _, run := range runs {
			if err := run.Validate(); err != nil || run.TaskID != taskID {
				return domaintask.Task{}, domaintask.Run{}, ErrResearchContinuationConflict
			}
			if _, exists := seen[run.RunID]; exists {
				return domaintask.Task{}, domaintask.Run{}, ErrResearchContinuationConflict
			}
			seen[run.RunID] = struct{}{}
			if run.StartedAt.Equal(latest.StartedAt) && run.RunID != latest.RunID {
				return domaintask.Task{}, domaintask.Run{}, ErrResearchContinuationConflict
			}
			if run.StartedAt.After(latest.StartedAt) {
				latest = run
			}
		}
		if latest.RunID != previousRunID || latest.Status == domaintask.RunStatusRunning {
			return domaintask.Task{}, domaintask.Run{}, ErrResearchContinuationConflict
		}

		var reason domaintask.RunStartReason
		switch {
		case projection.Status == "waiting_input" && task.Status == domaintask.StatusWaiting && latest.Status == domaintask.RunStatusWaiting:
			reason = domaintask.RunStartReasonCheckpointResume
		case (projection.Status == "partial" || projection.Status == "failed") && task.Status == domaintask.StatusFailed && latest.Status == domaintask.RunStatusFailed:
			reason = domaintask.RunStartReasonExplicitRerun
		default:
			return domaintask.Task{}, domaintask.Run{}, ErrResearchContinuationConflict
		}
		return txManager.startWithReason(ctx, taskID, reason)
	}

	candidate, err := m.store.GetTask(ctx, taskID)
	if err != nil {
		return domaintask.Run{}, err
	}
	if candidate.Activity != nil && candidate.Activity.Passive {
		return domaintask.Run{}, fmt.Errorf("passive activity Tasks do not issue Runs: %w", domaintask.ErrCurrentActivityInvalid)
	}
	_, activityOwnerAvailable := m.store.(domaintask.ActivityTaskTransaction)
	if activity, eligible := activityForRootTask(candidate); eligible && activityOwnerAvailable {
		preserveParent, parentErr := m.childSharesCurrentAncestor(ctx, candidate)
		if parentErr != nil {
			return domaintask.Run{}, parentErr
		}
		if !preserveParent {
			_, run, err := m.startExistingActivityTask(ctx, taskID, activity, start)
			return run, err
		}
	}
	if _, eligible := activityForRootTask(candidate); eligible && !activityOwnerAvailable {
		preserveParent, parentErr := m.childSharesCurrentAncestor(ctx, candidate)
		if parentErr != nil {
			return domaintask.Run{}, parentErr
		}
		if !preserveParent {
			if err := m.requireDormantActivityFeature(ctx, candidate.Assignee, candidate); err != nil {
				return domaintask.Run{}, err
			}
		}
	}
	var started domaintask.Run
	err = m.taskTransaction(ctx, taskID, func(txManager *Manager) error {
		_, run, err := start(txManager)
		started = run
		return err
	})
	if err != nil {
		return domaintask.Run{}, err
	}
	return started, nil
}
