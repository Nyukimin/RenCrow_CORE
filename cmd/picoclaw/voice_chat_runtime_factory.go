package main

import (
	"net/http"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/viewer"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	modulevoicechat "github.com/Nyukimin/picoclaw_multiLLM/modules/voicechat"
)

type voiceChatRuntime struct {
	Enabled    bool
	GatewayURL string
	InputMode  string
	WSHandler  http.Handler
}

func buildVoiceChatRuntime(cfg *config.Config, voiceDirect voiceDirectFinalHandler, idleNotifier orchestrator.IdleNotifier) voiceChatRuntime {
	enabled := voiceChatEnabledFromEnv()
	gatewayURL := inferVoiceChatGatewayURL(cfg)
	inputMode := voiceInputModeFromEnv()
	plan := modulevoicechat.BuildBridgePlan(enabled, gatewayURL, inputMode)
	return voiceChatRuntime{
		Enabled:    plan.Enabled,
		GatewayURL: plan.GatewayURL,
		InputMode:  plan.InputMode,
		WSHandler:  resolveVoiceChatWebSocketHandler(plan, voiceDirect, idleNotifier),
	}
}

func voiceChatDebugOptions(cfg *config.Config, rt voiceChatRuntime) viewer.DebugSystemOptions {
	plan := modulevoicechat.BuildBridgePlan(rt.Enabled, rt.GatewayURL, rt.InputMode)
	return viewer.DebugSystemOptions{
		VoiceChatEnabled:           plan.Available,
		VoiceChatGatewayConfigured: strings.TrimSpace(rt.GatewayURL) != "",
		VoiceInputMode:             plan.InputMode,
	}
}

func registerVoiceChatRuntimeRoutes(mux *http.ServeMux, rt voiceChatRuntime) {
	registerVoiceChatRoutes(mux, rt.WSHandler)
}
