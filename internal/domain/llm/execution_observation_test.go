package llm

import (
	"context"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestWithExecutionObservationKeepsOperationAttributionWithoutTransportIdentity(t *testing.T) {
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
	if got.TaskID != taskID || got.TraceID != string(traceID) {
		t.Fatalf("task/trace identity drifted: %+v", got)
	}
	if got.Initiator != "shiro" || got.Caller != "idlechat.daily_source_brief" || got.Purpose != "translate_article" {
		t.Fatalf("unexpected observation: %+v", got)
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

func TestWithExecutionObservationPreservesBackgroundAttribution(t *testing.T) {
	ctx := WithExecutionObservation(context.Background(), ExecutionObservation{
		Initiator: "shiro",
		Caller:    "memory.profile_promotion",
		Purpose:   "extract_profile_candidates",
	})

	first, ok := ExecutionObservationFromContext(ctx)
	if !ok {
		t.Fatalf("generated observation=%+v ok=%v", first, ok)
	}
	second, ok := ExecutionObservationFromContext(ctx)
	if !ok || second.Caller != first.Caller || second.Purpose != first.Purpose {
		t.Fatalf("operation attribution changed: first=%+v second=%+v", first, second)
	}
}

func TestWithExecutionObservationDefaultsPreservesUpstreamAttribution(t *testing.T) {
	taskID := modulecore.NewTaskID()
	ctx := WithExecutionObservation(context.Background(), ExecutionObservation{
		TaskID: taskID, Caller: "heartbeat.backlog", Purpose: "process_backlog_item",
	})
	ctx = WithExecutionObservationDefaults(ctx, ExecutionObservation{
		TaskID: modulecore.NewTaskID(), Initiator: "shiro", Caller: "agent.shiro", Purpose: "execute_ops_task",
	})

	got, ok := ExecutionObservationFromContext(ctx)
	if !ok || got.TaskID != taskID || got.Initiator != "shiro" || got.Caller != "heartbeat.backlog" || got.Purpose != "process_backlog_item" {
		t.Fatalf("unexpected merged observation: %+v ok=%v", got, ok)
	}
}
