package orchestrator

import (
	"context"

	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const orchestrationLLMPurpose = "route_and_execute"

// withOrchestrationLLMObservation binds the root Task attribution before any
// Mio route-decision or other LLM-capable pre-routing path. The LLM domain
// owns independent RequestID generation; this layer only supplies canonical
// ingress correlation and stable orchestration attribution.
func withOrchestrationLLMObservation(ctx context.Context, taskID modulecore.TaskID, traceID modulecore.TraceID, sessionID, caller string) context.Context {
	return domainllm.WithExecutionObservation(ctx, domainllm.ExecutionObservation{
		TaskID:    taskID,
		TraceID:   string(traceID),
		SessionID: sessionID,
		Initiator: "mio",
		Caller:    caller,
		Purpose:   orchestrationLLMPurpose,
	})
}

// withOrchestrationLLMTask changes only the execution Task attribution after
// activation. Existing RequestID, TraceID, SessionID, and caller metadata are
// retained so route decision and execution remain one LLM observation chain.
func withOrchestrationLLMTask(ctx context.Context, taskID modulecore.TaskID) context.Context {
	observation, ok := domainllm.ExecutionObservationFromContext(ctx)
	if !ok {
		return ctx
	}
	if taskID.Validate() != nil {
		taskID = ""
	}
	observation.TaskID = taskID
	return domainllm.WithExecutionObservation(ctx, observation)
}
