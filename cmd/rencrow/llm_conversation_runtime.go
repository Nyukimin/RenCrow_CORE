package main

import (
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	llmmiddleware "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/middleware"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/providers/ollama"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/providers/openai"
	modulellm "github.com/Nyukimin/RenCrow_CORE/modules/llm"
)

func buildConversationTextProvider(cfg *config.Config, providers primaryLLMProviders) (llm.LLMProvider, string) {
	plan := modulellm.BuildConversationTextProviderPlan(conversationRuntimeConfigFromAppConfig(cfg, providers.Worker != nil))
	if plan.Unavailable != "" {
		return nil, ""
	}
	if plan.UseWorker {
		return providers.Worker, plan.Description
	}
	summaryProvider := ollama.NewOllamaProviderWithNumCtx(plan.BaseURL, plan.Model, plan.NumCtx)
	return llmmiddleware.NewRawLogProvider(summaryProvider, plan.RawLogName), plan.Description
}

func buildConversationEmbedder(cfg *config.Config) (conversation.EmbeddingProvider, string) {
	plan := modulellm.BuildConversationEmbedderPlan(conversationRuntimeConfigFromAppConfig(cfg, false))
	if plan.Unavailable != "" {
		return nil, ""
	}
	switch plan.Provider {
	case modulellm.ConversationEmbedProviderOpenAI, modulellm.ConversationEmbedProviderLocalLLM:
		return openai.NewOpenAIEmbedderWithOptions(cfg.LocalLLM.APIKey, plan.Model, plan.BaseURL, plan.Timeout), plan.Description
	default:
		return ollama.NewOllamaEmbedder(plan.BaseURL, plan.Model), plan.Description
	}
}

func conversationRuntimeConfigFromAppConfig(cfg *config.Config, primaryWorkerReady bool) modulellm.ConversationRuntimeConfig {
	if cfg == nil {
		return modulellm.ConversationRuntimeConfig{PrimaryWorkerReady: primaryWorkerReady}
	}
	return modulellm.ConversationRuntimeConfig{
		LocalEnabled:       cfg.LocalLLM.Enabled,
		LocalProvider:      cfg.LocalLLM.Provider,
		LocalBaseURL:       cfg.LocalLLM.BaseURL,
		LocalTimeoutSec:    cfg.LocalLLM.TimeoutSec,
		PrimaryWorkerReady: primaryWorkerReady,
		OllamaBaseURL:      cfg.Ollama.BaseURL,
		OllamaModel:        cfg.Ollama.Model,
		SummaryModel:       cfg.Conversation.SummaryModel,
		EmbedProvider:      cfg.Conversation.EmbedProvider,
		EmbedBaseURL:       cfg.Conversation.EmbedBaseURL,
		EmbedModel:         cfg.Conversation.EmbedModel,
	}
}
