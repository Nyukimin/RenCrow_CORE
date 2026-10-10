package task

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// NativeOPSResumeSource is the exact, owner-read Harness execution that a new
// CORE Run will resume. It contains IDs and a result digest, never RunResult or
// accepted user input bytes.
type NativeOPSResumeSource struct {
	ActionID        modulecore.ActionID  `json:"action_id"`
	AttemptID       modulecore.AttemptID `json:"attempt_id"`
	HarnessTaskID   string               `json:"harness_task_id"`
	ThreadID        string               `json:"thread_id"`
	RunID           string               `json:"run_id"`
	ReceiptID       string               `json:"receipt_id"`
	PreviousRunID   string               `json:"previous_run_id,omitempty"`
	CheckpointID    *string              `json:"checkpoint_id,omitempty"`
	RunResultSHA256 string               `json:"run_result_sha256"`
}

func (s NativeOPSResumeSource) Validate() error {
	if err := s.ActionID.Validate(); err != nil {
		return fmt.Errorf("source action_id is invalid: %w", err)
	}
	if err := s.AttemptID.Validate(); err != nil {
		return fmt.Errorf("source attempt_id is invalid: %w", err)
	}
	for name, value := range map[string]string{
		"harness_task_id": s.HarnessTaskID, "thread_id": s.ThreadID,
		"run_id": s.RunID, "receipt_id": s.ReceiptID,
	} {
		if !validNativeResumeID(value) {
			return fmt.Errorf("source %s is invalid", name)
		}
	}
	if s.PreviousRunID != "" && !validNativeResumeID(s.PreviousRunID) {
		return fmt.Errorf("source previous_run_id is invalid")
	}
	if s.CheckpointID != nil && !validNativeResumeID(*s.CheckpointID) {
		return fmt.Errorf("source checkpoint_id is invalid")
	}
	if !validNativeResumeSHA256(s.RunResultSHA256) {
		return fmt.Errorf("source run_result_sha256 is invalid")
	}
	return nil
}

// NativeOPSResumeClaim is Task-owned idempotency and provenance for one
// explicit user-requested Resume. Claims append and remain immutable.
type NativeOPSResumeClaim struct {
	RequestID         string                `json:"request_id"`
	OwnerUserID       string                `json:"owner_user_id"`
	PayloadSHA256     string                `json:"payload_sha256"`
	ExpectedCoreRunID modulecore.RunID      `json:"expected_core_run_id"`
	NewCoreRunID      modulecore.RunID      `json:"new_core_run_id"`
	TraceID           modulecore.TraceID    `json:"trace_id"`
	Source            NativeOPSResumeSource `json:"source"`
	WriterGeneration  uint64                `json:"writer_generation"`
	CreatedAt         time.Time             `json:"created_at"`
}

func (c NativeOPSResumeClaim) Validate(task Task) error {
	if c.RequestID == "" || strings.TrimSpace(c.RequestID) != c.RequestID || !utf8.ValidString(c.RequestID) || len(c.RequestID) > 128 {
		return fmt.Errorf("native resume request_id is invalid")
	}
	if c.OwnerUserID == "" || strings.TrimSpace(c.OwnerUserID) != c.OwnerUserID || !utf8.ValidString(c.OwnerUserID) {
		return fmt.Errorf("native resume owner_user_id is invalid")
	}
	if !validNativeResumeSHA256(c.PayloadSHA256) {
		return fmt.Errorf("native resume payload_sha256 is invalid")
	}
	if err := c.ExpectedCoreRunID.Validate(); err != nil {
		return fmt.Errorf("native resume expected_core_run_id is invalid: %w", err)
	}
	if err := c.NewCoreRunID.Validate(); err != nil {
		return fmt.Errorf("native resume new_core_run_id is invalid: %w", err)
	}
	if err := c.TraceID.Validate(); err != nil {
		return fmt.Errorf("native resume trace_id is invalid: %w", err)
	}
	if c.WriterGeneration == 0 || c.CreatedAt.IsZero() || c.NewCoreRunID == c.ExpectedCoreRunID {
		return fmt.Errorf("native resume claim generation, time, or run linkage is invalid")
	}
	if err := c.Source.Validate(); err != nil {
		return err
	}
	if !ValidCriteriaRevision(task.ExpectedCriteriaRevision) {
		return fmt.Errorf("native resume requires the immutable Task criteria revision")
	}
	if task.AcceptedOPSClaim == nil || task.AcceptedOPSClaim.ReceiptRef.OwnerID != c.OwnerUserID ||
		task.AcceptedOPSClaim.BackendSelection != "shiro_native_coding_v1" {
		return fmt.Errorf("native resume owner or backend differs from the accepted OPS claim")
	}
	return nil
}

func (t Task) ValidateNativeOPSResumeClaims() error {
	seenRequests := make(map[string]NativeOPSResumeClaim, len(t.NativeResumeClaims))
	seenRuns := make(map[modulecore.RunID]struct{}, len(t.NativeResumeClaims))
	for _, claim := range t.NativeResumeClaims {
		if err := claim.Validate(t); err != nil {
			return err
		}
		if _, ok := seenRequests[claim.RequestID]; ok {
			return fmt.Errorf("native resume request_id is duplicated")
		}
		if _, ok := seenRuns[claim.NewCoreRunID]; ok {
			return fmt.Errorf("native resume new_core_run_id is duplicated")
		}
		seenRequests[claim.RequestID] = claim
		seenRuns[claim.NewCoreRunID] = struct{}{}
	}
	return nil
}

// NativeOPSResumeClaimsExtend reports whether next preserves every prior
// immutable claim in order and only appends new claims.
func NativeOPSResumeClaimsExtend(previous, next []NativeOPSResumeClaim) bool {
	if len(next) < len(previous) {
		return false
	}
	for index, oldClaim := range previous {
		newClaim := next[index]
		if oldClaim.RequestID != newClaim.RequestID || oldClaim.OwnerUserID != newClaim.OwnerUserID ||
			oldClaim.PayloadSHA256 != newClaim.PayloadSHA256 || oldClaim.ExpectedCoreRunID != newClaim.ExpectedCoreRunID ||
			oldClaim.NewCoreRunID != newClaim.NewCoreRunID || oldClaim.TraceID != newClaim.TraceID ||
			oldClaim.WriterGeneration != newClaim.WriterGeneration || !oldClaim.CreatedAt.Equal(newClaim.CreatedAt) ||
			!sameNativeOPSResumeSource(oldClaim.Source, newClaim.Source) {
			return false
		}
	}
	return true
}

func sameNativeOPSResumeSource(left, right NativeOPSResumeSource) bool {
	if left.ActionID != right.ActionID || left.AttemptID != right.AttemptID || left.HarnessTaskID != right.HarnessTaskID ||
		left.ThreadID != right.ThreadID || left.RunID != right.RunID || left.ReceiptID != right.ReceiptID ||
		left.PreviousRunID != right.PreviousRunID || left.RunResultSHA256 != right.RunResultSHA256 {
		return false
	}
	if left.CheckpointID == nil || right.CheckpointID == nil {
		return left.CheckpointID == nil && right.CheckpointID == nil
	}
	return *left.CheckpointID == *right.CheckpointID
}

func NativeOPSResumePayloadSHA256(taskID modulecore.TaskID, expectedCoreRunID modulecore.RunID) string {
	canonical := "rencrow-native-resume/v1\x00" + string(taskID) + "\x00" + string(expectedCoreRunID)
	hash := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(hash[:])
}

func validNativeResumeID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && utf8.ValidString(value) && len(value) <= 256
}

func validNativeResumeSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
