package llm

import (
	"context"
	"strings"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

type HealthCheckResult struct {
	Name     string
	Status   core.HealthStatus
	Ready    bool
	Detail   string
	Duration time.Duration
}

type ProviderHealthSnapshot struct {
	Provider string
	Ready    bool
}

type LocalHealthCheckRuntimeConfig struct {
	Enabled  bool
	Provider string
}

const (
	ExternalHealthStatusOK       = "ok"
	ExternalHealthStatusDegraded = "degraded"
)

func ShouldUseLocalHealthChecks(cfg LocalHealthCheckRuntimeConfig) bool {
	return cfg.Enabled && strings.EqualFold(strings.TrimSpace(cfg.Provider), CoderProviderLocalOpenAI)
}

func NormalizeExternalHealthCheckResult(name string, status string, ready bool, detail string, duration time.Duration) HealthCheckResult {
	out := HealthCheckResult{
		Name:     name,
		Detail:   detail,
		Duration: duration,
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case ExternalHealthStatusOK:
		out.Status = core.HealthLive
		out.Ready = true
	case ExternalHealthStatusDegraded:
		out.Status = core.HealthBlocked
		out.Ready = false
	default:
		out.Status = core.HealthDown
		out.Ready = false
	}
	if ready && out.Status == core.HealthLive {
		out.Ready = true
	}
	return out
}

func RoleFromHealthCheckName(name string) string {
	role := strings.TrimPrefix(strings.TrimSpace(name), "local_llm_")
	role = strings.ToLower(strings.TrimSpace(role))
	return role
}

func BuildProviderHealth(snapshot ProviderHealthSnapshot) core.HealthReport {
	if !snapshot.Ready {
		return core.HealthReport{Module: "llm", Status: core.HealthDown, Detail: "llm provider is nil"}
	}
	return core.HealthReport{
		Module:   "llm",
		Status:   core.HealthLive,
		Ready:    true,
		Metadata: map[string]any{"provider": snapshot.Provider},
	}
}

type HealthCheck interface {
	Name() string
	Run(ctx context.Context) HealthCheckResult
}

type HealthCheckedProvider struct {
	provider Provider
	check    HealthCheck
}

func NewHealthCheckedProvider(provider Provider, check HealthCheck) HealthCheckedProvider {
	return HealthCheckedProvider{provider: provider, check: check}
}

func (p HealthCheckedProvider) Name() string {
	if p.provider == nil {
		return ""
	}
	return p.provider.Name()
}

func (p HealthCheckedProvider) Health(ctx context.Context) core.HealthReport {
	base := core.HealthReport{Module: "llm", Status: core.HealthDown, Detail: "llm provider is nil"}
	if p.provider != nil {
		base = p.provider.Health(ctx)
	}
	if p.check == nil {
		return base
	}
	result := p.check.Run(ctx)
	if base.Metadata == nil {
		base.Metadata = map[string]any{}
	}
	base.Metadata["check"] = result.Name
	base.Metadata["duration_ms"] = result.Duration.Milliseconds()
	base.Status = result.Status
	base.Ready = result.Ready
	base.Detail = result.Detail
	return base
}

func (p HealthCheckedProvider) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	return p.provider.Generate(ctx, req)
}
