package superagent

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

// Routes groups SuperAgent route handlers supplied by cmd/rencrow.
// Handler implementations stay in legacy adapter/cmd packages during Ver0.80
// migration; this registrar owns only route registration and dependency handoff.
type Routes struct {
	Status           http.HandlerFunc
	Run              http.HandlerFunc
	RunPause         http.HandlerFunc
	RunResume        http.HandlerFunc
	RunQueue         http.HandlerFunc
	RunQueueClaim    http.HandlerFunc
	RunQueueComplete http.HandlerFunc
	SubagentTask     http.HandlerFunc
	ContextPack      http.HandlerFunc
	MessageChannel   http.HandlerFunc
	TraceEvent       http.HandlerFunc
}

// RegisterRoutes reserves the feature route boundary. Existing routes remain in
// their legacy packages until a phase migrates them through this registrar.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/superagent", routes.Status)
	registerRoute(mux, "/viewer/superagent/runs", routes.Run)
	registerRoute(mux, "/viewer/superagent/runs/pause", routes.RunPause)
	registerRoute(mux, "/viewer/superagent/runs/resume", routes.RunResume)
	registerRoute(mux, "/viewer/superagent/run-queue", routes.RunQueue)
	registerRoute(mux, "/viewer/superagent/run-queue/claim", routes.RunQueueClaim)
	registerRoute(mux, "/viewer/superagent/run-queue/complete", routes.RunQueueComplete)
	registerRoute(mux, "/viewer/superagent/subagent-tasks", routes.SubagentTask)
	registerRoute(mux, "/viewer/superagent/context-packs", routes.ContextPack)
	registerRoute(mux, "/viewer/superagent/message-channels", routes.MessageChannel)
	registerRoute(mux, "/viewer/superagent/trace-events", routes.TraceEvent)
}

// StartBackground reserves the feature background-job boundary.
func StartBackground(ctx context.Context, deps Dependencies) error {
	_ = ctx
	_ = deps
	return nil
}

func registerRoute(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	if mux == nil || pattern == "" || handler == nil {
		return
	}
	mux.HandleFunc(pattern, handler)
}
