package memory

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/picoclaw.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

// Routes groups Memory route handlers supplied by cmd/picoclaw.
// Handler implementations stay in legacy adapter/cmd packages during Ver0.80
// migration; this registrar owns only route registration and dependency handoff.
type Routes struct {
	Snapshot      http.HandlerFunc
	Layers        http.HandlerFunc
	Events        http.HandlerFunc
	State         http.HandlerFunc
	Promote       http.HandlerFunc
	User          http.HandlerFunc
	UserState     http.HandlerFunc
	UserForget    http.HandlerFunc
	UserSupersede http.HandlerFunc
	RecallPack    http.HandlerFunc
	RecallTraces  http.HandlerFunc
}

// RegisterRoutes reserves the feature route boundary. Existing routes remain in
// their legacy packages until a phase migrates them through this registrar.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/memory/snapshot", routes.Snapshot)
	registerRoute(mux, "/viewer/memory/layers", routes.Layers)
	registerRoute(mux, "/viewer/memory/events", routes.Events)
	registerRoute(mux, "/viewer/memory/state", routes.State)
	registerRoute(mux, "/viewer/memory/promote", routes.Promote)
	registerRoute(mux, "/viewer/memory/user", routes.User)
	registerRoute(mux, "/viewer/memory/user/state", routes.UserState)
	registerRoute(mux, "/viewer/memory/user/forget", routes.UserForget)
	registerRoute(mux, "/viewer/memory/user/supersede", routes.UserSupersede)
	registerRoute(mux, "/viewer/memory/recall-pack", routes.RecallPack)
	registerRoute(mux, "/viewer/recall/traces", routes.RecallTraces)
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
