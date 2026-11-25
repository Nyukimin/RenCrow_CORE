package taskcenter

import (
	"context"
	"errors"
	"fmt"

	taskmanager "github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type UserTaskCancellationPublicationReader interface {
	CurrentPublicationBinding(context.Context, string) (domainsession.CanonicalSessionPublicationBinding, error)
}

type UserTaskCancellationOwner interface {
	InspectUserTaskCancellation(context.Context, modulecore.TaskID, taskmanager.UserTaskCancellationAuthorizationCheck) (taskmanager.UserTaskCancellationState, error)
	CancelUserOriginTask(context.Context, modulecore.TaskID, taskmanager.UserTaskCancellationAuthorizationCheck) (taskmanager.UserTaskCancellationState, error)
}

func (s *Service) WithUserTaskCancellationPublicationReader(reader UserTaskCancellationPublicationReader) *Service {
	if s != nil {
		s.userTaskCancellationPublications = reader
	}
	return s
}

func (s *Service) WithUserTaskCancellationOwner(owner UserTaskCancellationOwner) *Service {
	if s == nil {
		return nil
	}
	s.userTaskCancellationOwner = owner
	return s
}

type UserTaskCancellationResult struct {
	TaskID      modulecore.TaskID `json:"task_id"`
	Status      domaintask.Status `json:"status"`
	Cancellable bool              `json:"cancellable"`
	Cancelled   bool              `json:"cancelled"`
	Replayed    bool              `json:"replayed"`
	Reason      string            `json:"reason,omitempty"`
}

func (s *Service) InspectUserTaskCancellation(ctx context.Context, taskID modulecore.TaskID) (UserTaskCancellationResult, error) {
	return s.userTaskCancellation(ctx, taskID, false)
}

func (s *Service) CancelUserOriginTask(ctx context.Context, taskID modulecore.TaskID) (UserTaskCancellationResult, error) {
	return s.userTaskCancellation(ctx, taskID, true)
}

func (s *Service) userTaskCancellation(ctx context.Context, taskID modulecore.TaskID, cancel bool) (UserTaskCancellationResult, error) {
	scope, err := s.scopeFromContext(ctx)
	if err != nil {
		return UserTaskCancellationResult{}, err
	}
	if scope.ActorKind != tool.ActorKindUser || scope.AuthenticatedUserID == "" || scope.ActorID != scope.AuthenticatedUserID ||
		!scope.Allows(tool.DataScopePublic) || !scope.Allows(tool.DataScopeUser) || taskID.Validate() != nil {
		return UserTaskCancellationResult{}, ErrForbidden
	}
	owner := s.userTaskCancellationOwner
	if owner == nil && s.nativeExecution != nil && s.nativeExecution.Tasks != nil {
		owner, _ = s.nativeExecution.Tasks.(UserTaskCancellationOwner)
	}
	if owner == nil {
		return UserTaskCancellationResult{}, ErrUnsupported
	}
	var state taskmanager.UserTaskCancellationState
	authorize := func(checkCtx context.Context, current domaintask.Task) error {
		if current.OriginSessionID == "" || current.OriginPublication == nil ||
			current.OriginThreadID == "" || current.OriginTurnID == "" || current.OriginMessageID == "" ||
			current.OriginPublication.Audience != "private" || current.OriginPublication.OwnerID != scope.AuthenticatedUserID {
			return ErrForbidden
		}
		return s.authorizeUserTaskCancellationPublication(checkCtx, scope, current.OriginSessionID, current.OriginPublication)
	}
	if cancel {
		state, err = owner.CancelUserOriginTask(ctx, taskID, authorize)
	} else {
		state, err = owner.InspectUserTaskCancellation(ctx, taskID, authorize)
	}
	if err != nil {
		switch {
		case errors.Is(err, domaintask.ErrNotFound):
			return UserTaskCancellationResult{}, ErrNotFound
		case errors.Is(err, domaintask.ErrPrivateTaskAccessForbidden):
			return UserTaskCancellationResult{}, ErrForbidden
		case errors.Is(err, ErrUnavailable), errors.Is(err, ErrForbidden):
			return UserTaskCancellationResult{}, err
		case errors.Is(err, domaintask.ErrTypedTaskRequiresOwner):
			return UserTaskCancellationResult{}, ErrUnsupported
		default:
			return UserTaskCancellationResult{}, fmt.Errorf("inspect or cancel owner Task: %w", err)
		}
	}
	return UserTaskCancellationResult{
		TaskID: state.TaskID, Status: state.Status, Cancellable: state.Cancellable,
		Cancelled: state.Cancelled, Replayed: state.Replayed, Reason: state.Reason,
	}, nil
}

func (s *Service) authorizeUserTaskCancellationPublication(ctx context.Context, scope tool.ToolExecutionScope, sessionID modulecore.SessionID, proof *domaintask.OriginPublicationProvenance) error {
	if proof == nil || sessionID.Validate() != nil || proof.Validate(sessionID) != nil {
		return ErrForbidden
	}
	if proof.Audience != "private" || proof.OwnerID != scope.AuthenticatedUserID {
		return ErrForbidden
	}
	reader := s.userTaskCancellationPublications
	if reader == nil {
		return fmt.Errorf("%w: current Session publication reader is unavailable", ErrUnavailable)
	}
	current, err := reader.CurrentPublicationBinding(ctx, string(sessionID))
	if errors.Is(err, domainsession.ErrSessionNotFound) || err == nil && current.Validate() != nil {
		return ErrForbidden
	}
	if err != nil {
		return fmt.Errorf("%w: current Session publication could not be read", ErrUnavailable)
	}
	address := current.ChannelAddress()
	if current.SessionID() != sessionID || current.PublicationIncarnation() != proof.PublicationIncarnation || address.ChannelType() != proof.Channel || address.ExternalConversationID() != proof.ChannelAddress || current.Audience() != proof.Audience || current.OwnerID() != proof.OwnerID {
		return ErrForbidden
	}
	return nil
}
