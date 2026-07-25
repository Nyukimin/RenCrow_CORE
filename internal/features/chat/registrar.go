package chat

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
	Send http.HandlerFunc
}

// RegisterRoutes registers Chat routes supplied by the composition root.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	if mux != nil && deps.Routes.Send != nil {
		mux.HandleFunc("/viewer/send", deps.Routes.Send)
	}
}

// StartBackground reserves the feature background-job boundary.
func StartBackground(ctx context.Context, deps Dependencies) error {
	_ = ctx
	_ = deps
	return nil
}
