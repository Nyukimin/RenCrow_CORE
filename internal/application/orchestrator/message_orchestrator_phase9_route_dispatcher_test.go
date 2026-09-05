package orchestrator

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newPhase9RouteDispatcher(mio MioAgent, shiro ShiroAgent) *messageRouteDispatcher {
	return newMessageRouteDispatcher(
		mio,
		shiro,
		nil,
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func(ctx context.Context, route routing.Route, jid, sessionID, channel, chatID, ttsSessionID string) (context.Context, *streamBundle) {
			return ctx, &streamBundle{}
		},
		func(ctx context.Context, sessionID string, route routing.Route, eventType, text string) {},
	)
}

func TestPhase9RouteDispatcher_CHATBypassesAutonomousExecutor(t *testing.T) {
	mio := &mockMioAgent{response: "chat response"}
	dispatcher := newPhase9RouteDispatcher(mio, &mockShiroAgent{})
	dispatcher.SetAutonomousExecutor(func(ctx context.Context, gotTask conversation.TurnInput, route routing.Route, jobID modulecore.TaskID, ttsSessionID string) (string, error) {
		t.Fatalf("CHAT route must not call autonomous executor")
		return "", nil
	})

	jobID := modulecore.NewTaskID()
	tk := newOrchestratorTestTurnInput(t, "こんにちは", "line", "U123").WithSessionID("sess-1")
	resp, err := dispatcher.ExecuteTurnInput(context.Background(), tk, routing.RouteCHAT, jobID, "")
	if err != nil {
		t.Fatalf("ExecuteTask failed: %v", err)
	}
	if resp != "chat response" {
		t.Fatalf("expected chat response, got %q", resp)
	}
}

func TestPhase9RouteDispatcher_NonCHATUsesAutonomousExecutor(t *testing.T) {
	dispatcher := newPhase9RouteDispatcher(&mockMioAgent{}, &mockShiroAgent{})
	var gotRoute routing.Route
	dispatcher.SetAutonomousExecutor(func(ctx context.Context, gotTask conversation.TurnInput, route routing.Route, jobID modulecore.TaskID, ttsSessionID string) (string, error) {
		gotRoute = route
		return "autonomous response", nil
	})

	jobID := modulecore.NewTaskID()
	tk := newOrchestratorTestTurnInput(t, "計画して", "line", "U123").WithSessionID("sess-1")
	resp, err := dispatcher.ExecuteTurnInput(context.Background(), tk, routing.RoutePLAN, jobID, "")
	if err != nil {
		t.Fatalf("ExecuteTask failed: %v", err)
	}
	if resp != "autonomous response" {
		t.Fatalf("expected autonomous response, got %q", resp)
	}
	if gotRoute != routing.RoutePLAN {
		t.Fatalf("expected autonomous route PLAN, got %s", gotRoute)
	}
}

func TestPhase9RouteDispatcher_OPSVerbalizesNamedHandoffAndReadback(t *testing.T) {
	var events []OrchestratorEvent
	dispatcher := newMessageRouteDispatcher(
		&mockMioAgent{},
		&mockShiroAgent{},
		nil,
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {
			events = append(events, NewEvent(eventType, from, to, content, route, jobID, sessionID, channel, chatID))
		},
		nil,
		nil,
	)
	dispatcher.SetAutonomousExecutor(func(ctx context.Context, gotTask conversation.TurnInput, route routing.Route, jobID modulecore.TaskID, ttsSessionID string) (string, error) {
		return "ops response", nil
	})

	jobID := modulecore.NewTaskID()
	tk := newOrchestratorTestTurnInput(t, "TTSの接続を確認して", "viewer", "viewer-user").WithSessionID("sess-1")
	if _, err := dispatcher.ExecuteTurnInput(context.Background(), tk, routing.RouteOPS, jobID, ""); err != nil {
		t.Fatalf("ExecuteTask failed: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("expected handoff and readback events, got %#v", events)
	}
	if events[0].Type != "agent.delegate" || events[0].From != "mio" || events[0].To != "shiro" || events[1].Type != "agent.acknowledge" || events[1].From != "shiro" || events[1].To != "mio" {
		t.Fatalf("unexpected handoff sequence: %#v", events)
	}
}

func TestPhase9RouteDispatcher_SetHeavyAgentUpdatesAnalyzeRoute(t *testing.T) {
	mio := &mockMioAgent{response: "mio analyze"}
	heavy := &mockHeavyAgent{response: "heavy analyze"}
	dispatcher := newPhase9RouteDispatcher(mio, &mockShiroAgent{})
	dispatcher.SetHeavyAgent(heavy)

	jobID := modulecore.NewTaskID()
	tk := newOrchestratorTestTurnInput(t, "分析して", "line", "U123").WithSessionID("sess-1")
	resp, err := dispatcher.ExecuteDirect(context.Background(), tk, routing.RouteANALYZE, jobID, "")
	if err != nil {
		t.Fatalf("ExecuteDirect failed: %v", err)
	}
	if resp != "heavy analyze" {
		t.Fatalf("expected heavy response, got %q", resp)
	}
	if !heavy.called {
		t.Fatal("expected heavy agent to be called")
	}
}
