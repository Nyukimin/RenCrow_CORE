package main

import (
	"log"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/modulebridge"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	llmmiddleware "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/middleware"
	modulellm "github.com/Nyukimin/picoclaw_multiLLM/modules/llm"
)

type llmRuntimeProviders struct {
	Primary            primaryLLMProviders
	Chat               llm.LLMProvider
	Worker             llm.LLMProvider
	ChatWorker         llm.LLMProvider
	Heavy              llm.LLMProvider
	Wild               llm.LLMProvider
	WorkerToolProvider llm.ToolCallingProvider
	Coder1             *coderAdapter
	Coder2             *coderAdapter
	Coder3             *coderAdapter
	Coder4             *coderAdapter
	ModuleProviders    map[string]modulellm.Provider
}

func buildLLMRuntimeProviders(cfg *config.Config, contextBudgetRecorder llmmiddleware.ContextBudgetRecorder, busyTracker *llmBusyTracker) llmRuntimeProviders {
	primaryProviders := buildPrimaryLLMProviders(cfg, contextBudgetRecorder)
	primaryProviders = primaryLLMProviders{
		Chat:       trackLLMProvider("chat", primaryProviders.Chat, busyTracker),
		Worker:     trackLLMProvider("worker", primaryProviders.Worker, busyTracker),
		ChatWorker: trackLLMProvider("chatworker", primaryProviders.ChatWorker, busyTracker),
		Heavy:      trackLLMProvider("heavy", primaryProviders.Heavy, busyTracker),
		Wild:       trackLLMProvider("wild", primaryProviders.Wild, busyTracker),
	}
	workerToolProvider, ok := primaryProviders.Worker.(llm.ToolCallingProvider)
	if !ok {
		log.Fatalf("worker provider %s does not support tool calling", primaryProviders.Worker.Name())
	}
	coder1Adapter, coder2Adapter, coder3Adapter, coder4Adapter := setupCoders(cfg, busyTracker)
	moduleProviders := modulebridge.NewLLMRoleProviders(primaryProviders.Chat, primaryProviders.Worker, primaryProviders.Heavy, primaryProviders.Wild)
	return llmRuntimeProviders{
		Primary:            primaryProviders,
		Chat:               primaryProviders.Chat,
		Worker:             primaryProviders.Worker,
		ChatWorker:         primaryProviders.ChatWorker,
		Heavy:              primaryProviders.Heavy,
		Wild:               primaryProviders.Wild,
		WorkerToolProvider: workerToolProvider,
		Coder1:             coder1Adapter,
		Coder2:             coder2Adapter,
		Coder3:             coder3Adapter,
		Coder4:             coder4Adapter,
		ModuleProviders:    wrapModuleLLMProvidersWithHealthChecks(cfg, moduleProviders),
	}
}

func selectChatConversationProvider(chatWorkerProvider, chatProvider llm.LLMProvider) llm.LLMProvider {
	return firstNonNilLLMProvider(chatProvider, chatWorkerProvider)
}
