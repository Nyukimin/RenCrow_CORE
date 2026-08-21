package backlog

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
	Atlas http.HandlerFunc
}

// RegisterRoutes registers handlers at the feature route boundary.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	if mux == nil || deps.Routes.Atlas == nil {
		return
	}
	mux.HandleFunc("/viewer/atlas", deps.Routes.Atlas)
	mux.HandleFunc("/viewer/atlas/", deps.Routes.Atlas)
	mux.HandleFunc("/v1/atlas", deps.Routes.Atlas)
	mux.HandleFunc("/v1/atlas/", deps.Routes.Atlas)
}

// StartBackground reserves the feature background-job boundary.
func StartBackground(ctx context.Context, deps Dependencies) error {
	_ = ctx
	_ = deps
	return nil
}
