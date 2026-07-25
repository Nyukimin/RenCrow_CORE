package avatar

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
	CharacterRuntime http.HandlerFunc
}

// RegisterRoutes registers Avatar routes supplied by the composition root.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	if mux != nil && deps.Routes.CharacterRuntime != nil {
		mux.HandleFunc("/viewer/character-runtime", deps.Routes.CharacterRuntime)
	}
}

// StartBackground reserves the feature background-job boundary.
func StartBackground(ctx context.Context, deps Dependencies) error {
	_ = ctx
	_ = deps
	return nil
}
