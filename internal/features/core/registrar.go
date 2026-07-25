package core

import (
	"context"
	"net/http"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

type Routes struct {
	Live              http.HandlerFunc
	Health            http.HandlerFunc
	Ready             http.HandlerFunc
	ModuleManifest    http.HandlerFunc
	ModuleHealth      http.HandlerFunc
	LLMDiagnostics    http.HandlerFunc
	ChatRoute         http.HandlerFunc
	WorkerDiagnostics http.HandlerFunc
	TTSDiagnostics    http.HandlerFunc
	TTSPlaybackState  http.HandlerFunc
	STTDiagnostics    http.HandlerFunc
	STTViewerInput    http.HandlerFunc
}

// RegisterRoutes registers process health routes supplied by the composition root.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	if mux == nil {
		return
	}
	registerRoute(mux, "/health/live", deps.Routes.Live)
	registerRoute(mux, "/health", deps.Routes.Health)
	registerRoute(mux, "/ready", deps.Routes.Ready)
	registerRoute(mux, modulecore.ModuleManifestEndpoint, deps.Routes.ModuleManifest)
	registerRoute(mux, modulecore.ModuleHealthEndpoint, deps.Routes.ModuleHealth)
	registerRoute(mux, modulecore.ModuleLLMDiagnosticsEndpoint, deps.Routes.LLMDiagnostics)
	registerRoute(mux, modulecore.ModuleChatRouteEndpoint, deps.Routes.ChatRoute)
	registerRoute(mux, modulecore.ModuleWorkerDiagnosticsEndpoint, deps.Routes.WorkerDiagnostics)
	registerRoute(mux, modulecore.ModuleTTSDiagnosticsEndpoint, deps.Routes.TTSDiagnostics)
	registerRoute(mux, modulecore.ModuleTTSPlaybackStateEndpoint, deps.Routes.TTSPlaybackState)
	registerRoute(mux, modulecore.ModuleSTTDiagnosticsEndpoint, deps.Routes.STTDiagnostics)
	registerRoute(mux, modulecore.ModuleSTTViewerInputEndpoint, deps.Routes.STTViewerInput)
}

func registerRoute(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	if handler != nil {
		mux.HandleFunc(pattern, handler)
	}
}

// StartBackground reserves the feature background-job boundary.
func StartBackground(ctx context.Context, deps Dependencies) error {
	_ = ctx
	_ = deps
	return nil
}
