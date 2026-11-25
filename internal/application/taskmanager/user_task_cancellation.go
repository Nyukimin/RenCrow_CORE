package taskmanager

import (
	"context"
	"errors"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const userTaskCancellationSummary = "cancelled by authenticated local owner"

type UserTaskCancellationAuthorizationCheck func(context.Context, domaintask.Task) error

type UserTaskCancellationState struct {
	TaskID      modulecore.TaskID `json:"task_id"`
	Status      domaintask.Status `json:"status"`
	Cancellable bool              `json:"cancellable"`
	Cancelled   bool              `json:"cancelled"`
	Replayed    bool              `json:"replayed"`
	Reason      string            `json:"reason,omitempty"`
}

func (m *Manager) InspectUserTaskCancellation(ctx context.Context, taskID modulecore.TaskID, authorize UserTaskCancellationAuthorizationCheck) (UserTaskCancellationState, error) {
	var state UserTaskCancellationState
	err := m.taskTransaction(ctx, taskID, func(tx *Manager) error {
		var err error
		state, err = tx.inspectUserTaskCancellationInTransaction(ctx, taskID, authorize)
		return err
	})
	return state, err
}

func (m *Manager) CancelUserOriginTask(ctx context.Context, taskID modulecore.TaskID, authorize UserTaskCancellationAuthorizationCheck) (UserTaskCancellationState, error) {
	var state UserTaskCancellationState
	err := m.taskTransaction(ctx, taskID, func(tx *Manager) error {
		current, err := tx.inspectUserTaskCancellationInTransaction(ctx, taskID, authorize)
		if err != nil {
			return err
		}
		if current.Reason == "already_cancelled" {
			current.Cancelled = true
			current.Replayed = true
			state = current
			return nil
		}
		if !current.Cancellable {
			state = current
			return nil
		}
		updated, err := tx.updateStatusInTransaction(ctx, taskID, domaintask.StatusCancelled, userTaskCancellationSummary, "", nil)
		if err != nil {
			return err
		}
		state = UserTaskCancellationState{TaskID: updated.TaskID, Status: updated.Status, Cancelled: true, Reason: "cancelled"}
		return nil
	})
	return state, err
}

func (m *Manager) inspectUserTaskCancellationInTransaction(ctx context.Context, taskID modulecore.TaskID, authorize UserTaskCancellationAuthorizationCheck) (UserTaskCancellationState, error) {
	if authorize == nil {
		return UserTaskCancellationState{}, domaintask.ErrPrivateTaskAccessForbidden
	}
	task, err := m.store.GetTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, domaintask.ErrTypedTaskRequiresOwner) {
			return UserTaskCancellationState{TaskID: taskID, Reason: "dedicated_owner_required"}, nil
		}
		return UserTaskCancellationState{}, err
	}
	if err := authorize(ctx, task); err != nil {
		return UserTaskCancellationState{}, err
	}
	state := UserTaskCancellationState{TaskID: task.TaskID, Status: task.Status}
	runs, err := m.store.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
	if err != nil {
		return UserTaskCancellationState{}, err
	}
	switch {
	case len(runs) > 0:
		state.Reason = "task_has_run"
	case task.PrivateAccess != nil || task.GoalID != "" || task.WorkstreamID != "":
		state.Reason = "private_or_goal_bound"
	case domaintask.IsOwnerManagedTask(task) || task.TaskCenterExecution != nil || task.NativePlanningInvocation != nil || task.TaskMeetingInvocation != nil || task.TaskMeetingResult != nil || task.Autonomous != nil || task.Activity != nil || task.Production != nil || task.AcceptedOPSClaim != nil || len(task.NativeResumeClaims) != 0 || task.ExpectedCriteriaRevision != "" || task.Route == domaintask.RouteGAME:
		state.Reason = "dedicated_owner_required"
	case task.ReadOnly:
		state.Reason = "read_only_task"
	case strings.TrimSpace(task.OwnerID) == "" && strings.TrimSpace(task.Assignee) == "":
		state.Reason = "task_owner_unknown"
	case !hasUserOrigin(task):
		state.Reason = "user_origin_missing"
	case task.ParentTaskID != "" || len(task.DependencyTaskIDs) != 0 || task.SupersedesTaskID != "":
		state.Reason = "task_has_relations"
	case task.Status == domaintask.StatusCancelled:
		state.Reason = "already_cancelled"
	case task.Status == domaintask.StatusRunning:
		state.Reason = "execution_in_progress"
	case task.Status != domaintask.StatusQueued:
		state.Reason = "task_not_queued"
	default:
		state.Cancellable = true
	}
	return state, nil
}
