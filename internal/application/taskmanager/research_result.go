package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var ErrResearchResultConflict = errors.New("Research result does not match the active Task Run")

// ResearchResultVerificationCheck verifies the exact Engine result/CAS readback.
// It runs before the Task transaction; it must not be treated as Task authority.
type ResearchResultVerificationCheck func(context.Context, domaintask.ResearchResultProjection) error

// RecordResearchResult stores deterministic Research facts after Engine
// readback. The Task transaction rechecks that the supplied Run is still the
// latest active Run and is owned by actorID before writing the projection.
func (m *Manager) RecordResearchResult(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	actorID string,
	projection domaintask.ResearchResultProjection,
	verify ResearchResultVerificationCheck,
	authorizePrivateRead domaintask.TaskReadAuthorizationCheck,
	authorizePrivateWrite domaintask.TaskWriteAuthorizationCheck,
) error {
	if ctx == nil || verify == nil {
		return errors.New("Research result context and Engine verification are required")
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
	if strings.TrimSpace(actorID) == "" || actorID != strings.TrimSpace(actorID) {
		return errors.New("actor_id is required")
	}
	if projection.TaskID != taskID || projection.ExecutionRunID != runID || projection.ActorID != actorID {
		return ErrResearchResultConflict
	}
	if err := verify(ctx, projection); err != nil {
		return fmt.Errorf("verify Research Engine result: %w", err)
	}
	return m.taskTransaction(ctx, taskID, func(txManager *Manager) error {
		task, err := txManager.store.GetTask(ctx, taskID)
		if errors.Is(err, domaintask.ErrNotFound) && projection.DataScope == "user" {
			if authorizePrivateRead == nil || authorizePrivateWrite == nil {
				return domaintask.ErrPrivateTaskAccessRequired
			}
			reader, ok := txManager.store.(domaintask.PrivateTaskReader)
			if !ok {
				return domaintask.ErrPrivateTaskAccessUnsupported
			}
			task, err = reader.GetTaskAuthorized(ctx, taskID, authorizePrivateRead)
		}
		if err != nil {
			return err
		}
		if domaintask.IsOwnerManagedTask(task) {
			return domaintask.ErrTypedTaskRequiresOwner
		}
		if err := projection.Validate(task); err != nil {
			return err
		}
		latest, err := validateLatestResearchRun(ctx, txManager.store, taskID, runID, task.PrivateAccess != nil, authorizePrivateRead)
		if err != nil {
			return err
		}
		currentGeneration, err := txManager.store.WriterGeneration()
		if err != nil {
			return fmt.Errorf("read current Task writer generation: %w", err)
		}
		if currentGeneration == 0 || latest.WriterGeneration == 0 || latest.WriterGeneration != currentGeneration {
			return fmt.Errorf("%w: Research Run writer generation is stale, future, or unbound", ErrRunConflict)
		}
		if task.Status != domaintask.StatusRunning || latest.Status != domaintask.RunStatusRunning ||
			task.Assignee != actorID || latest.Assignee != actorID {
			return ErrResearchResultConflict
		}
		if task.ResearchResult != nil {
			if reflect.DeepEqual(*task.ResearchResult, projection) {
				return nil
			}
			if task.ResearchResult.ExecutionRunID == runID {
				return ErrResearchResultConflict
			}
		}
		copyProjection := projection
		copyProjection.Missing = append([]string(nil), projection.Missing...)
		copyProjection.VerifiedReceiptRefs = append([]domaintask.ResearchVerifiedReceipt(nil), projection.VerifiedReceiptRefs...)
		task.ResearchResult = &copyProjection
		task.UpdatedAt = txManager.now()
		if task.PrivateAccess != nil {
			if authorizePrivateWrite == nil {
				return domaintask.ErrPrivateTaskWriteAuthorizationRequired
			}
			writer, ok := txManager.store.(domaintask.PrivateTaskWriter)
			if !ok {
				return domaintask.ErrPrivateTaskAccessUnsupported
			}
			return writer.SaveTaskAuthorized(ctx, task, authorizePrivateWrite)
		}
		if !domaintask.ResearchResultDataScopeMatches(task, projection.DataScope, projection.OwnerID) {
			return ErrResearchResultConflict
		}
		return txManager.store.SaveTask(ctx, task)
	})
}

func validateLatestResearchRun(
	ctx context.Context,
	store Store,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	private bool,
	authorize domaintask.TaskReadAuthorizationCheck,
) (domaintask.Run, error) {
	var runs []domaintask.Run
	var err error
	if private {
		if authorize == nil {
			return domaintask.Run{}, domaintask.ErrPrivateTaskAccessRequired
		}
		reader, ok := store.(domaintask.PrivateTaskReader)
		if !ok {
			return domaintask.Run{}, domaintask.ErrPrivateTaskAccessUnsupported
		}
		runs, err = reader.ListRunsAuthorized(ctx, domaintask.RunFilter{TaskID: taskID}, authorize)
	} else {
		runs, err = store.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
	}
	if err != nil {
		return domaintask.Run{}, err
	}
	if len(runs) == 0 {
		return domaintask.Run{}, fmt.Errorf("%w: task has no persisted runs", ErrRunConflict)
	}
	seen := make(map[modulecore.RunID]struct{}, len(runs))
	var selected domaintask.Run
	found := false
	for _, candidate := range runs {
		if err := candidate.Validate(); err != nil || candidate.TaskID != taskID {
			return domaintask.Run{}, fmt.Errorf("%w: Research Run history contains an invalid or misowned Run", ErrRunConflict)
		}
		if _, duplicate := seen[candidate.RunID]; duplicate {
			return domaintask.Run{}, fmt.Errorf("%w: duplicate Run %s", ErrRunConflict, candidate.RunID)
		}
		seen[candidate.RunID] = struct{}{}
		if candidate.RunID == runID {
			selected, found = candidate, true
		}
	}
	if !found {
		return domaintask.Run{}, fmt.Errorf("%w: requested Run is absent from Task history", ErrRunConflict)
	}
	for _, candidate := range runs {
		if candidate.RunID == selected.RunID {
			continue
		}
		if candidate.StartedAt.Equal(selected.StartedAt) || candidate.StartedAt.After(selected.StartedAt) || candidate.Status == domaintask.RunStatusRunning {
			return domaintask.Run{}, fmt.Errorf("%w: requested Run is not the unique latest active Run", ErrRunConflict)
		}
	}
	if err := ctx.Err(); err != nil {
		return domaintask.Run{}, err
	}
	return selected, nil
}
