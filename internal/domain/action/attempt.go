package action

import (
	"fmt"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// AttemptStartReason records why a physical attempt of an Action began.
type AttemptStartReason string

const (
	AttemptStartReasonFirst    AttemptStartReason = "first"
	AttemptStartReasonRetry    AttemptStartReason = "retry"
	AttemptStartReasonFallback AttemptStartReason = "fallback"
	AttemptStartReasonTimeout  AttemptStartReason = "timeout"
	AttemptStartReasonCancel   AttemptStartReason = "cancel_recovery"
)

// AttemptStatus is the lifecycle state of one physical Action attempt.
type AttemptStatus string

const (
	AttemptStatusRunning   AttemptStatus = "running"
	AttemptStatusSucceeded AttemptStatus = "succeeded"
	AttemptStatusFailed    AttemptStatus = "failed"
	AttemptStatusCancelled AttemptStatus = "cancelled"
	AttemptStatusTimedOut  AttemptStatus = "timed_out"
)

// Attempt is one non-hierarchical physical try belonging to exactly one Action.
type Attempt struct {
	AttemptID   modulecore.AttemptID `json:"attempt_id"`
	ActionID    modulecore.ActionID  `json:"action_id"`
	StartReason AttemptStartReason   `json:"start_reason"`
	Status      AttemptStatus        `json:"status"`
	StartedAt   time.Time            `json:"started_at"`
	CompletedAt *time.Time           `json:"completed_at,omitempty"`
	Summary     string               `json:"summary,omitempty"`
}

// AttemptFilter selects persisted Attempt history. Results are chronological.
type AttemptFilter struct {
	ActionID modulecore.ActionID
	Status   AttemptStatus
	Limit    int
}

func (a Attempt) Validate() error {
	if err := a.AttemptID.Validate(); err != nil {
		return fmt.Errorf("attempt_id is invalid: %w", err)
	}
	if err := a.ActionID.Validate(); err != nil {
		return fmt.Errorf("action_id is invalid: %w", err)
	}
	if !ValidAttemptStartReason(a.StartReason) {
		return fmt.Errorf("invalid attempt start reason: %s", a.StartReason)
	}
	if !ValidAttemptStatus(a.Status) {
		return fmt.Errorf("invalid attempt status: %s", a.Status)
	}
	if a.StartedAt.IsZero() {
		return fmt.Errorf("started_at is required")
	}
	if a.Status == AttemptStatusRunning && a.CompletedAt != nil {
		return fmt.Errorf("running attempt must not have completed_at")
	}
	if a.Status != AttemptStatusRunning && a.CompletedAt == nil {
		return fmt.Errorf("terminal attempt requires completed_at")
	}
	if a.CompletedAt != nil && a.CompletedAt.Before(a.StartedAt) {
		return fmt.Errorf("completed_at must not precede started_at")
	}
	return nil
}

func ValidAttemptStartReason(reason AttemptStartReason) bool {
	switch reason {
	case AttemptStartReasonFirst, AttemptStartReasonRetry, AttemptStartReasonFallback,
		AttemptStartReasonTimeout, AttemptStartReasonCancel:
		return true
	default:
		return false
	}
}

func ValidAttemptStatus(status AttemptStatus) bool {
	switch status {
	case AttemptStatusRunning, AttemptStatusSucceeded, AttemptStatusFailed,
		AttemptStatusCancelled, AttemptStatusTimedOut:
		return true
	default:
		return false
	}
}

func IsAttemptTerminal(status AttemptStatus) bool {
	return ValidAttemptStatus(status) && status != AttemptStatusRunning
}

func (a Attempt) IsActive() bool {
	return a.Status == AttemptStatusRunning
}

// Close returns a terminal copy of an active Attempt.
func (a Attempt) Close(status AttemptStatus, completedAt time.Time, summary string) (Attempt, error) {
	if !a.IsActive() {
		return Attempt{}, fmt.Errorf("attempt %s is already closed", a.AttemptID)
	}
	if !IsAttemptTerminal(status) {
		return Attempt{}, fmt.Errorf("attempt close status must be terminal: %s", status)
	}
	if completedAt.IsZero() {
		return Attempt{}, fmt.Errorf("completed_at is required")
	}
	if completedAt.Before(a.StartedAt) {
		return Attempt{}, fmt.Errorf("completed_at must not precede started_at")
	}
	a.Status = status
	a.CompletedAt = &completedAt
	if summary != "" {
		a.Summary = summary
	}
	return a, a.Validate()
}
