package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMessageOrchestrator_RouteChainContract_RoutingDecisionBeforeDispatch(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecisionWithEvidence(routing.RouteCHAT, 0.91, "chat", routing.DecisionEvidence{
			Source:     routing.EvidenceSourceRuleDictionary,
			Matched:    true,
			Route:      routing.RouteCHAT,
			Confidence: 0.91,
			Reason:     "test rule",
		}),
		response: "chat response",
	}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)
	rec := &recordingEventListener{}
	orch.SetEventListener(rec)

	_, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	messageIdx := indexOfEvent(rec.events, "message.received", "user", "mio", "")
	decisionIdx := indexOfEvent(rec.events, "routing.decision", "mio", "", "CHAT")
	startIdx := indexOfEvent(rec.events, "agent.start", "mio", "user", "CHAT")
	responseIdx := indexOfEvent(rec.events, "agent.response", "mio", "user", "CHAT")
	if messageIdx < 0 || decisionIdx < 0 || startIdx < 0 || responseIdx < 0 {
		t.Fatalf("missing route chain events: %#v", rec.events)
	}
	if !(messageIdx < decisionIdx && decisionIdx < startIdx && startIdx < responseIdx) {
		t.Fatalf("unexpected route chain event order: message=%d decision=%d start=%d response=%d", messageIdx, decisionIdx, startIdx, responseIdx)
	}
	if !strings.Contains(rec.events[decisionIdx].Content, "evidence=rule_dictionary:matched:CHAT") {
		t.Fatalf("routing.decision event should include structured evidence: %#v", rec.events[decisionIdx])
	}
}

func TestMessageOrchestratorOptInTimingIsInlineOnTerminalResponse(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1, "timing contract"),
		response: "timing response",
	}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)
	orch.SetSuperAgentRuntimeRecorder(&mockSuperAgentRuntimeRecorder{})
	rec := &recordingEventListener{}
	orch.SetEventListener(rec)

	ctx := turntiming.WithRecorder(context.Background(), turntiming.New(time.Now()))
	if _, err := orch.ProcessMessage(ctx, defaultReq()); err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	timingEvents := 0
	for _, event := range rec.events {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(encoded, &payload); err != nil {
			t.Fatal(err)
		}
		timing, hasTiming := payload["timing"]
		if event.Type == "agent.response" && event.To == "user" {
			if !hasTiming {
				t.Fatalf("terminal user-facing response has no timing snapshot: %#v", payload)
			}
			timingEvents++
			snapshot, ok := timing.(map[string]any)
			if !ok {
				t.Fatalf("timing has type %T, want object", timing)
			}
			points, ok := snapshot["points_ms"].(map[string]any)
			if !ok {
				t.Fatalf("timing.points_ms has type %T, want object", snapshot["points_ms"])
			}
			orderedPoints := []string{
				"process_started",
				"process_session_started",
				"process_session_ended",
				"root_task_preparation_started",
				"root_task_created",
				"root_task_preparation_ended",
				"initial_events_started",
				"message_received_event_published",
				"server_received_metric_published",
				"session_turn_log_attempt_ended",
				"initial_events_ended",
				"persona_observation_started",
				"persona_observation_ended",
				"pre_route_checks_started",
				"turn_input_built",
				"pre_route_checks_ended",
				"route_decision_started",
				"route_decided",
				"task_activation_started",
				"task_routing_event_published",
				"task_execution_prepared",
				"task_assignment_event_published",
				"task_assignment_recorded",
				"task_root_run_started",
				"task_identity_bound",
				"task_activation_ended",
				"lead_run_resolution_started",
				"lead_run_resolution_ended",
				"route_metric_published",
				"skill_bootstrap_started",
				"skill_bootstrap_ended",
				"lead_run_persistence_started",
				"lead_agent_run_saved",
				"lead_agent_start_event_appended",
				"lead_context_pack_saved",
				"lead_run_persistence_ended",
				"lead_run_context_ready",
				"dispatch_metric_published",
				"dispatch_started",
				"agent_start_publication_started",
				"agent_start_publication_ended",
				"response_publication_started",
			}
			var previousOffset float64
			for index, point := range orderedPoints {
				offset, ok := points[point].(float64)
				if !ok {
					t.Errorf("timing point %q missing from terminal response: %#v", point, points)
					continue
				}
				if index > 0 && offset < previousOffset {
					t.Errorf("timing point %q offset=%v precedes prior point at %v: %#v", point, offset, previousOffset, points)
				}
				previousOffset = offset
			}
			if _, ok := points["task_execution_run_started"]; ok {
				t.Errorf("root-only route recorded child execution start: %#v", points)
			}
		} else if hasTiming {
			t.Errorf("timing snapshot leaked onto non-terminal event %s->%s: %#v", event.From, event.To, payload)
		}
	}
	if timingEvents != 1 {
		t.Fatalf("terminal events with timing=%d, want one; events=%#v", timingEvents, rec.events)
	}
}

func TestMessageOrchestrator_RouteChainContract_ViewerRecipientBecomesChatSpeaker(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecisionWithEvidence(routing.RouteCHAT, 0.91, "chat", routing.DecisionEvidence{
			Source:     routing.EvidenceSourceRuleDictionary,
			Matched:    true,
			Route:      routing.RouteCHAT,
			Confidence: 0.91,
			Reason:     "test rule",
		}),
		chatFunc: func(context.Context, conversation.TurnInput) (string, error) {
			return "", errors.New("Kuro CHAT must not use Mio's chat provider")
		},
	}
	heavy := &mockHeavyAgent{response: "RC_kuro_contract、分析完了です。"}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)
	orch.SetHeavyAgent(heavy)
	rec := &recordingEventListener{}
	orch.SetEventListener(rec)
	req := defaultReq()
	req.To = "kuro"
	req.UserMessage = "合言葉 RC_kuro_contract で返答して"

	resp, err := orch.ProcessMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.Response != "RC_kuro_contract、分析完了です。" {
		t.Fatalf("response = %q", resp.Response)
	}
	if !heavy.called {
		t.Fatal("Kuro CHAT must use the Heavy agent")
	}

	messageIdx := indexOfEvent(rec.events, "message.received", "user", "kuro", "")
	startIdx := indexOfEvent(rec.events, "agent.start", "kuro", "user", "CHAT")
	responseIdx := indexOfEvent(rec.events, "agent.response", "kuro", "user", "CHAT")
	if messageIdx < 0 || startIdx < 0 || responseIdx < 0 {
		t.Fatalf("missing recipient speaker events: %#v", rec.events)
	}
	if indexOfEvent(rec.events, "agent.response", "mio", "user", "CHAT") >= 0 {
		t.Fatalf("recipient response must not be emitted as mio->user: %#v", rec.events)
	}
	if rec.events[messageIdx].TaskID.IsZero() {
		t.Fatalf("message.received must carry task_id for sequence tracking: %#v", rec.events[messageIdx])
	}
}

func TestMessageOrchestrator_RouteChainContract_KuroUnavailableDoesNotFallbackToMio(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecisionWithEvidence(routing.RouteCHAT, 0.91, "chat", routing.DecisionEvidence{
			Source:     routing.EvidenceSourceRuleDictionary,
			Matched:    true,
			Route:      routing.RouteCHAT,
			Confidence: 0.91,
			Reason:     "test rule",
		}),
		chatFunc: func(context.Context, conversation.TurnInput) (string, error) {
			return "mio fallback", nil
		},
	}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)
	req := defaultReq()
	req.To = "kuro"

	resp, err := orch.ProcessMessage(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "no heavy agent available for Kuro") {
		t.Fatalf("expected Kuro unavailable error, got response=%+v err=%v", resp, err)
	}
	if resp.Response == "mio fallback" {
		t.Fatal("Kuro failure must not silently fall back to Mio")
	}
}

func TestMessageOrchestrator_RouteChainContract_ShiroUsesDedicatedChatWorker(t *testing.T) {
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteCHAT, 1, "test"), chatFunc: func(context.Context, conversation.TurnInput) (string, error) {
		return "", errors.New("Shiro CHAT must not use Mio")
	}}
	shiroChat := &mockMioAgent{response: "shiro chatworker response"}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)
	orch.SetShiroChatAgent(shiroChat)
	req := defaultReq()
	req.To = "shiro"

	resp, err := orch.ProcessMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessMessage() error=%v", err)
	}
	if resp.Response != "shiro chatworker response" {
		t.Fatalf("response=%q", resp.Response)
	}
}

func TestMessageOrchestrator_RouteChainContract_MidoriUsesWildAgent(t *testing.T) {
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteCHAT, 1, "test"), chatFunc: func(context.Context, conversation.TurnInput) (string, error) {
		return "", errors.New("Midori CHAT must not use Mio")
	}}
	wild := &mockWildAgent{response: "midori wild response"}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)
	orch.SetWildAgent(wild)
	req := defaultReq()
	req.To = "midori"

	resp, err := orch.ProcessMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessMessage() error=%v", err)
	}
	if resp.Response != "midori wild response" {
		t.Fatalf("response=%q", resp.Response)
	}
}

func TestMessageOrchestrator_RouteChainContract_EmitsLatencyMetrics(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecisionWithEvidence(routing.RouteCHAT, 0.91, "chat", routing.DecisionEvidence{
			Source:     routing.EvidenceSourceRuleDictionary,
			Matched:    true,
			Route:      routing.RouteCHAT,
			Confidence: 0.91,
			Reason:     "test rule",
		}),
		response: "chat response",
	}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)
	rec := &recordingEventListener{}
	orch.SetEventListener(rec)

	_, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if !hasLatencyMetric(rec.events, "network", "server_received") {
		t.Fatalf("missing server receive latency metric: %#v", rec.events)
	}
	if !hasLatencyMetric(rec.events, "llm", "route_decision") {
		t.Fatalf("missing route decision latency metric: %#v", rec.events)
	}
	if !hasLatencyMetric(rec.events, "llm", "response_complete") {
		t.Fatalf("missing response complete latency metric: %#v", rec.events)
	}
}

func TestMessageOrchestrator_RouteChainContract_ChatCommandBypassesRouteDecision(t *testing.T) {
	decideCalled := false
	mio := &mockMioAgent{
		decideFunc: func(ctx context.Context, t conversation.TurnInput) (routing.Decision, error) {
			decideCalled = true
			return routing.NewDecision(routing.RouteOPS, 0.9, "should not run"), nil
		},
		cmdFunc: func(ctx context.Context, sessionID, message string) (agent.ChatCommandResult, error) {
			return agent.ChatCommandResult{Handled: true, Response: "command response"}, nil
		},
	}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	rec := &recordingEventListener{}
	orch.SetEventListener(rec)

	req := defaultReq()
	req.RootTaskID = modulecore.NewTaskID().String()
	resp, err := orch.ProcessMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if decideCalled {
		t.Fatal("chat command should be handled before route decision")
	}
	if resp.Route != routing.RouteCHAT {
		t.Fatalf("chat command route = %s, want CHAT", resp.Route)
	}
	if resp.TaskID != req.RootTaskID {
		t.Fatalf("chat command task ID = %q, want %q", resp.TaskID, req.RootTaskID)
	}
	if indexOfEvent(rec.events, "routing.decision", "mio", "", "OPS") >= 0 {
		t.Fatalf("routing.decision should not be emitted for handled chat command: %#v", rec.events)
	}
	responseIndex := indexOfEvent(rec.events, "agent.response", "mio", "user", "CHAT")
	if responseIndex < 0 {
		t.Fatalf("chat command response event missing: %#v", rec.events)
	}
	if rec.events[responseIndex].TaskID.String() != req.RootTaskID {
		t.Fatalf("chat command response event task ID = %q, want %q", rec.events[responseIndex].TaskID, req.RootTaskID)
	}
}

func hasLatencyMetric(events []OrchestratorEvent, kind, point string) bool {
	for _, ev := range events {
		if ev.Type != "metrics.latency" {
			continue
		}
		var payload struct {
			Kind  string `json:"kind"`
			Point string `json:"point"`
		}
		if err := json.Unmarshal([]byte(ev.Content), &payload); err != nil {
			continue
		}
		if payload.Kind == kind && payload.Point == point {
			return true
		}
	}
	return false
}

func TestMessageOrchestrator_RouteChainContract_InvalidProposalDoesNotReachWorker(t *testing.T) {
	worker := &recordingWorkerExecutionService{}
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCODE3, 1.0, "code3"),
	}
	coder3 := &mockCoderAgentWithProposal{
		proposal: proposal.NewProposal("", "", "", ""),
	}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, coder3, nil, worker)

	_, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err == nil {
		t.Fatal("invalid proposal should return error")
	}
	if worker.calls != 0 {
		t.Fatalf("invalid proposal reached WorkerExecutionService: calls=%d", worker.calls)
	}
}

func TestMessageOrchestrator_RouteChainContract_UnknownRouteDoesNotEmitSuccessResponse(t *testing.T) {
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.Route("UNKNOWN"), 0.5, "unknown"),
	}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	rec := &recordingEventListener{}
	orch.SetEventListener(rec)

	_, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err == nil {
		t.Fatal("unknown route should return error")
	}
	if !strings.Contains(err.Error(), "unsupported orchestrator route") {
		t.Fatalf("unknown route error missing route detail: %v", err)
	}
	for _, ev := range rec.events {
		if ev.Type == "agent.response" {
			t.Fatalf("unknown route should not emit success response event: %#v", rec.events)
		}
	}
}

func TestMessageOrchestrator_RouteChainContract_AnalyzeEmitsPublicTerminalResponse(t *testing.T) {
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteANALYZE, 1, "analyze")}
	heavy := &mockHeavyAgent{response: "PORTAL_BROWSER_OK"}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)
	orch.SetHeavyAgent(heavy)
	rec := &recordingEventListener{}
	orch.SetEventListener(rec)

	resp, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.Response != "PORTAL_BROWSER_OK" {
		t.Fatalf("response=%q", resp.Response)
	}
	internal := indexOfEvent(rec.events, "agent.response", "kuro", "mio", "ANALYZE")
	public := indexOfEvent(rec.events, "agent.response", "mio", "user", "ANALYZE")
	if internal < 0 || public <= internal {
		t.Fatalf("ANALYZE must emit internal then public terminal response: %#v", rec.events)
	}
}

func TestMessageOrchestrator_RouteChainContract_OPSEmitsPublicTerminalResponse(t *testing.T) {
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteOPS, 1, "ops")}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{response: "PORTAL_OPS_OK"}, nil, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)
	rec := &recordingEventListener{}
	orch.SetEventListener(rec)

	resp, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.Response != "PORTAL_OPS_OK" {
		t.Fatalf("response=%q", resp.Response)
	}
	internal := indexOfEvent(rec.events, "agent.response", "shiro", "mio", "OPS")
	public := indexOfEvent(rec.events, "agent.response", "mio", "user", "OPS")
	if internal < 0 || public <= internal {
		t.Fatalf("OPS must emit internal then public terminal response: %#v", rec.events)
	}
}

func TestMessageOrchestrator_RouteChainContract_WorkerErrorDoesNotBecomeSuccess(t *testing.T) {
	workerErr := errors.New("worker failed")
	worker := &recordingWorkerExecutionService{err: workerErr}
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCODE3, 1.0, "code3"),
	}
	coder3 := &mockCoderAgentWithProposal{
		proposal: proposal.NewProposal(
			"Plan",
			`[{"type":"shell","command":"echo ok"}]`,
			"Low risk",
			"Low cost",
		),
	}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, coder3, nil, worker)
	attachCanonicalTestTaskOwner(t, orch)
	orch.SetMaxRepair(0)
	rec := &recordingEventListener{}
	orch.SetEventListener(rec)

	_, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err == nil {
		t.Fatal("worker execution error should return ProcessMessage error")
	}
	if !strings.Contains(err.Error(), "worker execution failed") {
		t.Fatalf("worker execution error should keep worker context: %v", err)
	}
	if worker.calls == 0 {
		t.Fatal("worker ExecuteProposal should be reached for a valid proposal before returning the worker error")
	}
	if indexOfEvent(rec.events, "agent.response", "mio", "user", "CODE3") >= 0 {
		t.Fatalf("worker error should not be converted into a user-facing success event: %#v", rec.events)
	}
}

func TestMessageOrchestrator_RouteChainContract_GenerateErrorDoesNotBecomeFallbackSuccess(t *testing.T) {
	generateErr := errors.New("generate failed")
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCODE1, 1.0, "code1"),
	}
	coder1 := &failingCoderAgent{err: generateErr}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, coder1, nil, nil, nil, nil)
	attachCanonicalTestTaskOwner(t, orch)
	orch.SetMaxRepair(0)
	rec := &recordingEventListener{}
	orch.SetEventListener(rec)

	resp, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err == nil {
		t.Fatal("Generate error should return ProcessMessage error")
	}
	if !strings.Contains(err.Error(), "generate failed") {
		t.Fatalf("Generate error should not be hidden behind fallback success: %v", err)
	}
	if resp.Response != "" {
		t.Fatalf("Generate error should not produce fallback response text: %q", resp.Response)
	}
	if indexOfEvent(rec.events, "agent.response", "shiro", "mio", "CODE1") >= 0 {
		t.Fatalf("Generate error should not emit shiro->mio success response: %#v", rec.events)
	}
}

type recordingWorkerExecutionService struct {
	calls  int
	err    error
	result *patch.PatchExecutionResult
}

func (w *recordingWorkerExecutionService) ExecuteProposal(ctx context.Context, taskID modulecore.TaskID, p *proposal.Proposal) (*patch.PatchExecutionResult, error) {
	w.calls++
	if w.err != nil {
		return nil, w.err
	}
	if w.result != nil {
		return w.result, nil
	}
	return patch.NewPatchExecutionResult(), nil
}

func (w *recordingWorkerExecutionService) ExecuteObservation(_ context.Context, _ []service.ObservationAction) ([]service.ObservationActionResult, error) {
	return nil, nil
}
