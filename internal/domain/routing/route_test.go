package routing

import "testing"

func TestRouteString(t *testing.T) {
	route := RouteCODE3
	if route.String() != "CODE3" {
		t.Errorf("Expected 'CODE3', got '%s'", route.String())
	}
}

func TestRouteIsCoderRoute(t *testing.T) {
	tests := []struct {
		route   Route
		isCoder bool
		name    string
	}{
		{RouteCODE, true, "CODE should be coder route"},
		{RouteCODE1, true, "CODE1 should be coder route"},
		{RouteCODE2, true, "CODE2 should be coder route"},
		{RouteCODE3, true, "CODE3 should be coder route"},
		{RouteCODE4, true, "CODE4 should be coder route"},
		{RouteWILD, false, "WILD should not be coder route"},
		{RouteCHAT, false, "CHAT should not be coder route"},
		{RoutePLAN, false, "PLAN should not be coder route"},
		{RouteANALYZE, false, "ANALYZE should not be coder route"},
		{RouteOPS, false, "OPS should not be coder route"},
		{RouteRESEARCH, false, "RESEARCH should not be coder route"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.route.IsCoderRoute() != tt.isCoder {
				t.Errorf("%s: expected IsCoderRoute()=%v, got %v",
					tt.route, tt.isCoder, tt.route.IsCoderRoute())
			}
		})
	}
}

func TestRouteToCoderSlot(t *testing.T) {
	tests := []struct {
		route Route
		want  string
	}{
		{RouteCODE, "coder1"},
		{RouteCODE1, "coder1"},
		{RouteCODE2, "coder2"},
		{RouteCODE3, "coder3"},
		{RouteCODE4, "coder4"},
		{RouteCHAT, ""},
		{RoutePLAN, ""},
		{RouteANALYZE, ""},
		{RouteOPS, ""},
		{RouteRESEARCH, ""},
		{RouteWILD, ""},
	}

	for _, tt := range tests {
		t.Run(string(tt.route), func(t *testing.T) {
			if got := tt.route.RouteToCoderSlot(); got != tt.want {
				t.Fatalf("%s.RouteToCoderSlot() = %q, want %q", tt.route, got, tt.want)
			}
		})
	}
}

func TestNewDecision(t *testing.T) {
	decision := NewDecision(RouteCODE3, 0.95, "Explicit command")

	if decision.Route != RouteCODE3 {
		t.Errorf("Expected route CODE3, got %s", decision.Route)
	}

	if decision.Confidence != 0.95 {
		t.Errorf("Expected confidence 0.95, got %f", decision.Confidence)
	}

	if decision.Reason != "Explicit command" {
		t.Errorf("Expected reason 'Explicit command', got '%s'", decision.Reason)
	}
}

func TestNewDecisionWithEvidence(t *testing.T) {
	decision := NewDecisionWithEvidence(RouteCHAT, 0.7, "default", DecisionEvidence{
		Source:     EvidenceSourceSafeFallback,
		Matched:    true,
		Route:      RouteCHAT,
		Confidence: 0.7,
		Reason:     "no rule match",
	})

	if len(decision.Evidence) != 1 {
		t.Fatalf("evidence count=%d, want 1", len(decision.Evidence))
	}
	ev := decision.Evidence[0]
	if ev.Source != EvidenceSourceSafeFallback {
		t.Fatalf("source=%q, want safe_fallback", ev.Source)
	}
	if !ev.Matched || ev.Route != RouteCHAT || ev.Confidence != 0.7 {
		t.Fatalf("unexpected evidence: %#v", ev)
	}
}
