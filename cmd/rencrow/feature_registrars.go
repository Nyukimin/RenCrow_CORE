package main

import (
	"net/http"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
)

func registerFeatureRoutes(
	mux *http.ServeMux,
	cfg *config.Config,
	dependencies *Dependencies,
	sttRuntime sttRuntime,
	voiceChatRuntime voiceChatRuntime,
	debugSystemOpts viewer.DebugSystemOptions,
) {
	registerChannelRoutes(mux, cfg, dependencies)
	registerViewerBaseRoutes(mux, cfg, dependencies, debugSystemOpts)
	dependencies.aiWorkflowHeavyRuntime = viewer.HandleAIWorkflowHeavyWorkerRuntimeDiagnostics(viewer.HeavyWorkerRuntimeDiagnosticsOptions{
		GatewayConfigured: strings.TrimSpace(debugSystemOpts.LLMGateway.BaseURL) != "",
		GatewayBaseURL:    debugSystemOpts.LLMGateway.BaseURL,
		LogicalAlias:      "kuro",
	})
	registerOpsRoutes(mux, cfg, dependencies)
	registerSTTAndAudioRoutes(mux, cfg, sttRuntime, voiceChatRuntime, dependencies)
	registerWebRoutes(mux, dependencies)
	registerKnowledgeMemorySourceRoutes(mux, dependencies)
	registerGovernanceSecurityReportRoutes(mux, dependencies)
	registerImageRoutes(mux, cfg)
	registerViewerDynamicRoutes(mux, dependencies)
	registerIdleChatRoutes(mux, dependencies)
	registerHealthRoutes(mux, dependencies, cfg)
}
