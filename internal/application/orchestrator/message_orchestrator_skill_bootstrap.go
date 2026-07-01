package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	domainskill "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/skillgovernance"
)

func (o *MessageOrchestrator) recordRouteSkillBootstrap(ctx context.Context, req ProcessMessageRequest, route routing.Route) error {
	if o.skillBootstrap == nil || route == routing.RouteCHAT {
		return nil
	}
	return recordRouteSkillBootstrap(ctx, o.skillBootstrap, req, route)
}

func recordRouteSkillBootstrap(ctx context.Context, recorder SkillBootstrapRecorder, req ProcessMessageRequest, route routing.Route) error {
	if recorder == nil || route == routing.RouteCHAT {
		return nil
	}
	agent := "Worker"
	used := []string{"core.worker"}
	if isCodeRoute(route) {
		agent = "Coder"
		used = []string{"core.coder"}
	}
	if route == routing.RouteANALYZE {
		used = append(used, "core.heavy-worker")
	}
	if route == routing.RouteRESEARCH {
		used = append(used, "core.research")
	}
	if route == routing.RoutePLAN {
		used = append(used, "core.planning")
	}
	if route == routing.RouteWILD {
		used = append(used, "core.wild")
	}
	_, err := recorder.Record(ctx, domainskill.TaskContext{
		Text:         req.UserMessage,
		Intent:       strings.ToLower(route.String()),
		Agent:        agent,
		WorkstreamID: req.SessionID,
	}, used)
	if err != nil {
		return fmt.Errorf("route %s skill bootstrap failed: %w", route, err)
	}
	return nil
}
