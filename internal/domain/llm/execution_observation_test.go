package llm

import (
	"context"
	"strings"
	"testing"
)

func TestWithExecutionObservationReusesExistingCorrelationID(t *testing.T) {
	ctx := WithExecutionObservation(context.Background(), ExecutionObservation{
		TraceID:   "trace-1",
		JobID:     "job-1",
		SessionID: "session-1",
		Initiator: "shiro",
		Caller:    "idlechat.daily_source_brief",
		Purpose:   "translate_article",
	})

	got, ok := ExecutionObservationFromContext(ctx)
	if !ok {
		t.Fatal("execution observation is missing")
	}
	if got.RequestID != "trace-1" {
		t.Fatalf("request_id=%q want trace-1", got.RequestID)
	}
	if got.Initiator != "shiro" || got.Caller != "idlechat.daily_source_brief" || got.Purpose != "translate_article" {
		t.Fatalf("unexpected observation: %+v", got)
	}
}

func TestWithExecutionObservationGeneratesOneBackgroundRequestID(t *testing.T) {
	ctx := WithExecutionObservation(context.Background(), ExecutionObservation{
		Initiator: "shiro",
		Caller:    "memory.profile_promotion",
		Purpose:   "extract_profile_candidates",
	})

	first, ok := ExecutionObservationFromContext(ctx)
	if !ok || !strings.HasPrefix(first.RequestID, "llmreq_") {
		t.Fatalf("generated observation=%+v ok=%v", first, ok)
	}
	second, ok := ExecutionObservationFromContext(ctx)
	if !ok || second.RequestID != first.RequestID {
		t.Fatalf("request_id changed: first=%q second=%q", first.RequestID, second.RequestID)
	}
}

func TestWithExecutionObservationDefaultsPreservesUpstreamAttribution(t *testing.T) {
	ctx := WithExecutionObservation(context.Background(), ExecutionObservation{
		RequestID: "job-1", Caller: "heartbeat.backlog", Purpose: "process_backlog_item",
	})
	ctx = WithExecutionObservationDefaults(ctx, ExecutionObservation{
		RequestID: "other", Initiator: "shiro", Caller: "agent.shiro", Purpose: "execute_ops_task",
	})

	got, ok := ExecutionObservationFromContext(ctx)
	if !ok || got.RequestID != "job-1" || got.Initiator != "shiro" || got.Caller != "heartbeat.backlog" || got.Purpose != "process_backlog_item" {
		t.Fatalf("unexpected merged observation: %+v ok=%v", got, ok)
	}
}
