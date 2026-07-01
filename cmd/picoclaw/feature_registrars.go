package main

import (
	"net/http"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/viewer"
)

func registerFeatureRoutes(
	mux *http.ServeMux,
	cfg *config.Config,
	dependencies *Dependencies,
	sttRuntime sttRuntime,
	voiceChatRuntime voiceChatRuntime,
	debugSystemOpts viewer.DebugSystemOptions,
) {
	registerChannelRoutes(mux, dependencies)
	registerViewerBaseRoutes(mux, cfg, dependencies, debugSystemOpts)
	registerLLMOpsRoutes(mux, cfg, dependencies, &debugSystemOpts)
	registerOpsRoutes(mux, cfg, dependencies)
	registerSTTAndAudioRoutes(mux, cfg, sttRuntime, voiceChatRuntime, dependencies)
	registerWebRoutes(mux, dependencies)
	registerKnowledgeMemorySourceRoutes(mux, dependencies)
	registerGovernanceSecurityReportRoutes(mux, dependencies)
	registerViewerDynamicRoutes(mux, dependencies)
	registerIdleChatRoutes(mux, dependencies)
	registerHealthRoutes(mux, dependencies, cfg)
}
