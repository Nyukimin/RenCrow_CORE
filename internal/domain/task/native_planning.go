package task

import (
	"context"
	"errors"
	"fmt"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrNativePlanningTaskRequiresOwner   = errors.New("native planning Task requires its registered owner")
	ErrNativePlanningInvocationImmutable = errors.New("native planning invocation provenance is immutable after first save")
)

// NativePlanningInvocation is an immutable proof that a bounded planning
// Task was issued for one native actor invocation. It grants no Worker tools.
// The request key is trusted caller invocation identity, never model input.
type NativePlanningInvocation struct {
	ParentTaskID        modulecore.TaskID    `json:"parent_task_id"`
	ParentRunID         modulecore.RunID     `json:"parent_run_id"`
	ParentActorID       string               `json:"parent_actor_id"`
	ActorID             string               `json:"actor_id"`
	Purpose             string               `json:"purpose"`
	RequestKey          modulecore.RequestID `json:"request_key"`
	EnvelopeSHA256      string               `json:"envelope_sha256"`
	ParentReadOnly      bool                 `json:"parent_read_only"`
	DataScope           string               `json:"data_scope"`
	AuthenticatedUserID string               `json:"authenticated_user_id,omitempty"`
}

func (inv NativePlanningInvocation) Validate(task Task) error {
	if inv.ParentTaskID.Validate() != nil || inv.ParentRunID.Validate() != nil || inv.RequestKey.Validate() != nil {
		return errors.New("parent task, parent run, and request key are required")
	}
	if strings.TrimSpace(inv.ActorID) == "" || strings.TrimSpace(inv.ActorID) != inv.ActorID || strings.ToLower(inv.ActorID) != inv.ActorID {
		return errors.New("actor_id must be canonical lowercase")
	}
	if strings.TrimSpace(inv.ParentActorID) == "" || strings.TrimSpace(inv.ParentActorID) != inv.ParentActorID || strings.ToLower(inv.ParentActorID) != inv.ParentActorID {
		return errors.New("parent_actor_id must be canonical lowercase")
	}
	if inv.Purpose != "task_center_planning" {
		return fmt.Errorf("unsupported planning purpose %q", inv.Purpose)
	}
	if !validSHA256(inv.EnvelopeSHA256) {
		return errors.New("envelope_sha256 must be lowercase SHA-256")
	}
	switch inv.DataScope {
	case "public":
		if inv.AuthenticatedUserID != "" {
			return errors.New("public planning Task must not pin a user")
		}
	case "user":
		if strings.TrimSpace(inv.AuthenticatedUserID) == "" || strings.TrimSpace(inv.AuthenticatedUserID) != inv.AuthenticatedUserID {
			return errors.New("user planning Task requires its exact authenticated user")
		}
	default:
		return errors.New("planning Task data_scope must be public or user")
	}
	if task.ParentTaskID != inv.ParentTaskID || task.Route != RoutePLAN || task.Assignee != inv.ActorID {
		return errors.New("planning Task parent, route, and assignee must match its immutable invocation")
	}
	if inv.ParentReadOnly && !task.ReadOnly {
		return errors.New("planning Task cannot relax its parent's read-only restriction")
	}
	if task.TaskCenterExecution != nil || task.TaskMeetingInvocation != nil {
		return errors.New("planning Task cannot also be another native owner-managed Task")
	}
	return nil
}

func IsNativePlanningTask(task Task) bool { return task.NativePlanningInvocation != nil }

// IsOwnerManagedTask reports Tasks whose lifecycle and effects may only be
// changed by their dedicated typed owner.
func IsOwnerManagedTask(task Task) bool {
	return IsTaskCenterTyped(task) || IsNativePlanningTask(task) || IsTaskMeetingTask(task)
}

type NativePlanningStateAuthorizationCheck func(context.Context, Task, Run, SharedRoleContext) error
type NativePlanningParentAuthorizationCheck func(context.Context) error

type NativePlanningInvocationWriter interface {
	SaveNativePlanningInvocationState(context.Context, Task, Run, *SharedRoleContext, NativePlanningStateAuthorizationCheck) error
}

type NativePlanningInvocationStateReader interface {
	GetNativePlanningInvocationState(context.Context, modulecore.TaskID, modulecore.RunID, NativePlanningParentAuthorizationCheck, NativePlanningStateAuthorizationCheck) (Task, Run, SharedRoleContext, error)
}
