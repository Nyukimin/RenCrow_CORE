package task

import (
	"context"
	"errors"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrPrivateTaskAccessInvalid              = errors.New("private Task access metadata is invalid")
	ErrPrivateTaskAccessImmutable            = errors.New("private Task access metadata is immutable")
	ErrPrivateTaskAccessRequired             = errors.New("private Task access requires an owner authorization check")
	ErrPrivateTaskAccessForbidden            = errors.New("private Task access is forbidden")
	ErrPrivateTaskAccessUnsupported          = errors.New("Task store does not support authorized private Task access")
	ErrPrivateTaskWriteAuthorizationRequired = errors.New("private Task persistence requires an owner authorization check")
)

type TaskAccessScope string

const TaskAccessScopeUser TaskAccessScope = "user"

// TaskPrivateAccess is the durable owner boundary for a Task created from a
// private Goal. WorkstreamID and GoalID remain on Task itself and are the
// canonical source references; this metadata carries the exact user principal.
// A nil value is the legacy/public contract.
type TaskPrivateAccess struct {
	Scope               TaskAccessScope `json:"scope"`
	AuthenticatedUserID string          `json:"authenticated_user_id"`
}

func (access TaskPrivateAccess) Validate() error {
	if access.Scope != TaskAccessScopeUser || access.AuthenticatedUserID == "" ||
		strings.TrimSpace(access.AuthenticatedUserID) != access.AuthenticatedUserID {
		return ErrPrivateTaskAccessInvalid
	}
	return nil
}

type TaskReadResource string

const (
	TaskReadResourceTask         TaskReadResource = "task"
	TaskReadResourceRun          TaskReadResource = "run"
	TaskReadResourceContext      TaskReadResource = "context"
	TaskReadResourceNotification TaskReadResource = "notification"
	TaskReadResourceReport       TaskReadResource = "delegation_report"
)

// TaskReadAuthorizationInput contains only the durable authorization boundary
// and requested resource identity. It deliberately excludes Task content.
type TaskReadAuthorizationInput struct {
	Resource            TaskReadResource
	TaskID              modulecore.TaskID
	RunID               modulecore.RunID
	WorkstreamID        modulecore.WorkstreamID
	GoalID              modulecore.GoalID
	Scope               TaskAccessScope
	AuthenticatedUserID string
	// Origin publication proof is authorization metadata only. It is never
	// serialized into viewer DTOs or exposed as Task content.
	OriginSessionID   modulecore.SessionID         `json:"-"`
	OriginPublication *OriginPublicationProvenance `json:"-"`
}

type TaskReadAuthorizationCheck func(context.Context, TaskReadAuthorizationInput) error

type TaskWriteOperation string

const TaskWriteOperationPersist TaskWriteOperation = "persist"

type TaskWriteResource string

const (
	TaskWriteResourceTask    TaskWriteResource = "task"
	TaskWriteResourceRun     TaskWriteResource = "run"
	TaskWriteResourceContext TaskWriteResource = "context"
)

type TaskWriteAuthorizationInput struct {
	Operation           TaskWriteOperation
	Resource            TaskWriteResource
	TaskID              modulecore.TaskID
	RunID               modulecore.RunID
	WorkstreamID        modulecore.WorkstreamID
	GoalID              modulecore.GoalID
	Scope               TaskAccessScope
	AuthenticatedUserID string
}

// TaskWriteAuthorizationCheck authorizes persistence of an owner-bound Task
// record. It does not admit execution or external side effects.
type TaskWriteAuthorizationCheck func(context.Context, TaskWriteAuthorizationInput) error

// PrivateTaskReader is an explicit read port for future owner-authorized
// private Task flows. Implementations must invoke authorize before returning
// any private record and must fail closed on missing/unknown source links.
type PrivateTaskReader interface {
	GetTaskAuthorized(context.Context, modulecore.TaskID, TaskReadAuthorizationCheck) (Task, error)
	ListTasksAuthorized(context.Context, Filter, TaskReadAuthorizationCheck) ([]Task, error)
	GetRunAuthorized(context.Context, modulecore.RunID, TaskReadAuthorizationCheck) (Run, error)
	ListRunsAuthorized(context.Context, RunFilter, TaskReadAuthorizationCheck) ([]Run, error)
	GetContextAuthorized(context.Context, modulecore.TaskID, TaskReadAuthorizationCheck) (SharedRoleContext, error)
}

// PrivateTaskWriter is deliberately separate from Store.SaveTask. Generic
// stores must not create a private record or change its access metadata.
type PrivateTaskWriter interface {
	SaveTaskAuthorized(context.Context, Task, TaskWriteAuthorizationCheck) error
	SaveRunAuthorized(context.Context, Run, TaskWriteAuthorizationCheck) error
	SaveContextAuthorized(context.Context, SharedRoleContext, TaskWriteAuthorizationCheck) error
}

func TaskReadInput(task Task, resource TaskReadResource, runID modulecore.RunID) (TaskReadAuthorizationInput, error) {
	if task.PrivateAccess == nil || task.PrivateAccess.Validate() != nil || task.TaskID.Validate() != nil || task.WorkstreamID == "" || task.GoalID == "" {
		return TaskReadAuthorizationInput{}, ErrPrivateTaskAccessInvalid
	}
	if task.WorkstreamID.Validate() != nil || task.GoalID.Validate() != nil {
		return TaskReadAuthorizationInput{}, ErrPrivateTaskAccessInvalid
	}
	return TaskReadAuthorizationInput{
		Resource: resource, TaskID: task.TaskID, RunID: runID,
		WorkstreamID: task.WorkstreamID, GoalID: task.GoalID,
		Scope: task.PrivateAccess.Scope, AuthenticatedUserID: task.PrivateAccess.AuthenticatedUserID,
		OriginSessionID: task.OriginSessionID, OriginPublication: cloneOriginPublicationProvenance(task.OriginPublication),
	}, nil
}

func cloneOriginPublicationProvenance(input *OriginPublicationProvenance) *OriginPublicationProvenance {
	if input == nil {
		return nil
	}
	copy := *input
	return &copy
}

func PrivateTaskWriteInput(task Task) (TaskWriteAuthorizationInput, error) {
	return TaskWriteInput(task, TaskWriteResourceTask, "")
}

func TaskWriteInput(task Task, resource TaskWriteResource, runID modulecore.RunID) (TaskWriteAuthorizationInput, error) {
	if task.PrivateAccess == nil || task.PrivateAccess.Validate() != nil || task.TaskID.Validate() != nil || task.WorkstreamID == "" || task.GoalID == "" ||
		task.WorkstreamID.Validate() != nil || task.GoalID.Validate() != nil {
		return TaskWriteAuthorizationInput{}, ErrPrivateTaskAccessInvalid
	}
	return TaskWriteAuthorizationInput{
		Operation: TaskWriteOperationPersist, Resource: resource, TaskID: task.TaskID, RunID: runID,
		WorkstreamID: task.WorkstreamID, GoalID: task.GoalID,
		Scope: task.PrivateAccess.Scope, AuthenticatedUserID: task.PrivateAccess.AuthenticatedUserID,
	}, nil
}

func authorizePrivateTaskRead(ctx context.Context, task Task, resource TaskReadResource, runID modulecore.RunID, authorize TaskReadAuthorizationCheck) error {
	if ctx == nil {
		return errors.New("private Task read context is nil")
	}
	if authorize == nil {
		return ErrPrivateTaskAccessRequired
	}
	input, err := TaskReadInput(task, resource, runID)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := authorize(ctx, input); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return ErrPrivateTaskAccessForbidden
	}
	return ctx.Err()
}
