package llm

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

type executionObservationContextKey struct{}

// ExecutionObservation is prompt-free metadata that identifies why CORE is
// using an LLM. Existing correlation IDs remain authoritative.
type ExecutionObservation struct {
	RequestID string
	TraceID   string
	JobID     string
	SessionID string
	Initiator string
	Caller    string
	Purpose   string
}

// WithExecutionObservation attaches one normalized observation to ctx. When a
// background operation has no existing correlation ID, one request ID is
// generated here and then reused for the lifetime of the returned context.
func WithExecutionObservation(ctx context.Context, observation ExecutionObservation) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	observation = normalizeExecutionObservation(observation)
	return context.WithValue(ctx, executionObservationContextKey{}, observation)
}

// WithExecutionObservationDefaults fills only fields that an upstream caller
// has not already attributed.
func WithExecutionObservationDefaults(ctx context.Context, defaults ExecutionObservation) context.Context {
	current, ok := ExecutionObservationFromContext(ctx)
	if !ok {
		return WithExecutionObservation(ctx, defaults)
	}
	if current.RequestID == "" {
		current.RequestID = defaults.RequestID
	}
	if current.TraceID == "" {
		current.TraceID = defaults.TraceID
	}
	if current.JobID == "" {
		current.JobID = defaults.JobID
	}
	if current.SessionID == "" {
		current.SessionID = defaults.SessionID
	}
	if current.Initiator == "" {
		current.Initiator = defaults.Initiator
	}
	if current.Caller == "" {
		current.Caller = defaults.Caller
	}
	if current.Purpose == "" {
		current.Purpose = defaults.Purpose
	}
	return WithExecutionObservation(ctx, current)
}

// ExecutionObservationFromContext returns the prompt-free LLM observation.
func ExecutionObservationFromContext(ctx context.Context) (ExecutionObservation, bool) {
	if ctx == nil {
		return ExecutionObservation{}, false
	}
	observation, ok := ctx.Value(executionObservationContextKey{}).(ExecutionObservation)
	return observation, ok
}

func normalizeExecutionObservation(observation ExecutionObservation) ExecutionObservation {
	observation.RequestID = strings.TrimSpace(observation.RequestID)
	observation.TraceID = strings.TrimSpace(observation.TraceID)
	observation.JobID = strings.TrimSpace(observation.JobID)
	observation.SessionID = strings.TrimSpace(observation.SessionID)
	observation.Initiator = strings.TrimSpace(observation.Initiator)
	observation.Caller = strings.TrimSpace(observation.Caller)
	observation.Purpose = strings.TrimSpace(observation.Purpose)
	if observation.RequestID == "" {
		observation.RequestID = firstNonEmptyObservationID(observation.TraceID, observation.JobID, observation.SessionID)
	}
	if observation.RequestID == "" {
		observation.RequestID = newLLMRequestID()
	}
	return observation
}

func firstNonEmptyObservationID(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func newLLMRequestID() string {
	return "llmreq_" + uuid.NewString()
}
