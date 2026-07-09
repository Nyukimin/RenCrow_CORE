package source

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

// Routes groups Source Registry route handlers supplied by cmd/rencrow.
// Handler implementations stay in legacy adapter/cmd packages during Ver0.80
// migration; this registrar owns only route registration and dependency handoff.
type Routes struct {
	Registry              http.HandlerFunc
	DomainGraphAssertions http.HandlerFunc
	MovieDomainGraphSync  http.HandlerFunc
	HobbyDomainGraphSync  http.HandlerFunc
}

// RegisterRoutes reserves the feature route boundary. Existing routes remain in
// their legacy packages until a phase migrates them through this registrar.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/source-registry", routes.Registry)
	registerRoute(mux, "/viewer/domain-graph/assertions", routes.DomainGraphAssertions)
	registerRoute(mux, "/viewer/movie-catalog/domain-graph-sync", routes.MovieDomainGraphSync)
	registerRoute(mux, "/viewer/hobby-graph/domain-graph-sync", routes.HobbyDomainGraphSync)
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
