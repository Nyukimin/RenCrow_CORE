package main

import (
	"net/http"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	modulevoicechat "github.com/Nyukimin/RenCrow_CORE/modules/voicechat"
)

type voiceChatRuntime struct {
	Enabled    bool
	GatewayURL string
	InputMode  string
	WSHandler  http.Handler
}

func buildVoiceChatRuntime(cfg *config.Config, voiceDirect voiceDirectFinalHandler, idleNotifier orchestrator.IdleNotifier, owners voiceChatExecutionOwners) voiceChatRuntime {
	// idle_chat 無効時は *idlechat.IdleChatOrchestrator の typed nil が渡る。
	// interface に包んだままでは nil 判定を通過して NotifyActivity が panic する
	// ため、境界で nil interface に正規化する（2026-09-25 本番 panic 回帰）。
	if orch, ok := idleNotifier.(*idlechat.IdleChatOrchestrator); ok && orch == nil {
		idleNotifier = nil
	}
	enabled := voiceChatEnabledFromEnv()
	gatewayURL := inferVoiceChatGatewayURL(cfg)
	inputMode := voiceInputModeFromEnv()
	plan := modulevoicechat.BuildBridgePlan(enabled, gatewayURL, inputMode)
	return voiceChatRuntime{
		Enabled:    plan.Enabled,
		GatewayURL: plan.GatewayURL,
		InputMode:  plan.InputMode,
		WSHandler:  resolveVoiceChatWebSocketHandler(plan, voiceChatInputAudioSettingsFromConfig(cfg), voiceDirect, idleNotifier, owners),
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
