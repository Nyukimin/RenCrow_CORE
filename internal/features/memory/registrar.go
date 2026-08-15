package memory

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

// Routes groups Memory route handlers supplied by cmd/rencrow.
// Handler implementations are supplied by their owning adapter and command packages.
type Routes struct {
	Snapshot          http.HandlerFunc
	Layers            http.HandlerFunc
	Events            http.HandlerFunc
	State             http.HandlerFunc
	Promote           http.HandlerFunc
	User              http.HandlerFunc
	UserState         http.HandlerFunc
	UserForget        http.HandlerFunc
	UserSupersede     http.HandlerFunc
	RecallPack        http.HandlerFunc
	RecallTraces      http.HandlerFunc
	ProfilePromotions http.HandlerFunc
	ProfileRetry      http.HandlerFunc
	ChatGPTL3Import   http.HandlerFunc
	ChatGPTL3Confirm  http.HandlerFunc
}

// RegisterRoutes registers handlers at the feature route boundary.
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
	registerRoute(mux, "/viewer/memory/profile-promotions", routes.ProfilePromotions)
	registerRoute(mux, "/viewer/memory/profile-promotions/retry", routes.ProfileRetry)
	registerRoute(mux, "/viewer/memory/import/chatgpt", routes.ChatGPTL3Import)
	registerRoute(mux, "/viewer/memory/import/chatgpt/confirm", routes.ChatGPTL3Confirm)
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
