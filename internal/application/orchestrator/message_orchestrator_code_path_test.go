package orchestrator

import (
	"context"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/patch"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/proposal"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
)

type recordingEventListener struct {
	events []OrchestratorEvent
}

func (r *recordingEventListener) OnEvent(ev OrchestratorEvent) {
	r.events = append(r.events, ev)
}

func indexOfEvent(events []OrchestratorEvent, typ, from, to, route string) int {
	for i, ev := range events {
		if ev.Type == typ && ev.From == from && ev.To == to && ev.Route == route {
			return i
		}
	}
	return -1
}

func TestMessageOrchestrator_CodeRoute_AlwaysViaShiro_CODE1(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCODE1, 1.0, "explicit code1"),
	}
	shiro := &mockShiroAgent{response: "unused"}
	coder1 := &mockCoderAgent{response: "spec ready\n```\npatch applied\n```"}
	orch := NewMessageOrchestrator(repo, mio, shiro, coder1, nil, nil, nil, nil)
	rec := &recordingEventListener{}
	orch.SetEventListener(rec)

	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "s1",
		Channel:     "line",
		ChatID:      "u1",
		UserMessage: "design this",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	i1 := indexOfEvent(rec.events, "agent.start", "mio", "shiro", "CODE1")
	i2 := indexOfEvent(rec.events, "agent.start", "shiro", "coder1", "CODE1")
	i3 := indexOfEvent(rec.events, "agent.response", "coder1", "shiro", "CODE1")
	i4 := indexOfEvent(rec.events, "agent.response", "shiro", "mio", "CODE1")

	if i1 < 0 || i2 < 0 || i3 < 0 || i4 < 0 {
		t.Fatalf("missing expected shiro relay events for CODE1: %#v", rec.events)
	}
	if !(i1 < i2 && i2 < i3 && i3 < i4) {
		t.Fatalf("unexpected CODE1 event order: i1=%d i2=%d i3=%d i4=%d", i1, i2, i3, i4)
	}
}

func TestMessageOrchestrator_CodeRoute_AlwaysViaShiro_CODE2(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCODE2, 1.0, "explicit code2"),
	}
	shiro := &mockShiroAgent{response: "unused"}
	coder2 := &mockCoderAgent{response: "impl ready\n```\npatch applied\n```"}
	orch := NewMessageOrchestrator(repo, mio, shiro, nil, coder2, nil, nil, nil)
	rec := &recordingEventListener{}
	orch.SetEventListener(rec)

	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "s2",
		Channel:     "line",
		ChatID:      "u2",
		UserMessage: "implement this",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	i1 := indexOfEvent(rec.events, "agent.start", "mio", "shiro", "CODE2")
	i2 := indexOfEvent(rec.events, "agent.start", "shiro", "coder2", "CODE2")
	i3 := indexOfEvent(rec.events, "agent.response", "coder2", "shiro", "CODE2")
	i4 := indexOfEvent(rec.events, "agent.response", "shiro", "mio", "CODE2")

	if i1 < 0 || i2 < 0 || i3 < 0 || i4 < 0 {
		t.Fatalf("missing expected shiro relay events for CODE2: %#v", rec.events)
	}
	if !(i1 < i2 && i2 < i3 && i3 < i4) {
		t.Fatalf("unexpected CODE2 event order: i1=%d i2=%d i3=%d i4=%d", i1, i2, i3, i4)
	}
}

func TestMessageOrchestrator_CodeRoute_AlwaysViaShiro_CODE4(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCODE4, 1.0, "explicit code4"),
	}
	shiro := &mockShiroAgent{response: "unused"}
	coder4 := &mockCoderAgent{response: "prototype ready\n```\npatch applied\n```"}
	orch := NewMessageOrchestrator(repo, mio, shiro, nil, nil, nil, coder4, nil)
	rec := &recordingEventListener{}
	orch.SetEventListener(rec)

	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "s4",
		Channel:     "line",
		ChatID:      "u4",
		UserMessage: "prototype this",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	i1 := indexOfEvent(rec.events, "agent.start", "mio", "shiro", "CODE4")
	i2 := indexOfEvent(rec.events, "agent.start", "shiro", "coder4", "CODE4")
	i3 := indexOfEvent(rec.events, "agent.response", "coder4", "shiro", "CODE4")
	i4 := indexOfEvent(rec.events, "agent.response", "shiro", "mio", "CODE4")

	if i1 < 0 || i2 < 0 || i3 < 0 || i4 < 0 {
		t.Fatalf("missing expected shiro relay events for CODE4: %#v", rec.events)
	}
	if !(i1 < i2 && i2 < i3 && i3 < i4) {
		t.Fatalf("unexpected CODE4 event order: i1=%d i2=%d i3=%d i4=%d", i1, i2, i3, i4)
	}
}

func TestMessageOrchestrator_CodeRoute_AlwaysViaShiro_GenericCODE(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCODE, 1.0, "generic code"),
	}
	shiro := &mockShiroAgent{response: "unused"}
	coder1 := &mockCoderAgent{response: "generic code response\n```go\nfunc generated() {}\n```"}
	orch := NewMessageOrchestrator(repo, mio, shiro, coder1, nil, nil, nil, nil)
	rec := &recordingEventListener{}
	orch.SetEventListener(rec)

	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "s-code",
		Channel:     "line",
		ChatID:      "u-code",
		UserMessage: "change this",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	i1 := indexOfEvent(rec.events, "agent.start", "mio", "shiro", "CODE")
	i2 := indexOfEvent(rec.events, "agent.start", "shiro", "coder1", "CODE")
	i3 := indexOfEvent(rec.events, "agent.response", "coder1", "shiro", "CODE")
	i4 := indexOfEvent(rec.events, "agent.response", "shiro", "mio", "CODE")

	if i1 < 0 || i2 < 0 || i3 < 0 || i4 < 0 {
		t.Fatalf("missing expected shiro relay events for generic CODE: %#v", rec.events)
	}
	if !(i1 < i2 && i2 < i3 && i3 < i4) {
		t.Fatalf("unexpected generic CODE event order: i1=%d i2=%d i3=%d i4=%d", i1, i2, i3, i4)
	}
}

func TestMessageOrchestrator_CodeRoute_CODE3ProposalKeepsShiroEventOrder(t *testing.T) {
	p := proposal.NewProposal(
		"Plan CODE3",
		`[{"type":"shell","command":"echo ok"}]`,
		"Low risk",
		"Low cost",
	)
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCODE3, 1.0, "explicit code3"),
	}
	shiro := &mockShiroAgent{response: "unused"}
	coder3 := &mockCoderAgentWithProposal{proposal: p}
	worker := &recordingWorkerExecutionService{
		result: patch.NewPatchExecutionResult().WithSummary("実行: 1 件, 成功: 1 件, 失敗: 0 件"),
	}
	orch := NewMessageOrchestrator(repo, mio, shiro, nil, nil, coder3, nil, worker)
	rec := &recordingEventListener{}
	orch.SetEventListener(rec)

	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "s-code3",
		Channel:     "line",
		ChatID:      "u-code3",
		UserMessage: "apply this",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	i1 := indexOfEvent(rec.events, "agent.start", "mio", "shiro", "CODE3")
	i2 := indexOfEvent(rec.events, "agent.start", "shiro", "coder3", "CODE3")
	i3 := indexOfEvent(rec.events, "agent.response", "coder3", "shiro", "CODE3")
	i4 := indexOfEvent(rec.events, "agent.start", "shiro", "mio", "CODE3")
	i5 := indexOfEvent(rec.events, "agent.response", "shiro", "mio", "CODE3")

	if i1 < 0 || i2 < 0 || i3 < 0 || i4 < 0 || i5 < 0 {
		t.Fatalf("missing expected CODE3 proposal events: %#v", rec.events)
	}
	if !(i1 < i2 && i2 < i3 && i3 < i4 && i4 < i5) {
		t.Fatalf("unexpected CODE3 proposal event order: i1=%d i2=%d i3=%d i4=%d i5=%d", i1, i2, i3, i4, i5)
	}
}
