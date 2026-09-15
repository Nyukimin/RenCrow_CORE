package transport

import (
	"fmt"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type RequestStatus string

const (
	RequestStatusSent      RequestStatus = "sent"
	RequestStatusResponded RequestStatus = "responded"
	RequestStatusFailed    RequestStatus = "failed"
)

type Request struct {
	RequestID   modulecore.RequestID `json:"request_id"`
	TaskID      modulecore.TaskID    `json:"task_id"`
	RunID       modulecore.RunID     `json:"run_id"`
	ActionID    modulecore.ActionID  `json:"action_id"`
	AttemptID   modulecore.AttemptID `json:"attempt_id"`
	TraceID     modulecore.TraceID   `json:"trace_id,omitempty"`
	Operation   string               `json:"operation"`
	Sequence    int                  `json:"sequence"`
	Status      RequestStatus        `json:"status"`
	SentAt      time.Time            `json:"sent_at"`
	CompletedAt *time.Time           `json:"completed_at,omitempty"`
	Summary     string               `json:"summary,omitempty"`
}

type Response struct {
	ResponseID  modulecore.ResponseID `json:"response_id"`
	RequestID   modulecore.RequestID  `json:"request_id"`
	TaskID      modulecore.TaskID     `json:"task_id"`
	RunID       modulecore.RunID      `json:"run_id"`
	ActionID    modulecore.ActionID   `json:"action_id"`
	AttemptID   modulecore.AttemptID  `json:"attempt_id"`
	TraceID     modulecore.TraceID    `json:"trace_id,omitempty"`
	ExternalRef string                `json:"external_ref,omitempty"`
	ReceivedAt  time.Time             `json:"received_at"`
}

type RequestFilter struct {
	ActionID  modulecore.ActionID
	AttemptID modulecore.AttemptID
	Status    RequestStatus
}

type ResponseFilter struct {
	RequestID modulecore.RequestID
	ActionID  modulecore.ActionID
	AttemptID modulecore.AttemptID
}

func (r Request) Validate() error {
	if err := r.RequestID.Validate(); err != nil {
		return fmt.Errorf("request_id is invalid: %w", err)
	}
	if err := validateLineage(r.TaskID, r.RunID, r.ActionID, r.AttemptID, r.TraceID); err != nil {
		return err
	}
	if strings.TrimSpace(r.Operation) == "" {
		return fmt.Errorf("operation is required")
	}
	if len(r.Operation) > 128 || strings.ContainsAny(r.Operation, "\r\n\x00") {
		return fmt.Errorf("operation is invalid")
	}
	if r.Sequence <= 0 {
		return fmt.Errorf("sequence must be positive")
	}
	if r.SentAt.IsZero() {
		return fmt.Errorf("sent_at is required")
	}
	switch r.Status {
	case RequestStatusSent:
		if r.CompletedAt != nil {
			return fmt.Errorf("sent request must not have completed_at")
		}
	case RequestStatusResponded, RequestStatusFailed:
		if r.CompletedAt == nil || r.CompletedAt.Before(r.SentAt) {
			return fmt.Errorf("terminal request requires valid completed_at")
		}
	default:
		return fmt.Errorf("invalid request status %q", r.Status)
	}
	return nil
}

func (r Response) Validate() error {
	if err := r.ResponseID.Validate(); err != nil {
		return fmt.Errorf("response_id is invalid: %w", err)
	}
	if err := r.RequestID.Validate(); err != nil {
		return fmt.Errorf("request_id is invalid: %w", err)
	}
	if err := validateLineage(r.TaskID, r.RunID, r.ActionID, r.AttemptID, r.TraceID); err != nil {
		return err
	}
	if r.ReceivedAt.IsZero() {
		return fmt.Errorf("received_at is required")
	}
	if len(r.ExternalRef) > 512 || strings.ContainsAny(r.ExternalRef, "\r\n\x00") {
		return fmt.Errorf("external_ref is invalid")
	}
	return nil
}

func validateLineage(taskID modulecore.TaskID, runID modulecore.RunID, actionID modulecore.ActionID, attemptID modulecore.AttemptID, traceID modulecore.TraceID) error {
	if err := taskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := runID.Validate(); err != nil {
		return fmt.Errorf("run_id is invalid: %w", err)
	}
	if err := actionID.Validate(); err != nil {
		return fmt.Errorf("action_id is invalid: %w", err)
	}
	if err := attemptID.Validate(); err != nil {
		return fmt.Errorf("attempt_id is invalid: %w", err)
	}
	if traceID != "" {
		if err := traceID.Validate(); err != nil {
			return fmt.Errorf("trace_id is invalid: %w", err)
		}
	}
	return nil
}
