package idlechat

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/picoclaw.
type Dependencies struct {
	Ports      Ports
	Routes     Routes
	Background Background
}

// Routes groups IdleChat Viewer route handlers supplied by cmd/picoclaw.
// Handler implementations remain in legacy cmd/application packages during
// Ver0.80 migration; this registrar owns only route registration.
type Routes struct {
	Start       http.HandlerFunc
	Stop        http.HandlerFunc
	Interrupt   http.HandlerFunc
	Status      http.HandlerFunc
	Logs        http.HandlerFunc
	Forecast    http.HandlerFunc
	Story       http.HandlerFunc
	StorySimple http.HandlerFunc
}

// Background is the minimum IdleChat runtime start boundary.
type Background interface {
	Start()
}

// RegisterRoutes reserves the feature route boundary. Existing routes remain in
// their legacy packages until a phase migrates them through this registrar.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/idlechat/start", routes.Start)
	registerRoute(mux, "/viewer/idlechat/stop", routes.Stop)
	registerRoute(mux, "/viewer/idlechat/interrupt", routes.Interrupt)
	registerRoute(mux, "/viewer/idlechat/status", routes.Status)
	registerRoute(mux, "/viewer/idlechat/logs", routes.Logs)
	registerRoute(mux, "/viewer/idlechat/forecast", routes.Forecast)
	registerRoute(mux, "/viewer/idlechat/story", routes.Story)
	registerRoute(mux, "/viewer/idlechat/story-simple", routes.StorySimple)
}

// StartBackground reserves the feature background-job boundary.
func StartBackground(ctx context.Context, deps Dependencies) error {
	_ = ctx
	if deps.Background != nil {
		deps.Background.Start()
	}
	return nil
}

func registerRoute(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	if mux == nil || pattern == "" || handler == nil {
		return
	}
	mux.HandleFunc(pattern, handler)
}
