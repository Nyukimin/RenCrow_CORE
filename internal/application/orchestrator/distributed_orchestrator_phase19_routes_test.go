package orchestrator

import (
	"context"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/session"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
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
