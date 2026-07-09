package main

import (
	"context"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domainhealth "github.com/Nyukimin/RenCrow_CORE/internal/domain/health"
	modulellm "github.com/Nyukimin/RenCrow_CORE/modules/llm"
)

type moduleLLMDomainHealthCheck struct {
	check domainhealth.Check
}

func wrapModuleLLMProvidersWithHealthChecks(cfg *config.Config, providers map[string]modulellm.Provider) map[string]modulellm.Provider {
	if len(providers) == 0 {
		return providers
	}
	checks := localLLMHealthChecksByRole(cfg)
	if len(checks) == 0 {
		return providers
	}
	wrapped := make(map[string]modulellm.Provider, len(providers))
	for role, provider := range providers {
		if check := checks[modulellm.NormalizeRoleName(role)]; check != nil {
			wrapped[role] = modulellm.NewHealthCheckedProvider(provider, moduleLLMDomainHealthCheck{check: check})
			continue
		}
		wrapped[role] = provider
	}
	return wrapped
}

func localLLMHealthChecksByRole(cfg *config.Config) map[string]domainhealth.Check {
	if cfg == nil || !modulellm.ShouldUseLocalHealthChecks(modulellm.LocalHealthCheckRuntimeConfig{Enabled: cfg.LocalLLM.Enabled, Provider: cfg.LocalLLM.Provider}) {
		return nil
	}
	checks := buildLocalLLMHealthChecks(cfg)
	out := make(map[string]domainhealth.Check, len(checks))
	for _, check := range checks {
		role := modulellm.RoleFromHealthCheckName(check.Name())
		if role != "" {
			out[role] = check
		}
	}
	return out
}

func (c moduleLLMDomainHealthCheck) Name() string {
	if c.check == nil {
		return ""
	}
	return c.check.Name()
}

func (c moduleLLMDomainHealthCheck) Run(ctx context.Context) modulellm.HealthCheckResult {
	if c.check == nil {
		return modulellm.NormalizeExternalHealthCheckResult("", "", false, "health check is nil", 0)
	}
	result := c.check.Run(ctx)
	return modulellm.NormalizeExternalHealthCheckResult(result.Name, string(result.Status), result.Status == domainhealth.StatusOK, result.Message, result.Duration)
}
