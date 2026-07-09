package chat

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type fakeHealthProvider struct{}

func (fakeHealthProvider) Health(context.Context) core.HealthReport {
	return core.HealthReport{Module: "ignored", Status: core.HealthReady, Ready: true}
}

func TestBuildRouteReport(t *testing.T) {
	input := Input{SessionID: "s1", Channel: "viewer", Text: "実装して"}
	decision := RouteDecision{Route: RouteWorker, Reason: "rule match"}
	report := BuildRouteReport(context.Background(), fakeHealthProvider{}, input, decision, time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC))

	if report.Route != RouteWorker || report.Reason != "rule match" || report.Input.Text != "実装して" {
		t.Fatalf("route report did not preserve route data: %+v", report)
	}
	if report.Health.Module != "chat" || report.Health.CheckedAt.IsZero() {
		t.Fatalf("health was not normalized: %+v", report.Health)
	}
}

func TestRouteServiceUnavailableMessage(t *testing.T) {
	if RouteServiceUnavailableMessage != "chat module service unavailable" {
		t.Fatalf("unexpected unavailable message: %q", RouteServiceUnavailableMessage)
	}
}
