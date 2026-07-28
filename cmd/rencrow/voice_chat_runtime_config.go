package main

import (
	"os"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
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
	return modulevoicechat.InferGatewayURL(
		strings.TrimSpace(os.Getenv("VOICE_CHAT_GATEWAY_URL")),
		strings.TrimSpace(os.Getenv("RENCROW_LLM_CHAT_WS")),
		"",
	)
}

func voiceChatInputAudioSettingsFromConfig(cfg *config.Config) voiceChatInputAudioSettings {
	if cfg == nil {
		return voiceChatInputAudioSettings{}
	}
	apiKey := ""
	if envName := strings.TrimSpace(cfg.LLMGateway.APIKeyEnv); envName != "" {
		apiKey = strings.TrimSpace(os.Getenv(envName))
	}
	return voiceChatInputAudioSettings{
		Model:          "mio",
		APIKey:         apiKey,
		Timeout:        time.Duration(cfg.LLMGateway.TimeoutSec) * time.Second,
		ModelContext:   0,
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
