package actionmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrNotFound      = domainaction.ErrNotFound
	ErrAttemptConflict = errors.New("attempt state conflict")
)

// Store is the persistence boundary owned by the Action owner.
type Store interface {
	SaveAction(context.Context, domainaction.Action) error
	GetAction(context.Context, modulecore.ActionID) (domainaction.Action, error)
	ListActions(context.Context, domainaction.Filter) ([]domainaction.Action, error)
	SaveAttempt(context.Context, domainaction.Attempt) error
	GetAttempt(context.Context, modulecore.AttemptID) (domainaction.Attempt, error)
	ListAttempts(context.Context, domainaction.AttemptFilter) ([]domainaction.Attempt, error)
}

// Manager is the sole runtime issuer of ActionID and AttemptID.
type Manager struct {
	store Store
	now   func() time.Time
}

func New(store Store) *Manager {
	return &Manager{store: store, now: func() time.Time { return time.Now().UTC() }}
}

type CreateInput struct {
	TaskID modulecore.TaskID
	RunID  modulecore.RunID
	Kind   domainaction.Kind
	Name   string
}

// CreateAction mints a canonical ActionID and starts the first Attempt.
func (m *Manager) CreateAction(ctx context.Context, input CreateInput) (domainaction.Action, domainaction.Attempt, error) {
	if err := ctx.Err(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	if err := input.TaskID.Validate(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := input.RunID.Validate(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("run_id is invalid: %w", err)
	}
	if !domainaction.ValidKind(input.Kind) {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("invalid action kind: %s", input.Kind)
	}
	now := m.now()
	actionID := modulecore.NewActionID()
	attemptID := modulecore.NewAttemptID()
	action := domainaction.Action{
		ActionID:         actionID,
		TaskID:           input.TaskID,
		RunID:            input.RunID,
		Kind:             input.Kind,
		Name:             domainaction.NormalizeName(input.Name),
		Status:           domainaction.StatusOpen,
		CurrentAttemptID: attemptID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := action.Validate(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	attempt := domainaction.Attempt{
		AttemptID:   attemptID,
		ActionID:    actionID,
		StartReason: domainaction.AttemptStartReasonFirst,
		Status:      domainaction.AttemptStatusRunning,
		StartedAt:   now,
	}
	if err := attempt.Validate(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	if err := m.store.SaveAction(ctx, action); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	if err := m.store.SaveAttempt(ctx, attempt); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	return action, attempt, nil
}

// StartAttempt closes any active Attempt and issues a new AttemptID for the same ActionID.
func (m *Manager) StartAttempt(ctx context.Context, actionID modulecore.ActionID, reason domainaction.AttemptStartReason) (domainaction.Action, domainaction.Attempt, error) {
	if err := actionID.Validate(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("action_id is invalid: %w", err)
	}
	if reason == domainaction.AttemptStartReasonFirst {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("first attempt is created only by CreateAction")
	}
	if !domainaction.ValidAttemptStartReason(reason) {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("invalid attempt start reason: %s", reason)
	}
	action, err := m.store.GetAction(ctx, actionID)
	if err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	if !action.IsOpen() {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("action %s is closed", action.ActionID)
	}
	now := m.now()
	if err := m.closeCurrentAttempt(ctx, actionID, domainaction.AttemptStatusFailed, now, "superseded by retry"); err != nil && !errors.Is(err, ErrNotFound) {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	attempt := domainaction.Attempt{
		AttemptID:   modulecore.NewAttemptID(),
		ActionID:    actionID,
		StartReason: reason,
		Status:      domainaction.AttemptStatusRunning,
		StartedAt:   now,
	}
	if err := attempt.Validate(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	action.CurrentAttemptID = attempt.AttemptID
	action.UpdatedAt = now
	if err := action.Validate(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	if err := m.store.SaveAction(ctx, action); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	if err := m.store.SaveAttempt(ctx, attempt); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	return action, attempt, nil
}

// CompleteAttempt terminates the current Attempt and optionally the Action.
func (m *Manager) CompleteAttempt(ctx context.Context, actionID modulecore.ActionID, attemptStatus domainaction.AttemptStatus, actionStatus domainaction.Status, summary string) (domainaction.Action, domainaction.Attempt, error) {
	if err := actionID.Validate(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("action_id is invalid: %w", err)
	}
	action, err := m.store.GetAction(ctx, actionID)
	if err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	now := m.now()
	attempt, err := m.closeCurrentAttemptReturning(ctx, actionID, attemptStatus, now, summary)
	if err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	if actionStatus == "" || actionStatus == domainaction.StatusOpen {
		action.UpdatedAt = now
		if err := m.store.SaveAction(ctx, action); err != nil {
			return domainaction.Action{}, domainaction.Attempt{}, err
		}
		return action, attempt, nil
	}
	closed, err := action.Close(actionStatus, now, strings.TrimSpace(summary))
	if err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	if err := m.store.SaveAction(ctx, closed); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	return closed, attempt, nil
}

func (m *Manager) GetAction(ctx context.Context, actionID modulecore.ActionID) (domainaction.Action, error) {
	return m.store.GetAction(ctx, actionID)
}

func (m *Manager) ListActions(ctx context.Context, filter domainaction.Filter) ([]domainaction.Action, error) {
	return m.store.ListActions(ctx, filter)
}

func (m *Manager) ListAttempts(ctx context.Context, filter domainaction.AttemptFilter) ([]domainaction.Attempt, error) {
	return m.store.ListAttempts(ctx, filter)
}

func (m *Manager) closeCurrentAttempt(ctx context.Context, actionID modulecore.ActionID, status domainaction.AttemptStatus, completedAt time.Time, summary string) error {
	_, err := m.closeCurrentAttemptReturning(ctx, actionID, status, completedAt, summary)
	return err
}

func (m *Manager) closeCurrentAttemptReturning(ctx context.Context, actionID modulecore.ActionID, status domainaction.AttemptStatus, completedAt time.Time, summary string) (domainaction.Attempt, error) {
	attempts, err := m.store.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: actionID, Status: domainaction.AttemptStatusRunning})
	if err != nil {
		return domainaction.Attempt{}, err
	}
	if len(attempts) == 0 {
		return domainaction.Attempt{}, ErrNotFound
	}
	if len(attempts) > 1 {
		return domainaction.Attempt{}, fmt.Errorf("%w: multiple running attempts for %s", ErrAttemptConflict, actionID)
	}
	closed, err := attempts[0].Close(status, completedAt, summary)
	if err != nil {
		return domainaction.Attempt{}, err
	}
	if err := m.store.SaveAttempt(ctx, closed); err != nil {
		return domainaction.Attempt{}, err
	}
	return closed, nil
}
