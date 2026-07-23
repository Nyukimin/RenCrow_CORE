package llm

import (
	"fmt"
	"strings"
	"time"
)

const (
	ConversationTextProviderPrimaryWorker = "primary_worker"
	ConversationTextProviderOllamaSummary = "ollama_summary"

	ConversationEmbedProviderOllama   = LocalProviderOllama
	ConversationEmbedProviderOpenAI   = LocalProviderOpenAI
	ConversationEmbedProviderLocalLLM = "local_llm"
)

type ConversationRuntimeConfig struct {
	LocalEnabled       bool
	LocalProvider      string
	LocalBaseURL       string
	LocalTimeoutSec    int
	PrimaryWorkerReady bool

	OllamaBaseURL string
	OllamaModel   string

	SummaryModel string

	EmbedProvider string
	EmbedBaseURL  string
	EmbedModel    string
}

type ConversationTextProviderPlan struct {
	Provider    string
	UseWorker   bool
	BaseURL     string
	Model       string
	NumCtx      int
	RawLogName  string
	Description string
	Unavailable string
}

type ConversationEmbedderPlan struct {
	Provider    string
	BaseURL     string
	Model       string
	Timeout     time.Duration
	Description string
	Unavailable string
}

func BuildConversationTextProviderPlan(cfg ConversationRuntimeConfig) ConversationTextProviderPlan {
	if cfg.PrimaryWorkerReady {
		return ConversationTextProviderPlan{
			Provider:    ConversationTextProviderPrimaryWorker,
			UseWorker:   true,
			Description: "primary Worker",
		}
	}
	model := FirstNonEmpty(cfg.SummaryModel, cfg.OllamaModel)
	if model == "" {
		return ConversationTextProviderPlan{Unavailable: "conversation summary model is not configured"}
	}
	baseURL := strings.TrimSpace(cfg.OllamaBaseURL)
	return ConversationTextProviderPlan{
		Provider:    ConversationTextProviderOllamaSummary,
		BaseURL:     baseURL,
		Model:       model,
		NumCtx:      32768,
		RawLogName:  "conversation-summary",
		Description: fmt.Sprintf("%s (model: %s)", baseURL, model),
	}
}

func BuildConversationEmbedderPlan(cfg ConversationRuntimeConfig) ConversationEmbedderPlan {
	model := strings.TrimSpace(cfg.EmbedModel)
	if model == "" {
		return ConversationEmbedderPlan{Unavailable: "conversation embedding model is not configured"}
	}
	embedProvider := strings.ToLower(strings.TrimSpace(cfg.EmbedProvider))
	embedBaseURL := strings.TrimSpace(cfg.EmbedBaseURL)
	timeout := time.Duration(cfg.LocalTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = LocalDefaultTimeout
	}

	if embedProvider == ConversationEmbedProviderOllama {
		baseURL := FirstNonEmpty(embedBaseURL, cfg.OllamaBaseURL)
		return ConversationEmbedderPlan{
			Provider:    ConversationEmbedProviderOllama,
			BaseURL:     baseURL,
			Model:       model,
			Description: fmt.Sprintf("conversation embedding ollama: %s (model: %s)", baseURL, model),
		}
	}
	if embedProvider == ConversationEmbedProviderOpenAI {
		baseURL := FirstNonEmpty(embedBaseURL, cfg.LocalBaseURL)
		return ConversationEmbedderPlan{
			Provider:    ConversationEmbedProviderOpenAI,
			BaseURL:     baseURL,
			Model:       model,
			Timeout:     timeout,
			Description: fmt.Sprintf("conversation embedding openai: %s (model: %s)", baseURL, model),
		}
	}
	if cfg.LocalEnabled && NormalizeLocalProvider(cfg.LocalProvider) != LocalProviderOllama {
		baseURL := strings.TrimSpace(cfg.LocalBaseURL)
		return ConversationEmbedderPlan{
			Provider:    ConversationEmbedProviderLocalLLM,
			BaseURL:     baseURL,
			Model:       model,
			Timeout:     timeout,
			Description: fmt.Sprintf("local_llm embedding: %s (model: %s)", baseURL, model),
		}
	}

	baseURL := strings.TrimSpace(cfg.OllamaBaseURL)
	if cfg.LocalEnabled && NormalizeLocalProvider(cfg.LocalProvider) == LocalProviderOllama {
		baseURL = strings.TrimSpace(cfg.LocalBaseURL)
	}
	return ConversationEmbedderPlan{
		Provider:    ConversationEmbedProviderOllama,
		BaseURL:     baseURL,
		Model:       model,
		Description: fmt.Sprintf("%s (model: %s)", baseURL, model),
	}
}
