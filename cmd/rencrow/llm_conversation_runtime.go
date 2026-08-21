package main

import (
	"os"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/providers/rencrowllm"
)

// buildConversationTextProvider resolves the execution role that owns
// conversation summarization and async ProfilePromotion extraction.
// conversation.summary_model selects the role; an empty or unknown value keeps
// the historical worker role so existing deployments do not change behavior.
// Extraction is a bounded JSON-contract task, so a non-reasoning role such as
// chat finishes it without spending the worker reasoning budget.
func buildConversationTextProvider(cfg *config.Config, providers primaryLLMProviders) (llm.LLMProvider, string) {
	selected := ""
	if cfg != nil {
		selected = strings.ToLower(strings.TrimSpace(cfg.Conversation.SummaryModel))
	}
	switch selected {
	case "chat":
		if providers.Chat != nil {
			return providers.Chat, "RenCrow_LLM chat"
		}
	case "chatworker":
		if providers.ChatWorker != nil {
			return providers.ChatWorker, "RenCrow_LLM chatworker"
		}
	case "wild":
		if providers.Wild != nil {
			return providers.Wild, "RenCrow_LLM wild"
		}
	}
	if providers.Worker == nil {
		return nil, ""
	}
	return providers.Worker, "RenCrow_LLM worker"
}

func buildConversationEmbedder(cfg *config.Config) (conversation.EmbeddingProvider, string) {
	if cfg == nil || strings.TrimSpace(cfg.Conversation.EmbedModel) == "" {
		return nil, ""
	}
	apiKey := ""
	if envName := strings.TrimSpace(cfg.LLMGateway.APIKeyEnv); envName != "" {
		apiKey = strings.TrimSpace(os.Getenv(envName))
	}
	timeout := time.Duration(cfg.LLMGateway.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.LLMGateway.BaseURL), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8090"
	}
	return rencrowllm.NewGatewayEmbedderWithOptions(
		apiKey,
		cfg.Conversation.EmbedModel,
		baseURL,
		timeout,
	), "RenCrow_LLM embedding alias: " + cfg.Conversation.EmbedModel
}
