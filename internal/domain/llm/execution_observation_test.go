package llm

import (
	"context"
	"strings"
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
	if !strings.HasPrefix(got.RequestID, "llmreq_") {
		t.Fatalf("request_id=%q want independent llmreq_ id", got.RequestID)
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
	ctx := WithExecutionObservation(context.Background(), ExecutionObservation{
		RequestID: "request-explicit", TraceID: "trace-explicit", TaskID: taskID, SessionID: "session-explicit",
	})
	got, ok := ExecutionObservationFromContext(ctx)
	if !ok || got.RequestID != "request-explicit" || got.TaskID != taskID {
		t.Fatalf("explicit request identity was not preserved: %+v ok=%v", got, ok)
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
	if !strings.HasPrefix(got.RequestID, "llmreq_") {
		t.Fatalf("request_id=%q want generated independent request id", got.RequestID)
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
	if !ok || !strings.HasPrefix(first.RequestID, "llmreq_") {
		t.Fatalf("generated observation=%+v ok=%v", first, ok)
	}
	second, ok := ExecutionObservationFromContext(ctx)
	if !ok || second.RequestID != first.RequestID {
		t.Fatalf("request_id changed: first=%q second=%q", first.RequestID, second.RequestID)
	}
}

func TestWithExecutionObservationDefaultsPreservesUpstreamAttribution(t *testing.T) {
	taskID := modulecore.NewTaskID()
	ctx := WithExecutionObservation(context.Background(), ExecutionObservation{
		RequestID: "request-1", TaskID: taskID, Caller: "heartbeat.backlog", Purpose: "process_backlog_item",
	})
	ctx = WithExecutionObservationDefaults(ctx, ExecutionObservation{
		RequestID: "other", TaskID: modulecore.NewTaskID(), Initiator: "shiro", Caller: "agent.shiro", Purpose: "execute_ops_task",
	})

	got, ok := ExecutionObservationFromContext(ctx)
	if !ok || got.RequestID != "request-1" || got.TaskID != taskID || got.Initiator != "shiro" || got.Caller != "heartbeat.backlog" || got.Purpose != "process_backlog_item" {
		t.Fatalf("unexpected merged observation: %+v ok=%v", got, ok)
	}
}
