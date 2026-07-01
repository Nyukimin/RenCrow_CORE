package chat

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeService struct{}

func (fakeService) DecideRoute(context.Context, Input) (RouteDecision, error) {
	return RouteDecision{Route: RouteChat}, nil
}

func (fakeService) Respond(context.Context, Input, RuntimePorts) (Output, error) {
	return Output{Text: "ok"}, nil
}

func TestServiceIncludesRoutePolicy(t *testing.T) {
	var svc Service = fakeService{}
	var policy RoutePolicy = svc

	decision, err := policy.DecideRoute(context.Background(), Input{Text: "hello"})
	if err != nil {
		t.Fatalf("DecideRoute returned error: %v", err)
	}
	if decision.Route != RouteChat {
		t.Fatalf("route = %s, want %s", decision.Route, RouteChat)
	}
}

func TestInputJSONContractForModuleRoute(t *testing.T) {
	var input Input
	if err := json.Unmarshal([]byte(`{"session_id":"s1","channel":"viewer","user_id":"u1","text":"実装して"}`), &input); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}
	if input.SessionID != "s1" || input.Channel != "viewer" || input.UserID != "u1" || input.Text != "実装して" || input.To != "" {
		t.Fatalf("unexpected input: %+v", input)
	}
}

func TestInputJSONContractIncludesViewerRecipient(t *testing.T) {
	var input Input
	if err := json.Unmarshal([]byte(`{"session_id":"s1","channel":"viewer","user_id":"u1","to":"shiro","text":"相談して"}`), &input); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}
	normalized := NormalizeInput(input)
	if normalized.To != ViewerRecipientShiro {
		t.Fatalf("recipient = %q, want %q", normalized.To, ViewerRecipientShiro)
	}
}
