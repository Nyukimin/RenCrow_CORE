package sandbox

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/picoclaw.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

// Routes groups Sandbox route handlers supplied by cmd/picoclaw.
// Handler implementations stay in legacy adapter/cmd packages during Ver0.80
// migration; this registrar owns only route registration and dependency handoff.
type Routes struct {
	Status                http.HandlerFunc
	Promotion             http.HandlerFunc
	PromotionApply        http.HandlerFunc
	PromotionRollback     http.HandlerFunc
	PromotionPreview      http.HandlerFunc
	PromotionManualReview http.HandlerFunc
	WorktreeCreate        http.HandlerFunc
	WorktreeClose         http.HandlerFunc
}

// RegisterRoutes reserves the feature route boundary. Existing routes remain in
// their legacy packages until a phase migrates them through this registrar.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/sandbox", routes.Status)
	registerRoute(mux, "/viewer/sandbox/promotions", routes.Promotion)
	registerRoute(mux, "/viewer/sandbox/promotions/apply", routes.PromotionApply)
	registerRoute(mux, "/viewer/sandbox/promotions/rollback", routes.PromotionRollback)
	registerRoute(mux, "/viewer/sandbox/promotions/preview", routes.PromotionPreview)
	registerRoute(mux, "/viewer/sandbox/promotions/manual-review", routes.PromotionManualReview)
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
