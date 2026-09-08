package actionmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrNotFound        = domainaction.ErrNotFound
	ErrAttemptConflict = errors.New("attempt state conflict")
)

// Store is the persistence boundary owned by the Action owner.
type Store = domainaction.Store

// Manager is the sole runtime issuer of ActionID and AttemptID.
type Manager struct {
	store Store
	now   func() time.Time
	mu    sync.RWMutex
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
	if err := validateContext(ctx); err != nil {
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := validateContext(ctx); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
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
	var savedAction domainaction.Action
	var savedAttempt domainaction.Attempt
	err := m.store.Transaction(ctx, func(tx Store) error {
		if err := validateContext(ctx); err != nil {
			return err
		}
		if err := action.Validate(); err != nil {
			return err
		}
		if err := attempt.Validate(); err != nil {
			return err
		}
		if err := tx.SaveAction(ctx, action); err != nil {
			return err
		}
		if err := tx.SaveAttempt(ctx, attempt); err != nil {
			return err
		}
		savedAction = action
		savedAttempt = attempt
		return nil
	})
	if err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	return savedAction, savedAttempt, nil
}

// StartAttempt closes any active Attempt and issues a new AttemptID for the same ActionID.
func (m *Manager) StartAttempt(ctx context.Context, actionID modulecore.ActionID, reason domainaction.AttemptStartReason) (domainaction.Action, domainaction.Attempt, error) {
	if err := validateContext(ctx); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	if err := actionID.Validate(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("action_id is invalid: %w", err)
	}
	if reason == domainaction.AttemptStartReasonFirst {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("first attempt is created only by CreateAction")
	}
	if !domainaction.ValidAttemptStartReason(reason) {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("invalid attempt start reason: %s", reason)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := validateContext(ctx); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	var resultAction domainaction.Action
	var resultAttempt domainaction.Attempt
	err := m.store.Transaction(ctx, func(tx Store) error {
		if err := validateContext(ctx); err != nil {
			return err
		}
		action, err := tx.GetAction(ctx, actionID)
		if err != nil {
			return err
		}
		if err := action.Validate(); err != nil {
			return err
		}
		if !action.IsOpen() {
			return fmt.Errorf("action %s is closed", action.ActionID)
		}
		now := m.now()
		if _, err := m.closeCurrentAttemptReturning(ctx, tx, actionID, domainaction.AttemptStatusFailed, now, "superseded by retry"); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		attempt := domainaction.Attempt{
			AttemptID:   modulecore.NewAttemptID(),
			ActionID:    actionID,
			StartReason: reason,
			Status:      domainaction.AttemptStatusRunning,
			StartedAt:   now,
		}
		if err := attempt.Validate(); err != nil {
			return err
		}
		action.CurrentAttemptID = attempt.AttemptID
		action.UpdatedAt = now
		if err := action.Validate(); err != nil {
			return err
		}
		if err := tx.SaveAction(ctx, action); err != nil {
			return err
		}
		if err := tx.SaveAttempt(ctx, attempt); err != nil {
			return err
		}
		resultAction = action
		resultAttempt = attempt
		return nil
	})
	if err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	return resultAction, resultAttempt, nil
}

// CompleteAttempt terminates the current Attempt and optionally the Action.
func (m *Manager) CompleteAttempt(ctx context.Context, actionID modulecore.ActionID, expectedAttemptID modulecore.AttemptID, attemptStatus domainaction.AttemptStatus, actionStatus domainaction.Status, summary string) (domainaction.Action, domainaction.Attempt, error) {
	if err := validateContext(ctx); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	if err := actionID.Validate(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("action_id is invalid: %w", err)
	}
	if err := expectedAttemptID.Validate(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("attempt_id is invalid: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := validateContext(ctx); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	var resultAction domainaction.Action
	var resultAttempt domainaction.Attempt
	err := m.store.Transaction(ctx, func(tx Store) error {
		if err := validateContext(ctx); err != nil {
			return err
		}
		action, attempt, err := m.currentPair(tx, ctx, actionID, expectedAttemptID)
		if err != nil {
			return err
		}
		now := m.now()
		closedAttempt, err := attempt.Close(attemptStatus, now, summary)
		if err != nil {
			return err
		}
		result := action
		if actionStatus == "" || actionStatus == domainaction.StatusOpen {
			action.UpdatedAt = now
			result = action
		} else {
			result, err = action.Close(actionStatus, now, strings.TrimSpace(summary))
			if err != nil {
				return err
			}
		}
		if err := result.Validate(); err != nil {
			return err
		}
		if err := tx.SaveAttempt(ctx, closedAttempt); err != nil {
			return err
		}
		if err := tx.SaveAction(ctx, result); err != nil {
			return err
		}
		resultAction = result
		resultAttempt = closedAttempt
		return nil
	})
	if err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	return resultAction, resultAttempt, nil
}

// ValidateToolAttempt verifies that an owner-provided bound pair is the active
// Tool action for the supplied Task, Run, and tool name. It does not mutate state.
func (m *Manager) ValidateToolAttempt(ctx context.Context, actionID modulecore.ActionID, attemptID modulecore.AttemptID, taskID modulecore.TaskID, runID modulecore.RunID, toolName string) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	if err := actionID.Validate(); err != nil {
		return fmt.Errorf("action_id is invalid: %w", err)
	}
	if err := attemptID.Validate(); err != nil {
		return fmt.Errorf("attempt_id is invalid: %w", err)
	}
	if err := taskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := runID.Validate(); err != nil {
		return fmt.Errorf("run_id is invalid: %w", err)
	}
	if domainaction.NormalizeName(toolName) == "" {
		return fmt.Errorf("%w: tool name is required", ErrAttemptConflict)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store.ReadTransaction(ctx, func(tx Store) error {
		if err := validateContext(ctx); err != nil {
			return err
		}
		action, _, err := m.currentPair(tx, ctx, actionID, attemptID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return fmt.Errorf("%w: bound action/attempt is unavailable: %v", ErrAttemptConflict, err)
			}
			return err
		}
		if action.TaskID != taskID {
			return fmt.Errorf("%w: action %s belongs to task %s, want %s", ErrAttemptConflict, actionID, action.TaskID, taskID)
		}
		if action.RunID != runID {
			return fmt.Errorf("%w: action %s belongs to run %s, want %s", ErrAttemptConflict, actionID, action.RunID, runID)
		}
		if action.Kind != domainaction.KindTool {
			return fmt.Errorf("%w: action %s kind is %s, want %s", ErrAttemptConflict, actionID, action.Kind, domainaction.KindTool)
		}
		if domainaction.NormalizeName(action.Name) != toolName {
			return fmt.Errorf("%w: action %s name is %q, want %q", ErrAttemptConflict, actionID, domainaction.NormalizeName(action.Name), toolName)
		}
		return nil
	})
}

// CompleteToolAttempt maps one ToolRunner result to the terminal Action and Attempt state.
func (m *Manager) CompleteToolAttempt(ctx context.Context, actionID modulecore.ActionID, attemptID modulecore.AttemptID, response *tool.ToolResponse, toolErr error) error {
	if ctx == nil {
		return errors.New("context is required")
	}

	attemptStatus, actionStatus, summary := toolAttemptTerminalState(ctx, response, toolErr)
	bookkeepingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	_, _, err := m.CompleteAttempt(bookkeepingCtx, actionID, attemptID, attemptStatus, actionStatus, summary)
	return err
}

func toolAttemptTerminalState(ctx context.Context, response *tool.ToolResponse, toolErr error) (domainaction.AttemptStatus, domainaction.Status, string) {
	if response != nil && response.Error == nil && toolErr == nil {
		return domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "tool succeeded"
	}

	switch {
	case errors.Is(toolErr, context.DeadlineExceeded):
		return domainaction.AttemptStatusTimedOut, domainaction.StatusFailed, "tool timed out"
	case errors.Is(toolErr, context.Canceled):
		return domainaction.AttemptStatusCancelled, domainaction.StatusCancelled, "tool cancelled"
	case response != nil && response.Error != nil && response.Error.Code == tool.ErrTimeout:
		return domainaction.AttemptStatusTimedOut, domainaction.StatusFailed, "tool timed out"
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return domainaction.AttemptStatusTimedOut, domainaction.StatusFailed, "tool timed out"
	case errors.Is(ctx.Err(), context.Canceled):
		return domainaction.AttemptStatusCancelled, domainaction.StatusCancelled, "tool cancelled"
	default:
		return domainaction.AttemptStatusFailed, domainaction.StatusFailed, "tool failed"
	}
}

func (m *Manager) GetAction(ctx context.Context, actionID modulecore.ActionID) (domainaction.Action, error) {
	if err := validateContext(ctx); err != nil {
		return domainaction.Action{}, err
	}
	if err := actionID.Validate(); err != nil {
		return domainaction.Action{}, fmt.Errorf("action_id is invalid: %w", err)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := validateContext(ctx); err != nil {
		return domainaction.Action{}, err
	}
	return m.store.GetAction(ctx, actionID)
}

func (m *Manager) ListActions(ctx context.Context, filter domainaction.Filter) ([]domainaction.Action, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	return m.store.ListActions(ctx, filter)
}

func (m *Manager) ListAttempts(ctx context.Context, filter domainaction.AttemptFilter) ([]domainaction.Attempt, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	return m.store.ListAttempts(ctx, filter)
}

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	return ctx.Err()
}

// currentPair reads and verifies the active current pair using the transaction
// store supplied by the caller. The caller owns the transaction boundary.
func (m *Manager) currentPair(tx Store, ctx context.Context, actionID modulecore.ActionID, expectedAttemptID modulecore.AttemptID) (domainaction.Action, domainaction.Attempt, error) {
	action, err := tx.GetAction(ctx, actionID)
	if err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	if err := action.Validate(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	attempt, err := tx.GetAttempt(ctx, expectedAttemptID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("%w: expected attempt %s is unavailable: %v", ErrAttemptConflict, expectedAttemptID, err)
		}
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	if err := attempt.Validate(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	if !action.IsOpen() {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("%w: action %s is closed", ErrAttemptConflict, action.ActionID)
	}
	if action.CurrentAttemptID != expectedAttemptID {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("%w: expected attempt %s is not current for action %s", ErrAttemptConflict, expectedAttemptID, actionID)
	}
	if attempt.ActionID != actionID {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("%w: attempt %s belongs to action %s, want %s", ErrAttemptConflict, expectedAttemptID, attempt.ActionID, actionID)
	}
	if !attempt.IsActive() {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("%w: attempt %s is already closed", ErrAttemptConflict, expectedAttemptID)
	}
	return action, attempt, nil
}

func (m *Manager) closeCurrentAttemptReturning(ctx context.Context, tx Store, actionID modulecore.ActionID, status domainaction.AttemptStatus, completedAt time.Time, summary string) (domainaction.Attempt, error) {
	attempts, err := tx.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: actionID, Status: domainaction.AttemptStatusRunning})
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
	if err := tx.SaveAttempt(ctx, closed); err != nil {
		return domainaction.Attempt{}, err
	}
	return closed, nil
}
