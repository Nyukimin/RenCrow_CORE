package main

import (
	"os"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
)

func buildSecretRefsFromConfig(cfg *config.Config) []viewer.SecretRefRuntimeConfig {
	if cfg == nil {
		return nil
	}
	refs := make([]viewer.SecretRefRuntimeConfig, 0, 24)
	add := func(scope, path, label, value string) {
		refs = append(refs, viewer.SecretRefRuntimeConfig{
			Ref:        "config:" + path,
			Label:      label,
			Scope:      scope,
			Configured: strings.TrimSpace(value) != "",
		})
	}
	addEnv := func(scope, envName, label string) {
		refs = append(refs, viewer.SecretRefRuntimeConfig{
			Ref:        "env:" + envName,
			Label:      label,
			Scope:      scope,
			Configured: strings.TrimSpace(os.Getenv(envName)) != "",
		})
	}

	add("local_llm", "local_llm.api_key", "Local LLM API key", cfg.LocalLLM.APIKey)
	add("tool", "webwright_fetch.api_key", "Webwright Fetch local API key", cfg.WebwrightFetch.APIKey)
	add("provider", "claude.api_key", "Claude provider API key", cfg.Claude.APIKey)
	add("provider", "deepseek.api_key", "DeepSeek provider API key", cfg.DeepSeek.APIKey)
	add("provider", "openai.api_key", "OpenAI provider API key", cfg.OpenAI.APIKey)
	add("external_api", "google_search_chat.api_key", "Google Search Chat API key", cfg.GoogleSearchChat.APIKey)
	add("external_api", "google_search_worker.api_key", "Google Search Worker API key", cfg.GoogleSearchWorker.APIKey)
	add("provider", "coder1.api_key", "Coder1 provider API key", cfg.Coder1.APIKey)
	add("provider", "coder2.api_key", "Coder2 provider API key", cfg.Coder2.APIKey)
	add("provider", "coder3.api_key", "Coder3 provider API key", cfg.Coder3.APIKey)
	add("provider", "coder4.api_key", "Coder4 provider API key", cfg.Coder4.APIKey)
	add("tts", "tts.azure.api_key", "Azure TTS API key", cfg.TTS.Azure.APIKey)
	add("tts", "tts.eleven.api_key", "ElevenLabs TTS API key", cfg.TTS.Eleven.APIKey)
	addEnv("local_llm", "LLM_OPS_TOKEN", "LLM Ops proxy token")

	return refs
}
