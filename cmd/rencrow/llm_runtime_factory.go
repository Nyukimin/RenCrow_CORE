package main

import (
	"log"
	"os"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	llmmiddleware "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/middleware"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/providers/rencrowllm"
)

type primaryLLMProviders struct {
	Chat       llm.LLMProvider
	Worker     llm.LLMProvider
	ChatWorker llm.LLMProvider
	Heavy      llm.LLMProvider
	Wild       llm.LLMProvider
}

func buildPrimaryLLMProviders(cfg *config.Config, contextBudgetRecorder llmmiddleware.ContextBudgetRecorder) primaryLLMProviders {
	return buildGatewayPrimaryLLMProviders(cfg, contextBudgetRecorder)
}

func buildGatewayPrimaryLLMProviders(cfg *config.Config, recorder llmmiddleware.ContextBudgetRecorder) primaryLLMProviders {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.LLMGateway.BaseURL), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8090"
	}
	timeout := time.Duration(cfg.LLMGateway.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	apiKey := ""
	if envName := strings.TrimSpace(cfg.LLMGateway.APIKeyEnv); envName != "" {
		apiKey = strings.TrimSpace(os.Getenv(envName))
	}
	provider := func(role, alias, agentID string) llm.LLMProvider {
		raw := rencrowllm.NewGatewayProviderWithOptions(apiKey, alias, baseURL, timeout).
			WithRenCrowExecution(agentID, role, alias)
		return wrapPrimaryLLMProvider(cfg, role, enforceThinkingPolicyForRole(role, raw), recorder)
	}
	log.Printf("RenCrow_LLM Gateway enabled (base_url=%s)", baseURL)
	return primaryLLMProviders{
		Chat:       provider("chat", "mio", "mio"),
		Worker:     provider("worker", "worker", "shiro"),
		ChatWorker: provider("chatworker", "shiro", "shiro"),
		Heavy:      provider("heavy", "kuro", "kuro"),
		Wild:       provider("wild", "midori", "midori"),
	}
}

func enforceThinkingPolicyForRole(role string, provider llm.LLMProvider) llm.LLMProvider {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "chat", "wild":
		return llmmiddleware.NewNoThinkingProvider(provider)
	case "chatworker":
		return llmmiddleware.NewLowThinkingProvider(provider)
	default:
		return provider
	}
}

func wrapPrimaryLLMProvider(cfg *config.Config, name string, provider llm.LLMProvider, contextBudgetRecorder llmmiddleware.ContextBudgetRecorder) llm.LLMProvider {
	policy := domainai.ContextBudgetPolicy{}
	if cfg != nil {
		policy = domainai.ContextBudgetPolicy{
			MaxContextTokens: cfg.AIWorkflow.ContextBudgetTokens,
			WarnAtRatio:      cfg.AIWorkflow.ContextBudgetWarnRatio,
			StopAtRatio:      cfg.AIWorkflow.ContextBudgetStopRatio,
		}
	}
	budgeted := llmmiddleware.NewContextBudgetProvider(provider, name, policy, contextBudgetRecorder)
	return llmmiddleware.NewRawLogProvider(llmmiddleware.NewDateTimeProvider(budgeted), name)
}
