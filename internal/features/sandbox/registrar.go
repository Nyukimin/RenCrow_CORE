package sandbox

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

// Routes groups Sandbox route handlers supplied by cmd/rencrow.
// Handler implementations are supplied by their owning adapter and command packages.
type Routes struct {
	Status            http.HandlerFunc
	Promotion         http.HandlerFunc
	PromotionApply    http.HandlerFunc
	PromotionRollback http.HandlerFunc
	PromotionPreview  http.HandlerFunc
	WorktreeCreate    http.HandlerFunc
	WorktreeClose     http.HandlerFunc
}

// RegisterRoutes registers handlers at the feature route boundary.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/sandbox", routes.Status)
	registerRoute(mux, "/viewer/sandbox/promotions", routes.Promotion)
	registerRoute(mux, "/viewer/sandbox/promotions/apply", routes.PromotionApply)
	registerRoute(mux, "/viewer/sandbox/promotions/rollback", routes.PromotionRollback)
	registerRoute(mux, "/viewer/sandbox/promotions/preview", routes.PromotionPreview)
	registerRoute(mux, "/viewer/sandbox/worktrees/create", routes.WorktreeCreate)
	registerRoute(mux, "/viewer/sandbox/worktrees/close", routes.WorktreeClose)
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
