package llm

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

func TestCurrentGenerationPolicyDoesNotExecuteGeneration(t *testing.T) {
	policy := CurrentGenerationPolicy()
	if policy.EndpointExecutesGeneration {
		t.Fatalf("diagnostics policy must not execute generation: %+v", policy)
	}
	if !containsString(policy.RequiredRequestFields, "messages") {
		t.Fatalf("messages must remain the required generation field: %+v", policy)
	}
	if policy.Description == "" {
		t.Fatalf("generation policy must describe diagnostics behavior: %+v", policy)
	}
}

func TestBuildDiagnosticsSnapshot(t *testing.T) {
	updatedAt := time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC)
	snapshot := BuildDiagnosticsSnapshot(context.Background(), map[string]Provider{
		"chat": fakeDiagnosticsProvider{name: "chat-provider"},
	}, updatedAt)
	if snapshot.UpdatedAt != "2026-05-30T01:02:03Z" || len(snapshot.Roles) != 1 {
		t.Fatalf("unexpected diagnostics snapshot: %+v", snapshot)
	}
	if snapshot.GenerationPolicy.EndpointExecutesGeneration {
		t.Fatalf("diagnostics endpoint must not execute generation: %+v", snapshot.GenerationPolicy)
	}
}

func TestCollectRoleStatusOrdersRolesAndIncludesNilProvider(t *testing.T) {
	checkedAt := time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC)
	got := CollectRoleStatus(context.Background(), map[string]Provider{
		"worker": fakeDiagnosticsProvider{name: "worker-provider"},
		"chat":   fakeDiagnosticsProvider{name: "chat-provider"},
		"wild":   nil,
	}, checkedAt)

	if len(got) != 3 {
		t.Fatalf("unexpected role count: %+v", got)
	}
	if got[0].Role != "chat" || got[1].Role != "wild" || got[2].Role != "worker" {
		t.Fatalf("roles were not sorted: %+v", got)
	}
	if got[1].Health.Status != core.HealthDown || got[1].Health.CheckedAt.IsZero() {
		t.Fatalf("nil provider was not reported down: %+v", got[1])
	}
	if got[0].Health.Module != "llm:chat" || got[2].Health.Module != "llm:worker" {
		t.Fatalf("role-qualified health module was not set: %+v", got)
	}
}

func TestCollectHealthReportsReturnsRoleQualifiedReports(t *testing.T) {
	checkedAt := time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC)
	got := CollectHealthReports(context.Background(), map[string]Provider{
		"worker": fakeDiagnosticsProvider{name: "worker-provider"},
		"chat":   fakeDiagnosticsProvider{name: "chat-provider"},
	}, checkedAt)
	if len(got) != 2 {
		t.Fatalf("unexpected report count: %+v", got)
	}
	if got[0].Module != "llm:chat" || got[1].Module != "llm:worker" {
		t.Fatalf("health reports were not role-qualified and sorted: %+v", got)
	}
}

type fakeDiagnosticsProvider struct {
	name string
}

func (p fakeDiagnosticsProvider) Name() string {
	return p.name
}

func (p fakeDiagnosticsProvider) Health(context.Context) core.HealthReport {
	return core.HealthReport{Module: "llm", Status: core.HealthLive, Ready: true}
}

func (p fakeDiagnosticsProvider) Generate(context.Context, GenerateRequest) (GenerateResponse, error) {
	return GenerateResponse{Content: "ok"}, nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
