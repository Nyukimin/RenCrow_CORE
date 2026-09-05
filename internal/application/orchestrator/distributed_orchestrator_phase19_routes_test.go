package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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
		func(ctx context.Context, gotTask conversation.TurnInput, route routing.Route, jobID modulecore.TaskID) (string, error) {
			t.Fatal("code executor should not be called for CHAT")
			return "", nil
		},
		func(route routing.Route) string { return "" },
		func(t conversation.TurnInput, targetAgent string) conversation.TurnInput { return t },
		func(ctx context.Context, targetAgent string, msg domaintransport.Message) (domaintransport.Message, error) {
			t.Fatal("transport executor should not be called for local CHAT")
			return domaintransport.Message{}, nil
		},
	)
	dispatcher.SetAutonomousExecutor(func(ctx context.Context, t conversation.TurnInput, route routing.Route, jobID modulecore.TaskID, ttsSessionID string) (string, error) {
		autonomousCalled = true
		return "", nil
	})

	jobID := modulecore.NewTaskID()
	input := newOrchestratorTestTurnInput(t, "hello", "line", "U123").WithSessionID("sess-1")
	resp, err := dispatcher.ExecuteTurnInput(context.Background(), input, routing.RouteCHAT, jobID, "")
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
	dispatcher.SetAutonomousExecutor(func(ctx context.Context, gotTask conversation.TurnInput, route routing.Route, jobID modulecore.TaskID, ttsSessionID string) (string, error) {
		autonomousCalled = true
		if route != routing.RouteOPS {
			t.Fatalf("expected OPS route, got %s", route)
		}
		if gotTask.SessionID() != "sess-1" || ttsSessionID != "tts-1" {
			t.Fatalf("unexpected context: session=%s tts=%s", gotTask.SessionID(), ttsSessionID)
		}
		return "ops ok", nil
	})

	jobID := modulecore.NewTaskID()
	input := newOrchestratorTestTurnInput(t, "run", "line", "U123").WithSessionID("sess-1")
	resp, err := dispatcher.ExecuteTurnInput(context.Background(), input, routing.RouteOPS, jobID, "tts-1")
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
		func(t conversation.TurnInput, targetAgent string) conversation.TurnInput { return t },
		func(ctx context.Context, targetAgent string, msg domaintransport.Message) (domaintransport.Message, error) {
			return domaintransport.Message{From: targetAgent, To: "mio", Content: "確認完了", Type: domaintransport.MessageTypeResult}, nil
		},
	)

	jobID := modulecore.NewTaskID()
	tk := newOrchestratorTestTurnInput(t, "TTSの接続を確認して", "viewer", "viewer-user").WithSessionID("sess-1")
	if _, err := dispatcher.ExecuteDirect(context.Background(), tk, routing.RouteOPS, jobID, "tts-1"); err != nil {
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

func TestPhase19DistributedRemoteRouteCarriesExactTurnInputProjection(t *testing.T) {
	var sent domaintransport.Message
	dispatcher := newDistributedRouteDispatcher(
		&distMockMioAgent{},
		session.NewCentralMemory(),
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func(from, to, content, route, jobID, sessionID, channel, chatID string) {},
		nil,
		func(ctx context.Context, sessionID string, route routing.Route, eventType, text string) {},
		nil,
		func(route routing.Route) string { return "shiro" },
		func(input conversation.TurnInput, targetAgent string) conversation.TurnInput { return input },
		func(ctx context.Context, targetAgent string, msg domaintransport.Message) (domaintransport.Message, error) {
			sent = msg
			return domaintransport.Message{From: targetAgent, To: "mio", Content: "remote response", Type: domaintransport.MessageTypeResult}, nil
		},
	)

	jobID := modulecore.NewTaskID()
	input := newOrchestratorTestTurnInput(t, "remote request", "line", "U123").
		WithSessionID("sess-1").
		WithAttachments([]attachment.Attachment{{ID: "att-1"}}).
		WithViewerRecipient("mio").
		WithForcedRoute(routing.RouteOPS)
	if _, err := dispatcher.ExecuteDirect(context.Background(), input, routing.RouteOPS, jobID, ""); err != nil {
		t.Fatalf("ExecuteDirect() error = %v", err)
	}
	if sent.JobID != jobID.String() {
		t.Fatalf("sent JobID=%q, want %q", sent.JobID, jobID)
	}
	got, err := sent.ReconstructTurnInput()
	if err != nil {
		t.Fatalf("sent ReconstructTurnInput() error = %v", err)
	}
	if got.RootTaskID() != input.RootTaskID() || got.TurnID() != input.TurnID() || got.TraceID() != input.TraceID() || got.UserMessageID() != input.UserMessageID() || got.AgentMessageID() != input.AgentMessageID() {
		t.Fatalf("sent canonical identities changed: got=%#v want=%#v", got, input)
	}
	if got.SessionID() != input.SessionID() || got.MessageText() != input.MessageText() || got.ChannelAddress() != input.ChannelAddress() {
		t.Fatalf("sent input metadata changed: got=%#v want=%#v", got, input)
	}
	if got.ViewerRecipient() != input.ViewerRecipient() || got.ForcedRoute() != input.ForcedRoute() || got.Route() != routing.RouteOPS || len(got.Attachments()) != 1 || got.Attachments()[0].ID != "att-1" {
		t.Fatalf("sent projection changed: recipient=%q forced=%q route=%q attachments=%#v", got.ViewerRecipient(), got.ForcedRoute(), got.Route(), got.Attachments())
	}
}

func TestPhase19DistributedLocalRouteStoresExactTurnInputProjection(t *testing.T) {
	memory := session.NewCentralMemory()
	dispatcher := newDistributedRouteDispatcher(
		&distMockMioAgent{chatResponse: "local response"},
		memory,
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func(from, to, content, route, jobID, sessionID, channel, chatID string) {},
		func(ctx context.Context, route routing.Route, jid, sessionID, channel, chatID, ttsSessionID string) (context.Context, *streamBundle) {
			return ctx, &streamBundle{}
		},
		func(ctx context.Context, sessionID string, route routing.Route, eventType, text string) {},
		nil,
		func(route routing.Route) string { return "" },
		func(input conversation.TurnInput, targetAgent string) conversation.TurnInput { return input },
		func(ctx context.Context, targetAgent string, msg domaintransport.Message) (domaintransport.Message, error) {
			t.Fatal("local route should not call remote transport")
			return domaintransport.Message{}, nil
		},
	)

	jobID := modulecore.NewTaskID()
	input := newOrchestratorTestTurnInput(t, "local request", "line", "U123").WithSessionID("sess-1")
	if _, err := dispatcher.ExecuteDirect(context.Background(), input, routing.RouteCHAT, jobID, ""); err != nil {
		t.Fatalf("ExecuteDirect() error = %v", err)
	}
	var userMessage domaintransport.Message
	for _, entry := range memory.GetUnifiedView(20) {
		if entry.Message.From == "user" && entry.Message.To == "mio" {
			userMessage = entry.Message
			break
		}
	}
	if userMessage.TurnInput == nil {
		t.Fatalf("local user message has no turn input projection: %#v", userMessage)
	}
	got, err := userMessage.ReconstructTurnInput()
	if err != nil {
		t.Fatalf("local user message ReconstructTurnInput() error = %v", err)
	}
	if got.RootTaskID() != input.RootTaskID() || got.TurnID() != input.TurnID() || got.TraceID() != input.TraceID() || got.UserMessageID() != input.UserMessageID() || got.AgentMessageID() != input.AgentMessageID() || got.SessionID() != input.SessionID() || got.MessageText() != input.MessageText() {
		t.Fatalf("local user message changed input identity: got=%#v want=%#v", got, input)
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
