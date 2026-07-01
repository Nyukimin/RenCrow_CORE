package llm

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

type fakeHealthCheckedProvider struct {
	name string
}

func (p fakeHealthCheckedProvider) Name() string {
	return p.name
}

func (p fakeHealthCheckedProvider) Health(context.Context) core.HealthReport {
	return core.HealthReport{Module: "llm", Status: core.HealthLive, Ready: true}
}

func (p fakeHealthCheckedProvider) Generate(context.Context, GenerateRequest) (GenerateResponse, error) {
	return GenerateResponse{Content: "generated"}, nil
}

type fakeHealthCheck struct {
	name   string
	result HealthCheckResult
}

func (c fakeHealthCheck) Name() string {
	return c.name
}

func (c fakeHealthCheck) Run(context.Context) HealthCheckResult {
	if c.result.Name == "" {
		c.result.Name = c.name
	}
	return c.result
}

func TestHealthCheckedProviderReflectsBackendDown(t *testing.T) {
	provider := NewHealthCheckedProvider(fakeHealthCheckedProvider{name: "worker-provider"}, fakeHealthCheck{
		name: "local_llm_worker",
		result: HealthCheckResult{
			Status:   core.HealthDown,
			Ready:    false,
			Detail:   "connection failed",
			Duration: 25 * time.Millisecond,
		},
	})

	got := provider.Health(context.Background())
	if got.Status != core.HealthDown || got.Ready {
		t.Fatalf("backend down was not reflected: %+v", got)
	}
	if got.Detail != "connection failed" || got.Metadata["check"] != "local_llm_worker" {
		t.Fatalf("health detail/metadata missing: %+v", got)
	}
}

func TestHealthCheckedProviderReflectsBackendOK(t *testing.T) {
	provider := NewHealthCheckedProvider(fakeHealthCheckedProvider{name: "chat-provider"}, fakeHealthCheck{
		name: "local_llm_chat",
		result: HealthCheckResult{
			Status: core.HealthLive,
			Ready:  true,
			Detail: "reachable",
		},
	})

	got := provider.Health(context.Background())
	if got.Status != core.HealthLive || !got.Ready {
		t.Fatalf("backend ok was not reflected: %+v", got)
	}
	if got.Detail != "reachable" {
		t.Fatalf("health detail missing: %+v", got)
	}
}

func TestNormalizeExternalHealthCheckResult(t *testing.T) {
	tests := []struct {
		status string
		want   core.HealthStatus
		ready  bool
	}{
		{status: "ok", want: core.HealthLive, ready: true},
		{status: " OK ", want: core.HealthLive, ready: true},
		{status: "degraded", want: core.HealthBlocked, ready: false},
		{status: "down", want: core.HealthDown, ready: false},
		{status: "", want: core.HealthDown, ready: false},
	}
	for _, tt := range tests {
		got := NormalizeExternalHealthCheckResult("check", tt.status, true, "detail", 25*time.Millisecond)
		if got.Status != tt.want || got.Ready != tt.ready || got.Name != "check" || got.Detail != "detail" || got.Duration != 25*time.Millisecond {
			t.Fatalf("status %q normalized unexpectedly: %+v", tt.status, got)
		}
	}
}

func TestShouldUseLocalHealthChecks(t *testing.T) {
	if !ShouldUseLocalHealthChecks(LocalHealthCheckRuntimeConfig{Enabled: true, Provider: " local_openai "}) {
		t.Fatal("local_openai runtime should use local health checks")
	}
	if ShouldUseLocalHealthChecks(LocalHealthCheckRuntimeConfig{Enabled: false, Provider: "local_openai"}) {
		t.Fatal("disabled local runtime should not use local health checks")
	}
	if ShouldUseLocalHealthChecks(LocalHealthCheckRuntimeConfig{Enabled: true, Provider: "ollama"}) {
		t.Fatal("non local_openai runtime should not use local health checks")
	}
}

func TestRoleFromHealthCheckName(t *testing.T) {
	tests := map[string]string{
		"local_llm_chat":     "chat",
		" local_llm_Worker ": "worker",
		"heavy":              "heavy",
		"":                   "",
	}
	for name, want := range tests {
		if got := RoleFromHealthCheckName(name); got != want {
			t.Fatalf("RoleFromHealthCheckName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestBuildProviderHealth(t *testing.T) {
	got := BuildProviderHealth(ProviderHealthSnapshot{Provider: "local-openai", Ready: true})
	if got.Module != "llm" || got.Status != core.HealthLive || !got.Ready || got.Metadata["provider"] != "local-openai" {
		t.Fatalf("unexpected ready health: %+v", got)
	}

	got = BuildProviderHealth(ProviderHealthSnapshot{})
	if got.Module != "llm" || got.Status != core.HealthDown || got.Ready || got.Detail != "llm provider is nil" {
		t.Fatalf("unexpected unavailable health: %+v", got)
	}
}
