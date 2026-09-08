package task

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// RunStartReason records the canonical reason a Task execution was started.
type RunStartReason string

const (
	RunStartReasonFirst                RunStartReason = "first"
	RunStartReasonProcessRestartResume RunStartReason = "process_restart_resume"
	RunStartReasonLeaseReacquire       RunStartReason = "lease_reacquire"
	RunStartReasonAgentReassignment    RunStartReason = "agent_reassignment"
	RunStartReasonCheckpointResume     RunStartReason = "checkpoint_resume"
	RunStartReasonExplicitRerun        RunStartReason = "explicit_rerun"
)

// RunStatus is the lifecycle state of one execution of a Task.
type RunStatus string

const (
	RunStatusRunning     RunStatus = "running"
	RunStatusSucceeded   RunStatus = "succeeded"
	RunStatusFailed      RunStatus = "failed"
	RunStatusCancelled   RunStatus = "cancelled"
	RunStatusWaiting     RunStatus = "waiting"
	RunStatusBlocked     RunStatus = "blocked"
	RunStatusInterrupted RunStatus = "interrupted"
	RunStatusReassigned  RunStatus = "reassigned"
	RunStatusSuperseded  RunStatus = "superseded"
)

// Run is one non-hierarchical execution belonging to exactly one Task.
type Run struct {
	// WriterGeneration is a store-local fencing value, not an identity. Zero marks historical unbound records.
	WriterGeneration      uint64            `json:"writer_generation,omitempty"`
	RunID                 modulecore.RunID  `json:"run_id"`
	TaskID                modulecore.TaskID `json:"task_id"`
	StartReason           RunStartReason    `json:"start_reason"`
	Assignee              string            `json:"assignee,omitempty"`
	Status                RunStatus         `json:"status"`
	StartedAt             time.Time         `json:"started_at"`
	CompletedAt           *time.Time        `json:"completed_at,omitempty"`
	Summary               string            `json:"summary,omitempty"`
	StartCheckpointSHA256 string            `json:"start_checkpoint_sha256,omitempty"`
}

// RunFilter selects persisted Run history. Results are returned chronologically.
type RunFilter struct {
	TaskID modulecore.TaskID
	Status RunStatus
	Limit  int
}

func (r Run) Validate() error {
	if err := r.RunID.Validate(); err != nil {
		return fmt.Errorf("run_id is invalid: %w", err)
	}
	if err := r.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	if !ValidRunStartReason(r.StartReason) {
		return fmt.Errorf("invalid run start reason: %s", r.StartReason)
	}
	if !ValidRunStatus(r.Status) {
		return fmt.Errorf("invalid run status: %s", r.Status)
	}
	if r.StartedAt.IsZero() {
		return fmt.Errorf("started_at is required")
	}
	if r.Status == RunStatusRunning && r.CompletedAt != nil {
		return fmt.Errorf("running run must not have completed_at")
	}
	if r.Status != RunStatusRunning && r.CompletedAt == nil {
		return fmt.Errorf("terminal run requires completed_at")
	}
	if r.CompletedAt != nil && r.CompletedAt.Before(r.StartedAt) {
		return fmt.Errorf("completed_at must not precede started_at")
	}
	if err := ValidateStartCheckpointSHA256(r.StartCheckpointSHA256); err != nil {
		return err
	}
	return nil
}

// ValidateStartCheckpointSHA256 validates the optional source checkpoint
// evidence attached to a newly issued Run. It is evidence only: it is not a
// Run identity or a predecessor relation.
func ValidateStartCheckpointSHA256(value string) error {
	if value == "" {
		return nil
	}
	if len(value) != sha256.Size*2 {
		return fmt.Errorf("start_checkpoint_sha256 must be lowercase hex SHA-256")
	}
	if value != strings.ToLower(value) {
		return fmt.Errorf("start_checkpoint_sha256 must be lowercase hex SHA-256")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("start_checkpoint_sha256 must be lowercase hex SHA-256: %w", err)
	}
	return nil
}

func ValidRunStartReason(reason RunStartReason) bool {
	switch reason {
	case RunStartReasonFirst,
		RunStartReasonProcessRestartResume,
		RunStartReasonLeaseReacquire,
		RunStartReasonAgentReassignment,
		RunStartReasonCheckpointResume,
		RunStartReasonExplicitRerun:
		return true
	default:
		return false
	}
}

func ValidRunStatus(status RunStatus) bool {
	switch status {
	case RunStatusRunning,
		RunStatusSucceeded,
		RunStatusFailed,
		RunStatusCancelled,
		RunStatusWaiting,
		RunStatusBlocked,
		RunStatusInterrupted,
		RunStatusReassigned,
		RunStatusSuperseded:
		return true
	default:
		return false
	}
}

func IsRunTerminal(status RunStatus) bool {
	return ValidRunStatus(status) && status != RunStatusRunning
}

func CanRunTransition(from, to RunStatus) bool {
	if from == to {
		return true
	}
	if !ValidRunStatus(from) || !ValidRunStatus(to) || IsRunTerminal(from) {
		return false
	}
	return to != RunStatusRunning
}

// CanStartRunFromCheckpoint reports whether a persisted predecessor may issue
// the requested checkpoint-bound successor. The policy is shared by every
// checkpoint consumer; the Task owner still performs the atomic CAS and actor
// checks around this decision.
func CanStartRunFromCheckpoint(status RunStatus, reason RunStartReason) bool {
	switch reason {
	case RunStartReasonCheckpointResume:
		return status == RunStatusWaiting || status == RunStatusInterrupted
	case RunStartReasonExplicitRerun:
		return status == RunStatusSucceeded || status == RunStatusFailed || status == RunStatusCancelled
	default:
		return false
	}
}

func (r Run) IsActive() bool {
	return r.Status == RunStatusRunning
}

// Close returns a terminal copy of an active Run. A closed Run is immutable.
func (r Run) Close(status RunStatus, completedAt time.Time, summary string) (Run, error) {
	if !r.IsActive() {
		return Run{}, fmt.Errorf("run %s is already closed", r.RunID)
	}
	if !IsRunTerminal(status) {
		return Run{}, fmt.Errorf("run close status must be terminal: %s", status)
	}
	if completedAt.IsZero() {
		return Run{}, fmt.Errorf("completed_at is required")
	}
	if completedAt.Before(r.StartedAt) {
		return Run{}, fmt.Errorf("completed_at must not precede started_at")
	}
	r.Status = status
	r.CompletedAt = &completedAt
	if summary != "" {
		r.Summary = summary
	}
	return r, r.Validate()
}
