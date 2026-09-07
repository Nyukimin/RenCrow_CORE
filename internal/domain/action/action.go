package action

import (
	"errors"
	"fmt"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var ErrNotFound = errors.New("action not found")

// Kind names the logical operation without becoming an identity.
type Kind string

const (
	KindTool             Kind = "tool"
	KindLLM              Kind = "llm"
	KindDCI              Kind = "dci"
	KindSTT              Kind = "stt"
	KindTTS              Kind = "tts"
	KindPlayback         Kind = "playback"
	KindPatchApply       Kind = "patch_apply"
	KindExternalSend     Kind = "external_send"
	KindExternalPRSubmit Kind = "external_pr_submit"
	KindVerification     Kind = "verification"
	KindMemoryPromotion  Kind = "memory_promotion"
	KindPolicyMediation  Kind = "policy_mediation"
)

// Status is the lifecycle state of one logical Action.
type Status string

const (
	StatusOpen       Status = "open"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
	StatusCancelled  Status = "cancelled"
	StatusSuperseded Status = "superseded"
)

// Action is one logical operation belonging to exactly one Task Run.
type Action struct {
	ActionID         modulecore.ActionID  `json:"action_id"`
	TaskID           modulecore.TaskID    `json:"task_id"`
	RunID            modulecore.RunID     `json:"run_id"`
	Kind             Kind                 `json:"kind"`
	Name             string               `json:"name,omitempty"`
	Status           Status               `json:"status"`
	CurrentAttemptID modulecore.AttemptID `json:"current_attempt_id,omitempty"`
	CreatedAt        time.Time            `json:"created_at"`
	UpdatedAt        time.Time            `json:"updated_at"`
	CompletedAt      *time.Time           `json:"completed_at,omitempty"`
	Summary          string               `json:"summary,omitempty"`
}

// Filter selects persisted Actions. Results are returned chronologically.
type Filter struct {
	TaskID modulecore.TaskID
	RunID  modulecore.RunID
	Status Status
	Limit  int
}

func (a Action) Validate() error {
	if err := a.ActionID.Validate(); err != nil {
		return fmt.Errorf("action_id is invalid: %w", err)
	}
	if err := a.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := a.RunID.Validate(); err != nil {
		return fmt.Errorf("run_id is invalid: %w", err)
	}
	if !ValidKind(a.Kind) {
		return fmt.Errorf("invalid action kind: %s", a.Kind)
	}
	if !ValidStatus(a.Status) {
		return fmt.Errorf("invalid action status: %s", a.Status)
	}
	if a.CurrentAttemptID != "" {
		if err := a.CurrentAttemptID.Validate(); err != nil {
			return fmt.Errorf("current_attempt_id is invalid: %w", err)
		}
	}
	if a.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	if a.UpdatedAt.IsZero() {
		return fmt.Errorf("updated_at is required")
	}
	if a.UpdatedAt.Before(a.CreatedAt) {
		return fmt.Errorf("updated_at must not precede created_at")
	}
	if a.Status == StatusOpen && a.CompletedAt != nil {
		return fmt.Errorf("open action must not have completed_at")
	}
	if a.Status != StatusOpen && a.CompletedAt == nil {
		return fmt.Errorf("terminal action requires completed_at")
	}
	if a.CompletedAt != nil && a.CompletedAt.Before(a.CreatedAt) {
		return fmt.Errorf("completed_at must not precede created_at")
	}
	return nil
}

func ValidKind(kind Kind) bool {
	switch kind {
	case KindTool, KindLLM, KindDCI, KindSTT, KindTTS, KindPlayback,
		KindPatchApply, KindExternalSend, KindExternalPRSubmit,
		KindVerification, KindMemoryPromotion, KindPolicyMediation:
		return true
	default:
		return false
	}
}

func ValidStatus(status Status) bool {
	switch status {
	case StatusOpen, StatusSucceeded, StatusFailed, StatusCancelled, StatusSuperseded:
		return true
	default:
		return false
	}
}

func IsTerminal(status Status) bool {
	return ValidStatus(status) && status != StatusOpen
}

func (a Action) IsOpen() bool {
	return a.Status == StatusOpen
}

// Close returns a terminal copy of an open Action.
func (a Action) Close(status Status, completedAt time.Time, summary string) (Action, error) {
	if !a.IsOpen() {
		return Action{}, fmt.Errorf("action %s is already closed", a.ActionID)
	}
	if !IsTerminal(status) {
		return Action{}, fmt.Errorf("action close status must be terminal: %s", status)
	}
	if completedAt.IsZero() {
		return Action{}, fmt.Errorf("completed_at is required")
	}
	if completedAt.Before(a.CreatedAt) {
		return Action{}, fmt.Errorf("completed_at must not precede created_at")
	}
	a.Status = status
	a.CompletedAt = &completedAt
	a.UpdatedAt = completedAt
	if summary != "" {
		a.Summary = summary
	}
	return a, a.Validate()
}

// NormalizeName trims the optional human-readable operation name.
func NormalizeName(name string) string {
	return strings.TrimSpace(name)
}
