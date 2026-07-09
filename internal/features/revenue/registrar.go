package revenue

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports Ports
}

// RegisterRoutes reserves the feature route boundary. Existing routes remain in
// their legacy packages until a phase migrates them through this registrar.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	_ = mux
	_ = deps
}

// StartBackground reserves the feature background-job boundary.
func StartBackground(ctx context.Context, deps Dependencies) error {
	_ = ctx
	_ = deps
	return nil
}
