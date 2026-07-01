package main

import (
	"net/http"

	modulecore "github.com/Nyukimin/picoclaw_multiLLM/modules/core"
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
	mux.HandleFunc(moduleManifestPath, handleModuleManifest())
	mux.HandleFunc(moduleHealthPath, dependencies.moduleHealth)
	mux.HandleFunc(moduleLLMDiagnosticsPath, handleModuleLLMDiagnostics(dependencies.moduleLLMProviders))
	mux.HandleFunc(moduleChatRoutePath, handleModuleChatRouteDecision(dependencies.moduleChatService))
	mux.HandleFunc(moduleWorkerDiagnosticsPath, handleModuleWorkerDiagnostics(dependencies.moduleWorkerExecutor))
	mux.HandleFunc(moduleTTSDiagnosticsPath, handleModuleTTSDiagnostics(dependencies.moduleTTSProvider))
	mux.HandleFunc(moduleTTSPlaybackStatePath, handleModuleTTSPlaybackState(dependencies.moduleTTSPlayback))
	mux.HandleFunc(moduleSTTDiagnosticsPath, handleModuleSTTDiagnostics(sttRuntime.Module))
	mux.HandleFunc(moduleSTTViewerInputPath, handleModuleSTTViewerInput(dependencies.moduleSTTViewerInput))
}
