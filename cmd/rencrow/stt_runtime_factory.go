package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/modulebridge"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	sttfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/stt"
	sttinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/stt"
	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
)

// This file is the integration boundary for RenCrow_STT.
// Keep STT provider selection, URL inference, handler creation, and route
// registration here so the main server does not depend on STT wiring details.

type sttRuntime struct {
	Provider       sttinfra.Provider
	Handler        *sttinfra.Handler
	GatewayHTTPURL string
	WSHandler      http.Handler
	DebugOptions   viewer.DebugSystemOptions
	Module         modulestt.Provider
	// StreamHealth reports whether a /stt connection can be relayed to the STT stream.
	StreamHealth namedHealthProvider
	// Recorder records the utterances /stt relays. nil when STT is off.
	Recorder *sttinfra.UtteranceRecorder
}

// sttRecorderShutdownFlush is how long shutdown waits for queued records.
const sttRecorderShutdownFlush = 5 * time.Second

// CloseRecorder flushes the utterance recorder, within ctx, before the process exits.
func (rt sttRuntime) CloseRecorder(ctx context.Context) error {
	if rt.Recorder == nil {
		return nil
	}
	return rt.Recorder.Close(ctx)
}

func buildSTTRuntime(cfg *config.Config, tasks *taskmanager.Manager, actions *actionmanager.Manager, transport *transportmanager.Manager) sttRuntime {
	provider := buildSTTProvider(cfg)
	var recorder *sttinfra.UtteranceRecorder
	var relayRecorder sttTerminalRecorder
	var streamHealth namedHealthProvider = sttStreamUnavailableHealth{}
	if provider != nil {
		ownedProvider, err := sttinfra.NewActionProvider(provider, actions, tasks, transport, "mio")
		if err != nil {
			log.Fatalf("Failed to connect STT provider to execution owners: %v", err)
		}
		provider = ownedProvider
		// /stt does not call the provider: RenCrow_STT recognises, and CORE keeps
		// the evidence of each recognition, after the fact and boundedly.
		recorder, err = sttinfra.NewUtteranceRecorder(actions, tasks, "mio", sttinfra.RecorderConfig{})
		if err != nil {
			log.Fatalf("Failed to connect the STT utterance recorder to execution owners: %v", err)
		}
		relayRecorder = recorder
		streamHealth = newSTTStreamHealthProvider(sttStreamHealthConfig{
			BaseURL:       inferSTTBaseURLFromConfig(cfg),
			RecorderStats: recorder.Stats,
		})
	}
	gatewayHTTPURL := inferSTTGatewayHTTPURLFromConfig(cfg)
	return sttRuntime{
		Provider:       provider,
		Handler:        sttinfra.NewHandler(provider),
		GatewayHTTPURL: gatewayHTTPURL,
		WSHandler:      newSTTRelayHandler(sttRelayConfigFromAppConfig(cfg, provider != nil, relayRecorder)),
		StreamHealth:   streamHealth,
		Recorder:       recorder,
		Module:         modulebridge.NewRuntimeSTTProviderAdapter(provider),
		DebugOptions: viewer.DebugSystemOptions{
			TTSBaseURL:    inferTTSDebugBaseURLFromConfig(cfg),
			TTSHealthPath: inferTTSDebugHealthPathFromConfig(cfg),
			STTBaseURL:    inferSTTBaseURLFromConfig(cfg),
			STTStreamURL:  "/stt",
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
