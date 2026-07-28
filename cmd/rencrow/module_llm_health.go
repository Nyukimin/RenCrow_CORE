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
	_ = cfg
	return providers
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
