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

func voiceChatInputAudioSettingsFromConfig(cfg *config.Config) voiceChatInputAudioSettings {
	if cfg == nil {
		return voiceChatInputAudioSettings{}
	}
	local := localRuntimeConfigFromAppConfig(cfg)
	return voiceChatInputAudioSettings{
		Model:          modulellm.LocalModelForAlias(local, modulellm.RoleChat),
		APIKey:         cfg.LocalLLM.APIKey,
		Timeout:        modulellm.LocalTimeoutForAlias(local, modulellm.RoleChat),
		ModelContext:   modulellm.LocalModelContextForAlias(local, modulellm.RoleChat),
		Stream:         cfg.Mio.Generation.Stream,
		MaxTokens:      cfg.Mio.Generation.MaxTokens,
		Temperature:    cfg.Mio.Generation.Temperature,
		TopP:           cfg.Mio.Generation.TopP,
		TopK:           cfg.Mio.Generation.TopK,
		MinP:           cfg.Mio.Generation.MinP,
		Seed:           cfg.Mio.Generation.Seed,
		EnableThinking: cfg.Mio.Generation.ChatTemplateKwargs.EnableThinking,
		Prompt:         cfg.Mio.InputAudio.Prompt,
	}
}
