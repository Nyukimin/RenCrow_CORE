package orchestrator

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRouteDecisionPinsSelectedMidoriChatForImageGeneration(t *testing.T) {
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteWILD, 0.99, "image generation keyword")}
	coordinator := newRouteDecisionCoordinator(mio)
	req := ProcessMessageRequest{
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "青い海と白い灯台の画像を生成して",
		To:          "midori",
	}
	taskID := modulecore.NewTaskID()
	input := newOrchestratorTestTurnInput(t, req.UserMessage, req.Channel, req.ChatID).WithViewerRecipient(req.To)

	decision, err := coordinator.Decide(context.Background(), input, req, taskID)
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if decision.Route != routing.RouteCHAT {
		t.Fatalf("route = %s, want CHAT for selected Midori", decision.Route)
	}
	if len(decision.Evidence) == 0 || decision.Evidence[len(decision.Evidence)-1].Source != "viewer_recipient" {
		t.Fatalf("decision evidence = %#v", decision.Evidence)
	}
}

func TestRouteDecisionKeepsExplicitWildCommandAboveMidoriSelection(t *testing.T) {
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteWILD, 1, "explicit command")}
	coordinator := newRouteDecisionCoordinator(mio)
	req := ProcessMessageRequest{
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "/wild 青い海と白い灯台の画像を生成して",
		To:          "midori",
	}
	taskID := modulecore.NewTaskID()
	input := newOrchestratorTestTurnInput(t, req.UserMessage, req.Channel, req.ChatID).WithViewerRecipient(req.To)

	decision, err := coordinator.Decide(context.Background(), input, req, taskID)
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if decision.Route != routing.RouteWILD {
		t.Fatalf("route = %s, want explicit WILD", decision.Route)
	}
}

func TestRouteDecisionKeepsAutomaticRoutingForDefaultMio(t *testing.T) {
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteWILD, 0.99, "image generation keyword")}
	coordinator := newRouteDecisionCoordinator(mio)
	req := ProcessMessageRequest{
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "青い海と白い灯台の画像を生成して",
		To:          "mio",
	}
	taskID := modulecore.NewTaskID()
	input := newOrchestratorTestTurnInput(t, req.UserMessage, req.Channel, req.ChatID).WithViewerRecipient(req.To)

	decision, err := coordinator.Decide(context.Background(), input, req, taskID)
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if decision.Route != routing.RouteWILD {
		t.Fatalf("route = %s, want automatic WILD for Mio", decision.Route)
	}
}

func TestRouteDecisionDoesNotPinOutsideDirectViewerAgentChat(t *testing.T) {
	tests := []struct {
		name string
		req  ProcessMessageRequest
	}{
		{
			name: "non viewer channel",
			req: ProcessMessageRequest{
				Channel: "line", UserMessage: "画像を生成して", To: "midori",
			},
		},
		{
			name: "default mio recipient keeps automatic routing",
			req: ProcessMessageRequest{
				Channel: "viewer", UserMessage: "画像を生成して", To: "mio",
			},
		},
		{
			name: "registered slash command after expansion",
			req: ProcessMessageRequest{
				Channel:             "viewer",
				UserMessage:         "展開済みの設計レビュー依頼",
				To:                  "midori",
				originalUserMessage: "/review-architecture 対象を確認して",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteWILD, 0.99, "automatic route")}
			coordinator := newRouteDecisionCoordinator(mio)
			taskID := modulecore.NewTaskID()
			input := newOrchestratorTestTurnInput(t, test.req.UserMessage, test.req.Channel, "viewer-user").WithViewerRecipient(test.req.To)

			decision, err := coordinator.Decide(context.Background(), input, test.req, taskID)
			if err != nil {
				t.Fatalf("Decide() error = %v", err)
			}
			if decision.Route != routing.RouteWILD {
				t.Fatalf("route = %s, want WILD", decision.Route)
			}
			if mio.decideCalls == 0 {
				t.Fatalf("Mio.DecideAction was not invoked for non-direct cases")
			}
		})
	}
}

func TestRouteDecisionPinsDirectShiroChatAndSkipsMioRouting(t *testing.T) {
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteWILD, 0.99, "would route elsewhere")}
	coordinator := newRouteDecisionCoordinator(mio)
	req := ProcessMessageRequest{
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "画像を生成して",
		To:          "shiro",
	}
	taskID := modulecore.NewTaskID()
	input := newOrchestratorTestTurnInput(t, req.UserMessage, req.Channel, req.ChatID).WithViewerRecipient(req.To)

	decision, err := coordinator.Decide(context.Background(), input, req, taskID)
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if decision.Route != routing.RouteCHAT {
		t.Fatalf("route = %s, want CHAT for explicit Shiro recipient", decision.Route)
	}
	if mio.decideCalls != 0 {
		t.Fatalf("Mio.DecideAction must be skipped for explicit Viewer recipient, got %d calls", mio.decideCalls)
	}
	if len(decision.Evidence) == 0 || decision.Evidence[len(decision.Evidence)-1].Source != "viewer_recipient" {
		t.Fatalf("decision evidence = %#v", decision.Evidence)
	}
}

func TestRouteDecisionPinsDirectKuroChatAndSkipsMioRouting(t *testing.T) {
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteANALYZE, 0.9, "deep analysis")}
	coordinator := newRouteDecisionCoordinator(mio)
	req := ProcessMessageRequest{
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "最新のログを俯瞰で見て",
		To:          "kuro",
	}
	taskID := modulecore.NewTaskID()
	input := newOrchestratorTestTurnInput(t, req.UserMessage, req.Channel, req.ChatID).WithViewerRecipient(req.To)

	decision, err := coordinator.Decide(context.Background(), input, req, taskID)
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if decision.Route != routing.RouteCHAT {
		t.Fatalf("route = %s, want CHAT for explicit Kuro recipient", decision.Route)
	}
	if mio.decideCalls != 0 {
		t.Fatalf("Mio.DecideAction must be skipped for explicit Viewer recipient, got %d calls", mio.decideCalls)
	}
}

func TestRouteDecisionDirectMidoriPinSkipsMioRouting(t *testing.T) {
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteWILD, 0.99, "image generation keyword")}
	coordinator := newRouteDecisionCoordinator(mio)
	req := ProcessMessageRequest{
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "青い海と白い灯台の画像を生成して",
		To:          "midori",
	}
	taskID := modulecore.NewTaskID()
	input := newOrchestratorTestTurnInput(t, req.UserMessage, req.Channel, req.ChatID).WithViewerRecipient(req.To)

	decision, err := coordinator.Decide(context.Background(), input, req, taskID)
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if decision.Route != routing.RouteCHAT {
		t.Fatalf("route = %s, want CHAT for explicit Midori recipient", decision.Route)
	}
	if mio.decideCalls != 0 {
		t.Fatalf("Mio.DecideAction must be skipped for explicit Viewer recipient, got %d calls", mio.decideCalls)
	}
}
