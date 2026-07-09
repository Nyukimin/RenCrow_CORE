package chat

import (
	"context"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type RouteReport struct {
	Route  Route             `json:"route"`
	Reason string            `json:"reason,omitempty"`
	Input  Input             `json:"input"`
	Health core.HealthReport `json:"health"`
}

const RouteServiceUnavailableMessage = "chat module service unavailable"

func BuildRouteReport(ctx context.Context, service core.HealthProvider, input Input, decision RouteDecision, updatedAt time.Time) RouteReport {
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	return RouteReport{
		Route:  decision.Route,
		Reason: decision.Reason,
		Input:  input,
		Health: core.ProviderHealth(ctx, "chat", service, updatedAt),
	}
}
