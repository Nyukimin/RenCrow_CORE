package idlechat

import (
	"context"
	"errors"
	"fmt"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// generationRunOwner is the canonical Task/Run owner used by IdleChat generation. The
// issuer remains a separate narrow interface so generation cannot issue a
// completion through an unverified or projection-only implementation.
type generationRunOwner interface {
	Get(context.Context, modulecore.TaskID) (domaintask.Task, error)
	ListRuns(context.Context, domaintask.RunFilter) ([]domaintask.Run, error)
	VerifyRunCompletion(context.Context, modulecore.TaskID, modulecore.RunID, string, domaintask.Status) error
	CompleteRun(context.Context, modulecore.TaskID, modulecore.RunID, string, domaintask.Status, string, string) (domaintask.Task, error)
}

func generationRunOwnerFromIssuer(issuer idlechatRunIssuer) (generationRunOwner, error) {
	if issuer == nil {
		return nil, errors.New("idlechat run issuer is not configured")
	}
	owner, ok := issuer.(generationRunOwner)
	if !ok || owner == nil {
		return nil, errors.New("idlechat run owner is not configured")
	}
	return owner, nil
}

func validateGenerationRunInputs(ctx context.Context, issuer idlechatRunIssuer, taskID modulecore.TaskID, runID modulecore.RunID) error {
	if ctx == nil {
		return errors.New("generation run lifecycle context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := generationRunOwnerFromIssuer(issuer); err != nil {
		return err
	}
	return validateIdleChatRunIdentity(taskID, runID)
}

// inspectGenerationRun validates the exact canonical Task/Run pair before an IdleChat
// producer can close the Run. The selected Run must be the unique latest
// execution and no other execution may still be active.
func inspectGenerationRun(ctx context.Context, issuer idlechatRunIssuer, taskID modulecore.TaskID, runID modulecore.RunID) (domaintask.Run, error) {
	if err := validateGenerationRunInputs(ctx, issuer, taskID, runID); err != nil {
		return domaintask.Run{}, err
	}
	owner, err := generationRunOwnerFromIssuer(issuer)
	if err != nil {
		return domaintask.Run{}, err
	}

	task, err := owner.Get(ctx, taskID)
	if err != nil {
		return domaintask.Run{}, err
	}
	if task.TaskID != taskID {
		return domaintask.Run{}, fmt.Errorf("task_id mismatch: got %s, want %s", task.TaskID, taskID)
	}
	if err := task.Validate(); err != nil {
		return domaintask.Run{}, fmt.Errorf("task record is invalid: %w", err)
	}

	runs, err := owner.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
	if err != nil {
		return domaintask.Run{}, err
	}
	if len(runs) == 0 {
		return domaintask.Run{}, fmt.Errorf("run %s was not found for task %s", runID, taskID)
	}

	seen := make(map[modulecore.RunID]struct{}, len(runs))
	var selected domaintask.Run
	found := false
	for _, run := range runs {
		if err := run.Validate(); err != nil {
			return domaintask.Run{}, fmt.Errorf("run record is invalid: %w", err)
		}
		if run.TaskID != taskID {
			return domaintask.Run{}, fmt.Errorf("run %s task_id mismatch: got %s, want %s", run.RunID, run.TaskID, taskID)
		}
		if _, exists := seen[run.RunID]; exists {
			return domaintask.Run{}, fmt.Errorf("duplicate run %s", run.RunID)
		}
		seen[run.RunID] = struct{}{}
		if run.RunID == runID {
			selected = run
			found = true
		}
	}
	if !found {
		return domaintask.Run{}, fmt.Errorf("run %s was not found for task %s", runID, taskID)
	}
	if strings.TrimSpace(task.Assignee) == "" || strings.TrimSpace(selected.Assignee) == "" {
		return domaintask.Run{}, fmt.Errorf("task and run assignee are required")
	}
	if task.Assignee != selected.Assignee {
		return domaintask.Run{}, fmt.Errorf("task and run assignee do not match")
	}
	if selected.Status == domaintask.RunStatusRunning && task.Status != domaintask.StatusRunning {
		return domaintask.Run{}, fmt.Errorf("running run requires running task")
	}

	for _, run := range runs {
		if run.RunID == selected.RunID {
			continue
		}
		if run.StartedAt.Equal(selected.StartedAt) {
			return domaintask.Run{}, fmt.Errorf("latest run is ambiguous at started_at %s", selected.StartedAt)
		}
		if run.StartedAt.After(selected.StartedAt) {
			return domaintask.Run{}, fmt.Errorf("run %s is not the latest run", selected.RunID)
		}
		if run.Status == domaintask.RunStatusRunning {
			return domaintask.Run{}, fmt.Errorf("task has another active run %s", run.RunID)
		}
	}
	return selected, nil
}

func generationRunCompletionStatus(status domaintask.Status) (domaintask.RunStatus, bool) {
	switch status {
	case domaintask.StatusSucceeded:
		return domaintask.RunStatusSucceeded, true
	case domaintask.StatusFailed:
		return domaintask.RunStatusFailed, true
	case domaintask.StatusCancelled:
		return domaintask.RunStatusCancelled, true
	case domaintask.StatusWaiting:
		return domaintask.RunStatusWaiting, true
	default:
		return "", false
	}
}

// completeGenerationRun closes a verified active generation Run through the canonical
// owner. A terminal Run is checked again by the owner transaction so a retry
// cannot make a point-in-time preflight decision after the state has changed.
func completeGenerationRun(ctx context.Context, issuer idlechatRunIssuer, taskID modulecore.TaskID, runID modulecore.RunID, status domaintask.Status, summary, reason string) error {
	if err := validateGenerationRunInputs(ctx, issuer, taskID, runID); err != nil {
		return err
	}
	_, ok := generationRunCompletionStatus(status)
	if !ok {
		return fmt.Errorf("generation run completion status is not accepted: %s", status)
	}
	if status == domaintask.StatusWaiting && strings.TrimSpace(reason) == "" {
		return errors.New("waiting reason is required")
	}

	run, err := inspectGenerationRun(ctx, issuer, taskID, runID)
	if err != nil {
		return err
	}
	owner, err := generationRunOwnerFromIssuer(issuer)
	if err != nil {
		return err
	}

	if run.Status != domaintask.RunStatusRunning {
		return owner.VerifyRunCompletion(ctx, taskID, runID, run.Assignee, status)
	}
	_, err = owner.CompleteRun(ctx, taskID, runID, run.Assignee, status, summary, strings.TrimSpace(reason))
	return err
}
