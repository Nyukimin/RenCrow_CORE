package llm

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/picoclaw.
type Dependencies struct {
	Ports  Ports
	LLMOps LLMOpsRoutes
}

// RegisterRoutes registers LLM feature routes while handler bodies remain in
// their current adapter packages.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	registerRoute(mux, "/viewer/llm-ops/health", deps.LLMOps.Health)
	registerRoute(mux, "/viewer/llm-ops/status", deps.LLMOps.Status)
	registerRoute(mux, "/viewer/llm-ops/start", deps.LLMOps.Start)
	registerRoute(mux, "/viewer/llm-ops/stop", deps.LLMOps.Stop)
	registerRoute(mux, "/viewer/llm-ops/restart", deps.LLMOps.Restart)
}

func registerRoute(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	if handler == nil {
		return
	}
	mux.HandleFunc(pattern, handler)
}

// StartBackground reserves the feature background-job boundary.
func StartBackground(ctx context.Context, deps Dependencies) error {
	_ = ctx
	_ = deps
	return nil
}
