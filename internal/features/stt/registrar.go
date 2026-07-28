package stt

import (
	"context"
	"net/http"

	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

// Routes groups STT route handlers supplied by cmd/rencrow.
// Handler implementations are supplied by their owning adapter and command packages.
type Routes struct {
	ClientLog    http.HandlerFunc
	WAV          http.HandlerFunc
	RawWAV       http.HandlerFunc
	AutoTest     http.HandlerFunc
	AdminRestart http.HandlerFunc
	Health       http.HandlerFunc
	File         http.HandlerFunc
	ChatInput    http.HandlerFunc
	WebSocket    http.Handler
}

// RegisterRoutes registers handlers at the feature route boundary.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/stt/log", routes.ClientLog)
	registerRoute(mux, "/viewer/stt/wav", routes.WAV)
	registerRoute(mux, "/viewer/stt/wav/raw", routes.RawWAV)
	registerRoute(mux, "/viewer/stt/autotest", routes.AutoTest)
	registerRoute(mux, "/viewer/stt/admin/restart", routes.AdminRestart)
	registerRoute(mux, "/stt/health", routes.Health)
	registerRoute(mux, "/stt/file", routes.File)
	registerRoute(mux, "/stt/chat-input", routes.ChatInput)
	for _, path := range modulestt.WebSocketRoutePaths {
		registerHandler(mux, path, routes.WebSocket)
	}
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

func registerHandler(mux *http.ServeMux, pattern string, handler http.Handler) {
	if mux == nil || pattern == "" || handler == nil {
		return
	}
	mux.Handle(pattern, handler)
}
