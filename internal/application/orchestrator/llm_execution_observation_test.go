package orchestrator

import (
	"context"
	"testing"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestOrchestrationLLMObservationRetargetPreservesCorrelation(t *testing.T) {
	rootTaskID := modulecore.NewTaskID()
	childTaskID := modulecore.NewTaskID()
	traceID := modulecore.NewTraceID()
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), observationTestContextKey{}, "preserve"))
	defer cancel()

	ctx = withOrchestrationLLMObservation(ctx, rootTaskID, traceID, "session-observation", "orchestrator.test")
	rootObservation, ok := domainllm.ExecutionObservationFromContext(ctx)
	if !ok {
		t.Fatal("root observation is missing")
	}
	ctx = withOrchestrationLLMTask(ctx, childTaskID)
	childObservation, ok := domainllm.ExecutionObservationFromContext(ctx)
	if !ok {
		t.Fatal("child observation is missing")
	}
	if rootObservation.TaskID != rootTaskID || childObservation.TaskID != childTaskID {
		t.Fatalf("task attribution = root=%s child=%s", rootObservation.TaskID, childObservation.TaskID)
	}
	if rootObservation.RequestID == "" || childObservation.RequestID != rootObservation.RequestID {
		t.Fatalf("request attribution changed: root=%q child=%q", rootObservation.RequestID, childObservation.RequestID)
	}
	if childObservation.TraceID != string(traceID) || childObservation.SessionID != "session-observation" || childObservation.Caller != "orchestrator.test" || childObservation.Initiator != "mio" || childObservation.Purpose != "route_and_execute" {
		t.Fatalf("child observation lost correlation: %+v", childObservation)
	}
	ctx = domainllm.WithExecutionObservationDefaults(ctx, domainllm.ExecutionObservation{
		RequestID: "downstream-request",
		TraceID:   string(modulecore.NewTraceID()),
		TaskID:    modulecore.NewTaskID(),
		SessionID: "downstream-session",
		Caller:    "downstream",
		Purpose:   "downstream",
	})
	protectedObservation, ok := domainllm.ExecutionObservationFromContext(ctx)
	if !ok || protectedObservation.RequestID != childObservation.RequestID || protectedObservation.TaskID != childTaskID || protectedObservation.TraceID != childObservation.TraceID || protectedObservation.SessionID != childObservation.SessionID || protectedObservation.Caller != childObservation.Caller || protectedObservation.Purpose != childObservation.Purpose {
		t.Fatalf("downstream defaults overwrote orchestration attribution: %+v", protectedObservation)
	}
	if got := ctx.Value(observationTestContextKey{}); got != "preserve" {
		t.Fatalf("context value was not preserved: %v", got)
	}
}

type observationTestContextKey struct{}

func TestMessageOrchestratorPropagatesRootThenChildLLMObservation(t *testing.T) {
	rootTaskID := modulecore.NewTaskID()
	traceID := modulecore.NewTraceID()
	var routeObservation, executionObservation domainllm.ExecutionObservation
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteOPS, 1, "ops"),
		decideFunc: func(ctx context.Context, _ domainconversation.TurnInput) (routing.Decision, error) {
			routeObservation, _ = domainllm.ExecutionObservationFromContext(ctx)
			return routing.NewDecision(routing.RouteOPS, 1, "ops"), nil
		},
	}
	shiro := &mockShiroAgent{
		response: "shiro response",
		executeFunc: func(ctx context.Context, _ domainconversation.TurnInput) (string, error) {
			executionObservation, _ = domainllm.ExecutionObservationFromContext(ctx)
			return "shiro response", nil
		},
	}
	manager := newRecordingTaskLifecycleManager()
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, shiro, nil, nil, nil, nil, nil)
	orch.SetTaskLifecycleManager(manager)

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		RootTaskID:  rootTaskID.String(),
		TraceID:     string(traceID),
		SessionID:   "message-observation-session",
		Channel:     "line",
		ChatID:      "message-observation-chat",
		UserMessage: "run operation",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.TaskID == rootTaskID.String() || resp.RootTaskID != rootTaskID.String() {
		t.Fatalf("response task graph = %+v", resp)
	}
	assertOrchestrationObservation(t, routeObservation, rootTaskID, traceID, resp.SessionID, "orchestrator.message")
	assertOrchestrationObservation(t, executionObservation, modulecore.TaskID(resp.TaskID), traceID, resp.SessionID, "orchestrator.message")
	if executionObservation.RequestID != routeObservation.RequestID {
		t.Fatalf("request ID was regenerated across activation: route=%q execution=%q", routeObservation.RequestID, executionObservation.RequestID)
	}
	if executionObservation.TaskID == rootTaskID {
		t.Fatalf("OPS execution must use child task: %+v", executionObservation)
	}
}

type observationDistributedMioAgent struct {
	decision         routing.Decision
	routeObservation domainllm.ExecutionObservation
	chatObservation  domainllm.ExecutionObservation
	decideCallCount  int
	chatCallCount    int
}

func (m *observationDistributedMioAgent) DecideAction(ctx context.Context, _ domainconversation.TurnInput) (routing.Decision, error) {
	m.decideCallCount++
	m.routeObservation, _ = domainllm.ExecutionObservationFromContext(ctx)
	return m.decision, nil
}

func (m *observationDistributedMioAgent) Chat(ctx context.Context, _ domainconversation.TurnInput) (string, error) {
	m.chatCallCount++
	m.chatObservation, _ = domainllm.ExecutionObservationFromContext(ctx)
	return "mio response", nil
}

func (m *observationDistributedMioAgent) HandleChatCommand(context.Context, string, string) (domainagent.ChatCommandResult, error) {
	return domainagent.ChatCommandResult{Handled: false}, nil
}

func TestDistributedOrchestratorPropagatesRootThenChildLLMObservation(t *testing.T) {
	rootTaskID := modulecore.NewTaskID()
	traceID := modulecore.NewTraceID()
	mio := &observationDistributedMioAgent{decision: routing.NewDecision(routing.RouteOPS, 1, "ops")}
	router := transport.NewMessageRouter()
	defer router.Stop()
	orch := NewDistributedOrchestrator(&distMockSessionRepo{}, mio, router, session.NewCentralMemory(), nil)
	manager := newRecordingTaskLifecycleManager()
	orch.SetTaskLifecycleManager(manager)
	var executionObservation domainllm.ExecutionObservation
	orch.routes.executeToAgent = func(ctx context.Context, targetAgent string, msg domaintransport.Message) (domaintransport.Message, error) {
		executionObservation, _ = domainllm.ExecutionObservationFromContext(ctx)
		response := domaintransport.NewMessage(targetAgent, msg.From, msg.SessionID, msg.TaskID, "distributed response")
		response.Type = domaintransport.MessageTypeResult
		return response, nil
	}

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		RootTaskID:  rootTaskID.String(),
		TraceID:     string(traceID),
		SessionID:   "distributed-observation-session",
		Channel:     "line",
		ChatID:      "distributed-observation-chat",
		UserMessage: "run operation",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.TaskID == rootTaskID.String() || resp.RootTaskID != rootTaskID.String() {
		t.Fatalf("response task graph = %+v", resp)
	}
	assertOrchestrationObservation(t, mio.routeObservation, rootTaskID, traceID, resp.SessionID, "orchestrator.distributed")
	assertOrchestrationObservation(t, executionObservation, modulecore.TaskID(resp.TaskID), traceID, resp.SessionID, "orchestrator.distributed")
	if executionObservation.RequestID != mio.routeObservation.RequestID {
		t.Fatalf("request ID was regenerated across activation: route=%q execution=%q", mio.routeObservation.RequestID, executionObservation.RequestID)
	}
	if executionObservation.TaskID == rootTaskID {
		t.Fatalf("OPS execution must use child task: %+v", executionObservation)
	}
}

func assertOrchestrationObservation(t *testing.T, observation domainllm.ExecutionObservation, taskID modulecore.TaskID, traceID modulecore.TraceID, sessionID, caller string) {
	t.Helper()
	if observation.TaskID != taskID || observation.TraceID != string(traceID) || observation.SessionID != sessionID {
		t.Fatalf("observation correlation = %+v, want task=%s trace=%s session=%s", observation, taskID, traceID, sessionID)
	}
	if observation.RequestID == "" || observation.RequestID == string(taskID) || observation.RequestID == observation.TraceID || observation.RequestID == observation.SessionID {
		t.Fatalf("request ID is not independent: %+v", observation)
	}
	if observation.Initiator != "mio" || observation.Caller != caller || observation.Purpose != "route_and_execute" {
		t.Fatalf("observation attribution = %+v", observation)
	}
	if observation.TraceID == observation.TaskID.String() {
		t.Fatalf("trace and task IDs must remain distinct: %+v", observation)
	}
}
