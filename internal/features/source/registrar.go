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
// Handler implementations are supplied by their owning adapter and command packages.
type Routes struct {
	Registry              http.HandlerFunc
	XBookmarkWorkflows    http.HandlerFunc
	DomainGraphAssertions http.HandlerFunc
	MovieDomainGraphSync  http.HandlerFunc
	HobbyDomainGraphSync  http.HandlerFunc
}

// RegisterRoutes registers handlers at the feature route boundary.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/source-registry", routes.Registry)
	registerRoute(mux, "/viewer/x-bookmarks/workflows", routes.XBookmarkWorkflows)
	registerRoute(mux, "/viewer/x-bookmarks/workflows/run", routes.XBookmarkWorkflows)
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
