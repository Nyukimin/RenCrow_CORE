package modulebridge

import (
	"context"
	"fmt"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
	"github.com/Nyukimin/picoclaw_multiLLM/modules/chat"
	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

type fakeChatProcessor struct {
	req orchestrator.ProcessMessageRequest
	err error
}

func (p *fakeChatProcessor) ProcessMessage(_ context.Context, req orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	p.req = req
	if p.err != nil {
		return orchestrator.ProcessMessageResponse{}, p.err
	}
	return orchestrator.ProcessMessageResponse{
		Response: "返答です",
		Route:    routing.RouteCHAT,
		JobID:    "job-1",
	}, nil
}

type fakeRouteDecisionProvider struct {
	route routing.Route
}

func (p fakeRouteDecisionProvider) DecideAction(_ context.Context, t task.Task) (routing.Decision, error) {
	return routing.NewDecision(p.route, 0.9, "decided for "+t.UserMessage()), nil
}

func TestChatServiceAdapterRespond(t *testing.T) {
	processor := &fakeChatProcessor{}
	adapter := NewChatServiceAdapter(processor)

	health := adapter.Health(context.Background())
	if health.Status != core.HealthReady || !health.Ready {
		t.Fatalf("unexpected health: %+v", health)
	}

	got, err := adapter.Respond(context.Background(), chat.Input{
		SessionID: "session-1",
		Channel:   "viewer",
		UserID:    "user-1",
		Text:      "こんにちは",
	}, chat.RuntimePorts{})
	if err != nil {
		t.Fatalf("Respond returned error: %v", err)
	}
	if processor.req.SessionID != "session-1" || processor.req.Channel != "viewer" || processor.req.ChatID != "user-1" || processor.req.UserMessage != "こんにちは" {
		t.Fatalf("request was not mapped: %+v", processor.req)
	}
	if got.Text != "返答です" || got.Response.Content != "返答です" || got.JobID != "job-1" {
		t.Fatalf("response was not mapped: %+v", got)
	}
	if got.Route.Route != chat.RouteChat || got.Route.Reason != string(routing.RouteCHAT) {
		t.Fatalf("route was not mapped: %+v", got.Route)
	}
}

func TestChatServiceAdapterRespondDefaultsViewerIdentity(t *testing.T) {
	processor := &fakeChatProcessor{}
	adapter := NewChatServiceAdapter(processor)

	_, err := adapter.Respond(context.Background(), chat.Input{SessionID: "session-1", Text: "hi"}, chat.RuntimePorts{})
	if err != nil {
		t.Fatalf("Respond returned error: %v", err)
	}
	if processor.req.Channel != "viewer" || processor.req.ChatID != "viewer-user" {
		t.Fatalf("viewer defaults were not applied: %+v", processor.req)
	}
}

func TestChatServiceAdapterRespondPropagatesError(t *testing.T) {
	adapter := NewChatServiceAdapter(&fakeChatProcessor{err: fmt.Errorf("boom")})
	_, err := adapter.Respond(context.Background(), chat.Input{SessionID: "session-1", Text: "hi"}, chat.RuntimePorts{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestChatServiceAdapterDecideRouteUsesInjectedDecider(t *testing.T) {
	adapter := NewChatServiceAdapterWithRoutePolicy(&fakeChatProcessor{}, NewMioRoutePolicy(fakeRouteDecisionProvider{route: routing.RouteCODE2}))

	got, err := adapter.DecideRoute(context.Background(), chat.Input{
		SessionID: "session-1",
		Channel:   "viewer",
		UserID:    "user-1",
		Text:      "実装して",
	})
	if err != nil {
		t.Fatalf("DecideRoute returned error: %v", err)
	}
	if got.Route != chat.RouteWorker {
		t.Fatalf("route = %s, want worker", got.Route)
	}
	if got.Reason != "decided for 実装して" {
		t.Fatalf("reason was not mapped: %+v", got)
	}
}

func TestNewRuntimeChatServiceWiresMioRoutePolicy(t *testing.T) {
	adapter := NewRuntimeChatService(&fakeChatProcessor{}, fakeRouteDecisionProvider{route: routing.RouteCODE})
	got, err := adapter.DecideRoute(context.Background(), chat.Input{Text: "修正して"})
	if err != nil {
		t.Fatalf("DecideRoute returned error: %v", err)
	}
	if got.Route != chat.RouteWorker {
		t.Fatalf("runtime chat service did not wire worker route: %+v", got)
	}
}

func TestChatServiceAdapterRouteMapping(t *testing.T) {
	tests := []struct {
		route routing.Route
		want  chat.Route
	}{
		{routing.RouteCHAT, chat.RouteChat},
		{routing.RouteCODE, chat.RouteWorker},
		{routing.RouteCODE3, chat.RouteWorker},
		{routing.RouteCODE4, chat.RouteWorker},
		{routing.RouteOPS, chat.RouteWorker},
		{routing.RoutePLAN, chat.RouteWorker},
		{routing.RouteANALYZE, chat.RouteWorker},
		{routing.RouteRESEARCH, chat.RouteLLM},
	}
	for _, tt := range tests {
		if got := chat.NormalizeRouteName(string(tt.route)); got != tt.want {
			t.Fatalf("route %s mapped to %s, want %s", tt.route, got, tt.want)
		}
	}
}
