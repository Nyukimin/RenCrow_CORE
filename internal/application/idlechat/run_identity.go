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
	Create(ctx context.Context, draft domaintask.Task, shared domaintask.SharedRoleContext) (domaintask.Task, error)
	StartRunWithReason(ctx context.Context, taskID modulecore.TaskID, reason domaintask.RunStartReason) (domaintask.Run, error)
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
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return "", "", errors.New("idlechat task title is empty")
	}
	taskID := existingTaskID
	if taskID == "" {
		task, err := issuer.Create(ctx, domaintask.Task{
			Title:    title,
			Route:    domaintask.RouteGeneral,
			Assignee: idleChatAssignee(assignee),
		}, domaintask.SharedRoleContext{})
		if err != nil {
			return "", "", err
		}
		taskID = task.TaskID
	} else if err := taskID.Validate(); err != nil {
		return "", "", fmt.Errorf("existing task_id is invalid: %w", err)
	}
	run, err := issuer.StartRunWithReason(ctx, taskID, reason)
	if err != nil {
		return "", "", err
	}
	if err := validateIdleChatRunIdentity(run.TaskID, run.RunID); err != nil {
		return "", "", err
	}
	return run.TaskID, run.RunID, nil
}

func resumeIdleChatRun(ctx context.Context, issuer idlechatRunIssuer, taskID modulecore.TaskID) (modulecore.RunID, error) {
	if issuer == nil {
		return "", errors.New("idlechat run issuer is not configured")
	}
	if err := taskID.Validate(); err != nil {
		return "", err
	}
	run, err := issuer.StartRunWithReason(ctx, taskID, domaintask.RunStartReasonCheckpointResume)
	if err != nil {
		return "", err
	}
	if err := run.RunID.Validate(); err != nil {
		return "", err
	}
	return run.RunID, nil
}
