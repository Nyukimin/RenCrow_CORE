package main

import (
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	llmmiddleware "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/middleware"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/providers/ollama"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/providers/openai"
	modulellm "github.com/Nyukimin/RenCrow_CORE/modules/llm"
)

func buildLocalAliasProvider(cfg *config.Config, alias, model string, timeout time.Duration, global chan struct{}) llm.LLMProvider {
	localCfg := localRuntimeConfigFromAppConfig(cfg)
	aliasConfig := modulellm.LocalAliasConfig{
		Alias:        strings.TrimSpace(alias),
		Provider:     modulellm.NormalizeLocalProvider(localLLMProviderFromConfig(cfg)),
		BaseURL:      modulellm.LocalBaseURLForAlias(localCfg, alias),
		Model:        modulellm.LocalModelForAlias(localCfg, alias),
		Timeout:      modulellm.LocalTimeoutForAlias(localCfg, alias),
		QueueTimeout: modulellm.LocalQueueTimeoutForAlias(alias),
		QueuePolicy:  modulellm.LocalQueuePolicyWait,
		Concurrency:  localLLMConcurrencyFromConfig(cfg),
		NumCtx:       modulellm.LocalOllamaNumCtxForAlias(alias),
	}
	if model != "" {
		aliasConfig.Model = model
	}
	if timeout > 0 {
		aliasConfig.Timeout = timeout
	}
	return buildLocalAliasProviderFromConfig(cfg, aliasConfig, global)
}

func buildLocalAliasProviderFromConfig(cfg *config.Config, aliasConfig modulellm.LocalAliasConfig, global chan struct{}) llm.LLMProvider {
	var raw llm.LLMProvider
	switch aliasConfig.Provider {
	case modulellm.LocalProviderOllama:
		raw = ollama.NewOllamaProviderWithNumCtx(aliasConfig.BaseURL, aliasConfig.Model, aliasConfig.NumCtx)
	default:
		apiKey := ""
		if cfg != nil {
			apiKey = cfg.LocalLLM.APIKey
		}
		raw = openai.NewOpenAIProviderWithModelContext(apiKey, aliasConfig.Model, aliasConfig.BaseURL, aliasConfig.Timeout, aliasConfig.NumCtx)
	}
	modelSem := make(chan struct{}, aliasConfig.Concurrency)
	return llmmiddleware.NewLimitedProviderWithOptions(raw, "local-"+aliasConfig.Alias+"-"+aliasConfig.Model, global, modelSem, llmmiddleware.LimitedProviderOptions{
		Alias:             aliasConfig.Alias,
		QueueTimeout:      aliasConfig.QueueTimeout,
		GenerationTimeout: aliasConfig.Timeout,
		QueuePolicy:       aliasConfig.QueuePolicy,
	})
}

func firstNonEmpty(values ...string) string {
	return modulellm.FirstNonEmpty(values...)
}

func firstNonNilLLMProvider(values ...llm.LLMProvider) llm.LLMProvider {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

func maxDuration(values ...time.Duration) time.Duration {
	return modulellm.MaxDuration(values...)
}

func localLLMProviderFromConfig(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.LocalLLM.Provider
}

func localLLMConcurrencyFromConfig(cfg *config.Config) int {
	if cfg == nil {
		return 0
	}
	return cfg.LocalLLM.ModelConcurrency
}
