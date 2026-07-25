package main

import (
	"net/http"

	corefeature "github.com/Nyukimin/RenCrow_CORE/internal/features/core"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	moduleManifestPath          = modulecore.ModuleManifestEndpoint
	moduleHealthPath            = modulecore.ModuleHealthEndpoint
	moduleLLMDiagnosticsPath    = modulecore.ModuleLLMDiagnosticsEndpoint
	moduleChatRoutePath         = modulecore.ModuleChatRouteEndpoint
	moduleWorkerDiagnosticsPath = modulecore.ModuleWorkerDiagnosticsEndpoint
	moduleTTSDiagnosticsPath    = modulecore.ModuleTTSDiagnosticsEndpoint
	moduleTTSPlaybackStatePath  = modulecore.ModuleTTSPlaybackStateEndpoint
	moduleSTTDiagnosticsPath    = modulecore.ModuleSTTDiagnosticsEndpoint
	moduleSTTViewerInputPath    = modulecore.ModuleSTTViewerInputEndpoint
)

func currentRegisteredModuleEndpointPaths() []string {
	return modulecore.RegisteredModuleEndpointPaths()
}

func registerModuleRoutes(mux *http.ServeMux, dependencies *Dependencies, sttRuntime sttRuntime) {
	if mux == nil || dependencies == nil {
		return
	}
	dependencies.moduleHealth = handleModuleHealth(
		dependencies.moduleLLMProviders,
		dependencies.moduleChatService,
		dependencies.moduleTTSProvider,
		dependencies.moduleTTSPlayback,
		sttRuntime.Module,
		dependencies.moduleSTTViewerInput,
		dependencies.moduleWorkerExecutor,
	)
	corefeature.RegisterRoutes(mux, corefeature.Dependencies{Routes: corefeature.Routes{
		ModuleManifest:    handleModuleManifest(),
		ModuleHealth:      dependencies.moduleHealth,
		LLMDiagnostics:    handleModuleLLMDiagnostics(dependencies.moduleLLMProviders),
		ChatRoute:         handleModuleChatRouteDecision(dependencies.moduleChatService),
		WorkerDiagnostics: handleModuleWorkerDiagnostics(dependencies.moduleWorkerExecutor),
		TTSDiagnostics:    handleModuleTTSDiagnostics(dependencies.moduleTTSProvider),
		TTSPlaybackState:  handleModuleTTSPlaybackState(dependencies.moduleTTSPlayback),
		STTDiagnostics:    handleModuleSTTDiagnostics(sttRuntime.Module),
		STTViewerInput:    handleModuleSTTViewerInput(dependencies.moduleSTTViewerInput),
	}})
}
