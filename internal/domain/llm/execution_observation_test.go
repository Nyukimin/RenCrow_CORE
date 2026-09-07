package llm

import (
	"context"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestWithExecutionObservationKeepsTaskAndGeneratesIndependentRequestID(t *testing.T) {
	taskID := modulecore.NewTaskID()
	traceID := modulecore.NewTraceID()
	ctx := WithExecutionObservation(context.Background(), ExecutionObservation{
		TraceID:   string(traceID),
		TaskID:    taskID,
		SessionID: "session-1",
		Initiator: "shiro",
		Caller:    "idlechat.daily_source_brief",
		Purpose:   "translate_article",
	})

	got, ok := ExecutionObservationFromContext(ctx)
	if !ok {
		t.Fatal("execution observation is missing")
	}
	if err := got.RequestID.Validate(); err != nil {
		t.Fatalf("request_id=%q want canonical RequestID: %v", got.RequestID, err)
	}
	if got.TaskID != taskID || got.TraceID != string(traceID) {
		t.Fatalf("task/trace identity drifted: %+v", got)
	}
	if got.Initiator != "shiro" || got.Caller != "idlechat.daily_source_brief" || got.Purpose != "translate_article" {
		t.Fatalf("unexpected observation: %+v", got)
	}
}

func TestWithExecutionObservationPreservesExplicitRequestID(t *testing.T) {
	taskID := modulecore.NewTaskID()
	requestID := modulecore.NewRequestID()
	ctx := WithExecutionObservation(context.Background(), ExecutionObservation{
		RequestID: requestID, TraceID: "trace-explicit", TaskID: taskID, SessionID: "session-explicit",
	})
	got, ok := ExecutionObservationFromContext(ctx)
	if !ok || got.RequestID != requestID || got.TaskID != taskID {
		t.Fatalf("explicit request identity was not preserved: %+v ok=%v", got, ok)
	}
}

func TestWithExecutionObservationReplacesNonCanonicalRequestID(t *testing.T) {
	ctx := WithExecutionObservation(context.Background(), ExecutionObservation{RequestID: modulecore.RequestID("request-explicit")})
	got, ok := ExecutionObservationFromContext(ctx)
	if !ok {
		t.Fatal("execution observation is missing")
	}
	if got.RequestID == "request-explicit" {
		t.Fatal("non-canonical request_id must be replaced")
	}
	if err := got.RequestID.Validate(); err != nil {
		t.Fatalf("replaced request_id invalid: %v", err)
	}
}

func TestWithExecutionObservationDropsMalformedTaskID(t *testing.T) {
	ctx := WithExecutionObservation(context.Background(), ExecutionObservation{TaskID: modulecore.TaskID("not-a-task-id")})
	got, ok := ExecutionObservationFromContext(ctx)
	if !ok {
		t.Fatal("execution observation is missing")
	}
	if got.TaskID != "" {
		t.Fatalf("malformed task_id must not be propagated: %+v", got)
	}
	if err := got.RequestID.Validate(); err != nil {
		t.Fatalf("request_id=%q want generated canonical request id: %v", got.RequestID, err)
	}
}

func TestWithExecutionObservationDoesNotRewriteTaskIDWhitespace(t *testing.T) {
	taskID := modulecore.NewTaskID()
	ctx := WithExecutionObservation(context.Background(), ExecutionObservation{TaskID: modulecore.TaskID(" " + taskID.String())})
	got, ok := ExecutionObservationFromContext(ctx)
	if !ok {
		t.Fatal("execution observation is missing")
	}
	if got.TaskID != "" {
		t.Fatalf("non-canonical task_id must be rejected rather than normalized: %+v", got)
	}
}

func TestWithExecutionObservationGeneratesOneBackgroundRequestID(t *testing.T) {
	ctx := WithExecutionObservation(context.Background(), ExecutionObservation{
		Initiator: "shiro",
		Caller:    "memory.profile_promotion",
		Purpose:   "extract_profile_candidates",
	})

	first, ok := ExecutionObservationFromContext(ctx)
	if !ok || first.RequestID.Validate() != nil {
		t.Fatalf("generated observation=%+v ok=%v", first, ok)
	}
	second, ok := ExecutionObservationFromContext(ctx)
	if !ok || second.RequestID != first.RequestID {
		t.Fatalf("request_id changed: first=%q second=%q", first.RequestID, second.RequestID)
	}
}

func TestWithExecutionObservationDefaultsPreservesUpstreamAttribution(t *testing.T) {
	taskID := modulecore.NewTaskID()
	requestID := modulecore.NewRequestID()
	ctx := WithExecutionObservation(context.Background(), ExecutionObservation{
		RequestID: requestID, TaskID: taskID, Caller: "heartbeat.backlog", Purpose: "process_backlog_item",
	})
	ctx = WithExecutionObservationDefaults(ctx, ExecutionObservation{
		RequestID: modulecore.NewRequestID(), TaskID: modulecore.NewTaskID(), Initiator: "shiro", Caller: "agent.shiro", Purpose: "execute_ops_task",
	})

	got, ok := ExecutionObservationFromContext(ctx)
	if !ok || got.RequestID != requestID || got.TaskID != taskID || got.Initiator != "shiro" || got.Caller != "heartbeat.backlog" || got.Purpose != "process_backlog_item" {
		t.Fatalf("unexpected merged observation: %+v ok=%v", got, ok)
	}
}
