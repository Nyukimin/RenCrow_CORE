package llm

import (
	"context"
	"sort"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

type GenerationPolicy struct {
	EndpointExecutesGeneration bool     `json:"endpoint_executes_generation"`
	RequiredRequestFields      []string `json:"required_request_fields"`
	OptionalRequestFields      []string `json:"optional_request_fields,omitempty"`
	Description                string   `json:"description"`
}

type DiagnosticsSnapshot struct {
	UpdatedAt        string           `json:"updated_at"`
	Roles            []RoleStatus     `json:"roles"`
	GenerationPolicy GenerationPolicy `json:"generation_policy"`
}

func CurrentGenerationPolicy() GenerationPolicy {
	return GenerationPolicy{
		EndpointExecutesGeneration: false,
		RequiredRequestFields:      []string{"messages"},
		OptionalRequestFields:      []string{"max_tokens", "temperature", "system_prompt", "provider_options"},
		Description:                "Diagnostics endpoint does not call Generate; generation is only available through module llm.Provider.",
	}
}

func BuildDiagnosticsSnapshot(ctx context.Context, providers map[string]Provider, updatedAt time.Time) DiagnosticsSnapshot {
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	return DiagnosticsSnapshot{
		UpdatedAt:        updatedAt.UTC().Format(time.RFC3339),
		Roles:            CollectRoleStatus(ctx, providers, updatedAt),
		GenerationPolicy: CurrentGenerationPolicy(),
	}
}

type RoleStatus struct {
	Role     string            `json:"role"`
	Provider string            `json:"provider,omitempty"`
	Health   core.HealthReport `json:"health"`
}

func CollectRoleStatus(ctx context.Context, providers map[string]Provider, checkedAt time.Time) []RoleStatus {
	roles := sortedRoles(providers)
	out := make([]RoleStatus, 0, len(roles))
	for _, role := range roles {
		provider := providers[role]
		status := RoleStatus{Role: role}
		if provider == nil {
			status.Health = core.HealthReport{
				Module:    "llm:" + role,
				Status:    core.HealthDown,
				Ready:     false,
				Detail:    "provider is nil",
				CheckedAt: checkedAt,
			}
			out = append(out, status)
			continue
		}
		status.Provider = provider.Name()
		status.Health = provider.Health(ctx)
		status.Health.Module = "llm:" + role
		if status.Health.CheckedAt.IsZero() {
			status.Health.CheckedAt = checkedAt
		}
		out = append(out, status)
	}
	return out
}

func CollectHealthReports(ctx context.Context, providers map[string]Provider, checkedAt time.Time) []core.HealthReport {
	statuses := CollectRoleStatus(ctx, providers, checkedAt)
	reports := make([]core.HealthReport, 0, len(statuses))
	for _, status := range statuses {
		reports = append(reports, status.Health)
	}
	return reports
}

func sortedRoles(providers map[string]Provider) []string {
	roles := make([]string, 0, len(providers))
	for role := range providers {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}
