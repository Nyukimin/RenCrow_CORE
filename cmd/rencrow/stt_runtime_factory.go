package main

import (
	"net/http"
	"os"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/modulebridge"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	sttfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/stt"
	sttinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/stt"
	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
)

// This file is the integration boundary for RenCrow_STT.
// Keep STT provider selection, URL inference, handler creation, and route
// registration here so the main server does not depend on STT wiring details.

type sttRuntime struct {
	Provider     sttinfra.Provider
	Handler      *sttinfra.Handler
	ProviderURL  string
	GatewayURL   string
	WSHandler    http.Handler
	DebugOptions viewer.DebugSystemOptions
	Module       modulestt.Provider
}

func buildSTTRuntime(cfg *config.Config) sttRuntime {
	provider := buildSTTProvider(cfg)
	providerURL := inferSTTProviderURLFromConfig(cfg)
	gatewayURL := inferSTTGatewayURL(os.Getenv("STT_GATEWAY_URL"), os.Getenv("RENCROW_STT_URL"))
	return sttRuntime{
		Provider:    provider,
		Handler:     sttinfra.NewHandler(provider),
		ProviderURL: providerURL,
		GatewayURL:  gatewayURL,
		WSHandler:   resolveSTTWebSocketHandlerWithProvider(provider, providerURL, gatewayURL),
		Module:      modulebridge.NewRuntimeSTTProviderAdapter(provider),
		DebugOptions: viewer.DebugSystemOptions{
			TTSBaseURL:    inferTTSDebugBaseURLFromConfig(cfg),
			TTSHealthPath: inferTTSDebugHealthPathFromConfig(cfg),
			STTBaseURL:    inferSTTBaseURLFromConfig(cfg),
			STTStreamURL:  sttStreamURLFromConfig(cfg),
		},
	}
}

func registerSTTRuntimeRoutes(mux *http.ServeMux, rt sttRuntime) {
	sttfeature.RegisterRoutes(mux, sttfeature.Dependencies{Routes: sttRuntimeRoutes(rt)})
}

func sttRuntimeRoutes(rt sttRuntime) sttfeature.Routes {
	routes := sttfeature.Routes{WebSocket: rt.WSHandler}
	handler := rt.Handler
	if handler == nil {
		handler = sttinfra.NewHandler(nil)
	}
	routes.Health = handler.Health
	routes.File = handler.File
	routes.ChatInput = handler.ChatInput
	return routes
}

func registerSTTRoutes(mux *http.ServeMux, sttWSHandler http.Handler) {
	if mux == nil {
		return
	}
	for _, path := range modulestt.WebSocketRoutePaths {
		mux.Handle(path, sttWSHandler)
	}
}
