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
// Handler implementations are supplied by their owning adapter and command packages.
type Routes struct {
	Status         http.HandlerFunc
	RunPause       http.HandlerFunc
	RunResume      http.HandlerFunc
	MessageChannel http.HandlerFunc
}

// RegisterRoutes registers handlers at the feature route boundary.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/superagent", routes.Status)
	registerRoute(mux, "/viewer/superagent/runs/pause", routes.RunPause)
	registerRoute(mux, "/viewer/superagent/runs/resume", routes.RunResume)
	registerRoute(mux, "/viewer/superagent/message-channels", routes.MessageChannel)
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
