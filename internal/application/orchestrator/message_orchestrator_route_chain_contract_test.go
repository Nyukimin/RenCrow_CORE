package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
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
		chatFunc: func(ctx context.Context, t task.Task) (string, error) {
			if t.ViewerRecipient() != "kuro" {
				return "", errors.New("missing viewer recipient")
			}
			if t.UserMessage() != "合言葉 RC_kuro_contract で返答して" {
				return "", errors.New("user message was changed")
			}
			return "RC_kuro_contract、分析完了です。", nil
		},
	}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
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

	messageIdx := indexOfEvent(rec.events, "message.received", "user", "kuro", "")
	startIdx := indexOfEvent(rec.events, "agent.start", "kuro", "user", "CHAT")
	responseIdx := indexOfEvent(rec.events, "agent.response", "kuro", "user", "CHAT")
	if messageIdx < 0 || startIdx < 0 || responseIdx < 0 {
		t.Fatalf("missing recipient speaker events: %#v", rec.events)
	}
	if indexOfEvent(rec.events, "agent.response", "mio", "user", "CHAT") >= 0 {
		t.Fatalf("recipient response must not be emitted as mio->user: %#v", rec.events)
	}
	if rec.events[messageIdx].JobID == "" {
		t.Fatalf("message.received must carry job_id for sequence tracking: %#v", rec.events[messageIdx])
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
		decideFunc: func(ctx context.Context, t task.Task) (routing.Decision, error) {
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

	resp, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if decideCalled {
		t.Fatal("chat command should be handled before route decision")
	}
	if resp.Route != routing.RouteCHAT {
		t.Fatalf("chat command route = %s, want CHAT", resp.Route)
	}
	if indexOfEvent(rec.events, "routing.decision", "mio", "", "OPS") >= 0 {
		t.Fatalf("routing.decision should not be emitted for handled chat command: %#v", rec.events)
	}
	if indexOfEvent(rec.events, "agent.response", "mio", "user", "CHAT") < 0 {
		t.Fatalf("chat command response event missing: %#v", rec.events)
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
	if !strings.Contains(err.Error(), "unknown route") {
		t.Fatalf("unknown route error missing route detail: %v", err)
	}
	for _, ev := range rec.events {
		if ev.Type == "agent.response" {
			t.Fatalf("unknown route should not emit success response event: %#v", rec.events)
		}
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

func (w *recordingWorkerExecutionService) ExecuteProposal(ctx context.Context, jobID task.JobID, p *proposal.Proposal) (*patch.PatchExecutionResult, error) {
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
