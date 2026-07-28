package main

import (
	"os"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/providers/openai"
)

func buildConversationTextProvider(cfg *config.Config, providers primaryLLMProviders) (llm.LLMProvider, string) {
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
	return openai.NewOpenAIEmbedderWithOptions(
		apiKey,
		cfg.Conversation.EmbedModel,
		baseURL,
		timeout,
	), "RenCrow_LLM embedding alias: " + cfg.Conversation.EmbedModel
}
