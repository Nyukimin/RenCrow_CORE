package main

import (
	"os"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	modulellm "github.com/Nyukimin/RenCrow_CORE/modules/llm"
	modulevoicechat "github.com/Nyukimin/RenCrow_CORE/modules/voicechat"
)

func voiceChatEnabledFromEnv() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("VOICE_CHAT_ENABLED")), "true")
}

func voiceInputModeFromEnv() string {
	if raw := strings.TrimSpace(os.Getenv("VOICE_INPUT_MODE")); raw != "" {
		return modulevoicechat.NormalizeVoiceInputMode(raw)
	}
	return modulevoicechat.VoiceInputModeSTTPrimary
}

func inferVoiceChatGatewayURL(cfg *config.Config) string {
	chatBaseURL := ""
	if cfg != nil {
		chatBaseURL = modulellm.LocalBaseURLForAlias(localRuntimeConfigFromAppConfig(cfg), modulellm.RoleChat)
	}
	return modulevoicechat.InferGatewayURL(
		strings.TrimSpace(os.Getenv("VOICE_CHAT_GATEWAY_URL")),
		strings.TrimSpace(os.Getenv("RENCROW_LLM_CHAT_WS")),
		chatBaseURL,
	)
}
