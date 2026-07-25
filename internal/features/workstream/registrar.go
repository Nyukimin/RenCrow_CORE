package workstream

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

type Routes struct {
	Status       http.HandlerFunc
	Goals        http.HandlerFunc
	Artifacts    http.HandlerFunc
	Annotations  http.HandlerFunc
	Steering     http.HandlerFunc
	Heartbeats   http.HandlerFunc
	VaultUpdates http.HandlerFunc
	VaultReview  http.HandlerFunc
	VaultPreview http.HandlerFunc
}

func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/workstreams", routes.Status)
	registerRoute(mux, "/viewer/workstreams/goals", routes.Goals)
	registerRoute(mux, "/viewer/workstreams/artifacts", routes.Artifacts)
	registerRoute(mux, "/viewer/workstreams/annotations", routes.Annotations)
	registerRoute(mux, "/viewer/workstreams/steering", routes.Steering)
	registerRoute(mux, "/viewer/workstreams/heartbeats", routes.Heartbeats)
	registerRoute(mux, "/viewer/workstreams/vault-updates", routes.VaultUpdates)
	registerRoute(mux, "/viewer/workstreams/vault-updates/review", routes.VaultReview)
	registerRoute(mux, "/viewer/workstreams/vault-updates/preview", routes.VaultPreview)
}

func registerRoute(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	if mux != nil && pattern != "" && handler != nil {
		mux.HandleFunc(pattern, handler)
	}
}

// StartBackground reserves the feature background-job boundary.
func StartBackground(ctx context.Context, deps Dependencies) error {
	_ = ctx
	_ = deps
	return nil
}
