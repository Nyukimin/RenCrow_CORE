package main

import (
	"context"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	llmmiddleware "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/middleware"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/providers/ollama"
	modulellm "github.com/Nyukimin/RenCrow_CORE/modules/llm"
)

type primaryLLMProviders struct {
	Chat       llm.LLMProvider
	Worker     llm.LLMProvider
	ChatWorker llm.LLMProvider
	Heavy      llm.LLMProvider
	Wild       llm.LLMProvider
}

const (
	localLLMDefaultTimeout = modulellm.LocalDefaultTimeout
)

func buildPrimaryLLMProviders(cfg *config.Config, contextBudgetRecorder llmmiddleware.ContextBudgetRecorder) primaryLLMProviders {
	plan := modulellm.BuildPrimaryProviderPlan(primaryRuntimeConfigFromAppConfig(cfg))
	if plan.Mode == modulellm.PrimaryModeLocal {
		global := make(chan struct{}, cfg.LocalLLM.GlobalConcurrency)
		localCfg := localRuntimeConfigFromAppConfig(cfg)
		chatTimeout := modulellm.LocalTimeoutForAlias(localCfg, "Chat")
		workerTimeout := modulellm.LocalTimeoutForAlias(localCfg, "Worker")
		heavyTimeout := modulellm.LocalTimeoutForAlias(localCfg, "Heavy")
		wildTimeout := modulellm.LocalTimeoutForAlias(localCfg, "Wild")
		chat := buildLocalAliasProvider(cfg, "Chat", cfg.LocalLLM.ChatModel, chatTimeout, global)
		worker := buildLocalAliasProvider(cfg, "Worker", cfg.LocalLLM.WorkerModel, workerTimeout, global)
		chatWorker := buildLocalAliasProvider(cfg, "ChatWorker", cfg.LocalLLM.ChatWorkerModel, workerTimeout, global)
		heavy := buildLocalAliasProvider(cfg, "Heavy", modulellm.LocalModelForAlias(localCfg, "Heavy"), heavyTimeout, global)
		wild := buildLocalAliasProvider(cfg, "Wild", cfg.LocalLLM.WildModel, wildTimeout, global)
		if cfg.LocalLLMWarmupEnabled() {
			go warmPrimaryLLMProviders(context.Background(), map[string]llm.LLMProvider{
				"Chat":       chat,
				"Worker":     worker,
				"ChatWorker": chatWorker,
				"Heavy":      heavy,
				"Wild":       wild,
			}, maxDuration(chatTimeout, workerTimeout, heavyTimeout, wildTimeout))
		}
		return primaryLLMProviders{
			Chat:       wrapPrimaryLLMProvider(cfg, "chat", chat, contextBudgetRecorder),
			Worker:     wrapPrimaryLLMProvider(cfg, "worker", worker, contextBudgetRecorder),
			ChatWorker: wrapPrimaryLLMProvider(cfg, "chatworker", chatWorker, contextBudgetRecorder),
			Heavy:      wrapPrimaryLLMProvider(cfg, "heavy", heavy, contextBudgetRecorder),
			Wild:       wrapPrimaryLLMProvider(cfg, "wild", wild, contextBudgetRecorder),
		}
	}

	chatRole := plan.Roles[modulellm.PrimaryRoleChat]
	workerRole := plan.Roles[modulellm.PrimaryRoleWorker]
	chatRawProvider := ollama.NewOllamaProviderWithNumCtx(chatRole.BaseURL, chatRole.Model, chatRole.NumCtx)
	workerRawProvider := ollama.NewOllamaProviderWithNumCtx(workerRole.BaseURL, workerRole.Model, workerRole.NumCtx)
	return primaryLLMProviders{
		Chat:       wrapPrimaryLLMProvider(cfg, "chat", chatRawProvider, contextBudgetRecorder),
		Worker:     wrapPrimaryLLMProvider(cfg, "worker", workerRawProvider, contextBudgetRecorder),
		ChatWorker: wrapPrimaryLLMProvider(cfg, "chatworker", workerRawProvider, contextBudgetRecorder),
		Heavy:      wrapPrimaryLLMProvider(cfg, "heavy", workerRawProvider, contextBudgetRecorder),
		Wild:       wrapPrimaryLLMProvider(cfg, "wild", workerRawProvider, contextBudgetRecorder),
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

func localRuntimeConfigFromAppConfig(cfg *config.Config) modulellm.LocalRuntimeConfig {
	if cfg == nil {
		return modulellm.LocalRuntimeConfig{}
	}
	return modulellm.LocalRuntimeConfig{
		Provider:          cfg.LocalLLM.Provider,
		BaseURL:           cfg.LocalLLM.BaseURL,
		ChatBaseURL:       cfg.LocalLLM.ChatBaseURL,
		WorkerBaseURL:     cfg.LocalLLM.WorkerBaseURL,
		ChatWorkerBaseURL: cfg.LocalLLM.ChatWorkerBaseURL,
		HeavyBaseURL:      cfg.LocalLLM.HeavyBaseURL,
		WildBaseURL:       cfg.LocalLLM.WildBaseURL,
		ChatModel:         cfg.LocalLLM.ChatModel,
		WorkerModel:       cfg.LocalLLM.WorkerModel,
		ChatWorkerModel:   cfg.LocalLLM.ChatWorkerModel,
		HeavyModel:        cfg.LocalLLM.HeavyModel,
		WildModel:         cfg.LocalLLM.WildModel,
		TimeoutSec:        cfg.LocalLLM.TimeoutSec,
		ChatTimeoutSec:    cfg.LocalLLM.ChatTimeoutSec,
		ModelConcurrency:  cfg.LocalLLM.ModelConcurrency,
		ModelContext:      cfg.LocalLLM.ModelContext,
		ChatModelContext:  cfg.LocalLLM.ChatModelContext,
	}
}

func primaryRuntimeConfigFromAppConfig(cfg *config.Config) modulellm.PrimaryRuntimeConfig {
	if cfg == nil {
		return modulellm.PrimaryRuntimeConfig{}
	}
	return modulellm.PrimaryRuntimeConfig{
		LocalEnabled: cfg.LocalLLM.Enabled,
		Local:        localRuntimeConfigFromAppConfig(cfg),
		LegacyOllama: modulellm.LegacyOllamaRuntimeConfig{
			BaseURL:     cfg.Ollama.BaseURL,
			ChatModel:   cfg.Ollama.Model,
			WorkerModel: cfg.Ollama.Model,
		},
	}
}
