package chat

import "testing"

func TestNormalizeRouteName(t *testing.T) {
	tests := []struct {
		route string
		want  Route
	}{
		{"CHAT", RouteChat},
		{" chat ", RouteChat},
		{"CODE", RouteWorker},
		{"CODE1", RouteWorker},
		{"CODE2", RouteWorker},
		{"CODE3", RouteWorker},
		{"CODE4", RouteWorker},
		{"OPS", RouteWorker},
		{"PLAN", RouteWorker},
		{"ANALYZE", RouteWorker},
		{"STT", RouteSTT},
		{"TTS", RouteTTS},
		{"RESEARCH", RouteLLM},
		{"WILD", RouteLLM},
		{"unknown", RouteLLM},
	}
	for _, tt := range tests {
		if got := NormalizeRouteName(tt.route); got != tt.want {
			t.Fatalf("route %q normalized to %s, want %s", tt.route, got, tt.want)
		}
	}
}

func TestNormalizeRouteDecisionUsesExplicitReason(t *testing.T) {
	decision := NormalizeRouteDecision("CODE", "rule dictionary match")
	if decision.Route != RouteWorker {
		t.Fatalf("route = %s, want %s", decision.Route, RouteWorker)
	}
	if decision.Reason != "rule dictionary match" {
		t.Fatalf("reason = %q", decision.Reason)
	}
}

func TestNormalizeRouteDecisionFallsBackToRouteName(t *testing.T) {
	decision := NormalizeRouteDecision(" CHAT ", " ")
	if decision.Route != RouteChat {
		t.Fatalf("route = %s, want %s", decision.Route, RouteChat)
	}
	if decision.Reason != "CHAT" {
		t.Fatalf("reason = %q", decision.Reason)
	}
}
