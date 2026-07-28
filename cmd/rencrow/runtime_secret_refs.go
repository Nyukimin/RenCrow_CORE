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

	if envName := strings.TrimSpace(cfg.LLMGateway.APIKeyEnv); envName != "" {
		addEnv("rencrow_llm", envName, "RenCrow_LLM Gateway API key")
	}
	add("external_api", "google_search_chat.api_key", "Google Search Chat API key", cfg.GoogleSearchChat.APIKey)
	add("external_api", "google_search_worker.api_key", "Google Search Worker API key", cfg.GoogleSearchWorker.APIKey)
	addEnv("llm_ops", "LLM_OPS_TOKEN", "LLM Ops proxy token")

	return refs
}
