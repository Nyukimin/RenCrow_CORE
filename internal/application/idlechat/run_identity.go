package idlechat

import (
	"context"
	"errors"
	"fmt"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type idlechatRunIssuer interface {
	// CreateAndStartRun admits a new conversation Task and its first Run in one
	// transaction: a refusal (for example execution capacity) persists nothing.
	CreateAndStartRun(ctx context.Context, draft domaintask.Task, shared domaintask.SharedRoleContext) (domaintask.Task, domaintask.Run, error)
	StartRunWithReason(ctx context.Context, taskID modulecore.TaskID, reason domaintask.RunStartReason) (domaintask.Run, error)
}

// idlechatRunCanceller is kept separate from the narrow admission interface so
// existing checkpoint/recovery owners can continue to use their read/start
// surface. A configured owner that admits a new conversation task must also
// expose this canonical cancellation operation: it closes a newly admitted
// Task when the owner's answer is inconsistent (the only case in which an
// admitted Task must be withdrawn), so admission fails closed before it can
// create an unusable Task.
type idlechatRunCanceller interface {
	Cancel(ctx context.Context, taskID modulecore.TaskID, summary string) (domaintask.Task, error)
}

func validateIdleChatRunIdentity(taskID modulecore.TaskID, runID modulecore.RunID) error {
	if err := taskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := runID.Validate(); err != nil {
		return fmt.Errorf("run_id is invalid: %w", err)
	}
	return nil
}

func idleChatAssignee(initiatedBy string) string {
	switch strings.ToLower(strings.TrimSpace(initiatedBy)) {
	case "mio":
		return "Mio"
	case "shiro", "":
		return "Shiro"
	default:
		return strings.TrimSpace(initiatedBy)
	}
}

func issueIdleChatRun(
	ctx context.Context,
	issuer idlechatRunIssuer,
	title, assignee string,
	reason domaintask.RunStartReason,
	existingTaskID modulecore.TaskID,
) (modulecore.TaskID, modulecore.RunID, error) {
	if issuer == nil {
		return "", "", errors.New("idlechat run issuer is not configured")
	}
	if ctx == nil {
		return "", "", errors.New("idlechat run context is nil")
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return "", "", errors.New("idlechat task title is empty")
	}
	taskID := existingTaskID
	newTask := existingTaskID == ""
	var canceller idlechatRunCanceller
	if newTask {
		var ok bool
		canceller, ok = issuer.(idlechatRunCanceller)
		if !ok || canceller == nil {
			return "", "", errors.New("idlechat run owner cannot cancel newly created tasks")
		}
	}
	var run domaintask.Run
	if newTask {
		if reason != domaintask.RunStartReasonFirst {
			return "", "", fmt.Errorf("a new idlechat task starts with the %s reason, got %s", domaintask.RunStartReasonFirst, reason)
		}
		task, startedRun, err := issuer.CreateAndStartRun(ctx, domaintask.Task{
			Title:    title,
			Route:    domaintask.RouteGeneral,
			Assignee: idleChatAssignee(assignee),
		}, domaintask.SharedRoleContext{})
		if err != nil {
			// Nothing was persisted, so there is no Task to withdraw.
			return "", "", err
		}
		taskID = task.TaskID
		run = startedRun
		if err := taskID.Validate(); err != nil {
			return "", "", fmt.Errorf("created task_id is invalid: %w", err)
		}
	} else {
		if err := taskID.Validate(); err != nil {
			return "", "", fmt.Errorf("existing task_id is invalid: %w", err)
		}
		startedRun, err := issuer.StartRunWithReason(ctx, taskID, reason)
		if err != nil {
			return "", "", err
		}
		run = startedRun
	}
	if err := validateIdleChatRunIdentity(run.TaskID, run.RunID); err != nil {
		if !newTask {
			return "", "", err
		}
		cleanupErr := cancelCreatedIdleChatTask(ctx, canceller, taskID)
		return "", "", errors.Join(err, cleanupErr)
	}
	if run.TaskID != taskID {
		err := fmt.Errorf("run task_id mismatch: got %s, want %s", run.TaskID, taskID)
		if !newTask {
			return "", "", err
		}
		cleanupErr := cancelCreatedIdleChatTask(ctx, canceller, taskID)
		return "", "", errors.Join(err, cleanupErr)
	}
	return run.TaskID, run.RunID, nil
}

func cancelCreatedIdleChatTask(ctx context.Context, canceller idlechatRunCanceller, taskID modulecore.TaskID) error {
	if canceller == nil {
		return errors.New("idlechat run owner cannot cancel newly created tasks")
	}
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	cleanupCtx, cancel := context.WithTimeout(base, idleChatConversationRunCleanupTimeout)
	defer cancel()
	if _, err := canceller.Cancel(cleanupCtx, taskID, "IdleChat conversation admission failed"); err != nil {
		return fmt.Errorf("cancel newly created idlechat task: %w", err)
	}
	return nil
}
