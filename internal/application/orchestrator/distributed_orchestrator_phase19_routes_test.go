package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
)

func TestPhase19DistributedRouteDispatcherCHATBypassesAutonomousExecutor(t *testing.T) {
	mio := &distMockMioAgent{chatResponse: "chat ok"}
	var autonomousCalled bool
	dispatcher := newDistributedRouteDispatcher(
		mio,
		session.NewCentralMemory(),
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func(from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func(ctx context.Context, route routing.Route, jid, sessionID, channel, chatID, ttsSessionID string) (context.Context, *streamBundle) {
			return ctx, &streamBundle{}
		},
		func(ctx context.Context, sessionID string, route routing.Route, eventType, text string) {},
		func(ctx context.Context, gotTask task.Task, route routing.Route, sessionID, jid string) (string, error) {
			t.Fatal("code executor should not be called for CHAT")
			return "", nil
		},
		func(route routing.Route) string { return "" },
		func(t task.Task, targetAgent, sessionID string) task.Task { return t },
		func(ctx context.Context, targetAgent string, msg domaintransport.Message) (domaintransport.Message, error) {
			t.Fatal("transport executor should not be called for local CHAT")
			return domaintransport.Message{}, nil
		},
	)
	dispatcher.SetAutonomousExecutor(func(ctx context.Context, t task.Task, route routing.Route, sessionID, ttsSessionID string) (string, error) {
		autonomousCalled = true
		return "", nil
	})

	resp, err := dispatcher.ExecuteTask(context.Background(), task.NewTask(task.NewJobID(), "hello", "line", "U123"), routing.RouteCHAT, "sess-1", "")
	if err != nil {
		t.Fatalf("ExecuteTask failed: %v", err)
	}
	if resp != "chat ok" {
		t.Fatalf("expected chat response, got %q", resp)
	}
	if autonomousCalled {
		t.Fatal("CHAT should bypass autonomous executor")
	}
}

func TestPhase19DistributedRouteDispatcherNonCHATUsesAutonomousExecutor(t *testing.T) {
	var autonomousCalled bool
	dispatcher := newDistributedRouteDispatcher(
		&distMockMioAgent{},
		session.NewCentralMemory(),
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func(from, to, content, route, jobID, sessionID, channel, chatID string) {},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	dispatcher.SetAutonomousExecutor(func(ctx context.Context, gotTask task.Task, route routing.Route, sessionID, ttsSessionID string) (string, error) {
		autonomousCalled = true
		if route != routing.RouteOPS {
			t.Fatalf("expected OPS route, got %s", route)
		}
		if sessionID != "sess-1" || ttsSessionID != "tts-1" {
			t.Fatalf("unexpected context: session=%s tts=%s", sessionID, ttsSessionID)
		}
		return "ops ok", nil
	})

	resp, err := dispatcher.ExecuteTask(context.Background(), task.NewTask(task.NewJobID(), "run", "line", "U123"), routing.RouteOPS, "sess-1", "tts-1")
	if err != nil {
		t.Fatalf("ExecuteTask failed: %v", err)
	}
	if resp != "ops ok" || !autonomousCalled {
		t.Fatalf("expected autonomous response, resp=%q called=%t", resp, autonomousCalled)
	}
}

func TestPhase19DistributedRemoteRouteVerbalizesHandoffReadbackAndReport(t *testing.T) {
	var events []OrchestratorEvent
	dispatcher := newDistributedRouteDispatcher(
		&distMockMioAgent{},
		session.NewCentralMemory(),
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {
			events = append(events, NewEvent(eventType, from, to, content, route, jobID, sessionID, channel, chatID))
		},
		func(from, to, content, route, jobID, sessionID, channel, chatID string) {},
		nil,
		func(ctx context.Context, sessionID string, route routing.Route, eventType, text string) {},
		nil,
		func(route routing.Route) string { return "shiro" },
		func(t task.Task, targetAgent, sessionID string) task.Task { return t },
		func(ctx context.Context, targetAgent string, msg domaintransport.Message) (domaintransport.Message, error) {
			return domaintransport.Message{From: targetAgent, To: "mio", Content: "確認完了", Type: domaintransport.MessageTypeResult}, nil
		},
	)

	tk := task.NewTask(task.NewJobID(), "TTSの接続を確認して", "viewer", "viewer-user")
	if _, err := dispatcher.ExecuteDirect(context.Background(), tk, routing.RouteOPS, "sess-1", "tts-1"); err != nil {
		t.Fatalf("ExecuteDirect failed: %v", err)
	}
	delegate := orchestratorEventIndex(events, "agent.delegate", "mio", "shiro")
	readback := orchestratorEventIndex(events, "agent.acknowledge", "shiro", "mio")
	report := orchestratorEventIndex(events, "agent.report", "shiro", "mio")
	if delegate < 0 || readback < 0 || report < 0 || !(delegate < readback && readback < report) {
		t.Fatalf("missing or unordered handoff speech: %#v", events)
	}
	if !strings.HasPrefix(events[delegate].Content, "Shiro、") || !strings.HasPrefix(events[readback].Content, "Mio、") || !strings.HasPrefix(events[report].Content, "Mio、") {
		t.Fatalf("handoff speech must begin with named recipient/delegator: %#v", events)
	}
}

func orchestratorEventIndex(events []OrchestratorEvent, eventType, from, to string) int {
	for i, ev := range events {
		if ev.Type == eventType && ev.From == from && ev.To == to {
			return i
		}
	}
	return -1
}
