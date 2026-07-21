package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	idlechatfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/idlechat"
	llmfactory "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/factory"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

var idleChatTTSPrefetch *idleChatTTSPrefetchManager

func buildIdleChatRuntime(
	cfg *config.Config,
	deps *Dependencies,
	chatProvider llm.LLMProvider,
	workerProvider llm.LLMProvider,
	chatWorkerProvider llm.LLMProvider,
	heavyProvider llm.LLMProvider,
	wildProvider llm.LLMProvider,
	centralMemory *domainsession.CentralMemory,
	coder2Adapter *coderAdapter,
	recentGlossaryTopics func(context.Context, int) ([]string, error),
	dailySourceBriefResearch idlechat.DailySourceBriefResearch,
	ttsBridge orchestrator.TTSBridge,
) {
	if !cfg.IdleChat.Enabled {
		return
	}
	idleChatOrch := idlechat.NewIdleChatOrchestrator(
		chatProvider,
		centralMemory,
		cfg.IdleChat.Participants,
		cfg.IdleChat.IntervalMin,
		cfg.IdleChat.MaxTurns,
		cfg.IdleChat.Temperature,
		config.BuildIdleChatAgentPrompts(cfg.Prompts),
		cfg.IdleChat.StoryDataDir,
	)
	idleChatOrch.SetIntervalSeconds(cfg.IdleChat.IntervalSec)
	if deps.llmBusyTracker != nil {
		idleChatOrch.SetExternalLLMBusyFunc(deps.llmBusyTracker.ExternalBusy)
	}
	chatWorkerAliasProvider := chatWorkerProvider
	if chatWorkerAliasProvider == nil && workerProvider != nil {
		chatWorkerAliasProvider = namedLLMProvider{name: "ChatWorker", inner: workerProvider}
	}
	idleChatOrch.SetSpeakerProviders(map[string]llm.LLMProvider{
		"mio":        chatProvider,
		"shiro":      firstNonNilLLMProvider(chatWorkerAliasProvider, workerProvider),
		"worker":     workerProvider,
		"chatworker": chatWorkerAliasProvider,
		"kuro":       heavyProvider,
		"wild":       wildProvider,
	})
	idleChatOrch.SetSpeakerProviderOptions(idleChatProviderOptionsFromConfig(cfg.IdleChat.SpeakerLLMOptions))
	idleChatOrch.SetNewsSourceConfig(idleChatNewsSourceConfigFromRuntime(cfg.IdleChat.NewsSources))
	idleChatOrch.SetDailySourceBriefResearch(dailySourceBriefResearch)
	idleChatOrch.SetTopicGenerationConfig(idleChatTopicGenerationConfigFromRuntime(cfg.IdleChat.TopicGeneration))
	idleChatOrch.SetDialogueInterestingnessConfig(idleChatDialogueInterestingnessConfigFromRuntime(cfg.IdleChat.DialogueInterestingness))
	if topicProvider, label := selectForecastTopicProvider(workerProvider); topicProvider != nil {
		idleChatOrch.SetForecastTopicProviderWithLabel(topicProvider, label)
		idleChatOrch.InitForecastTopicStock(filepath.Join(cfg.Session.StorageDir, "forecast_topic_stock.json"))
		log.Printf("IdleChat: Forecast topic generator set to %s, topic stock bootstrap/idle/heartbeat refill enabled", label)
	}
	if forecastProvider, label := selectForecastProviderForRuntime(cfg, workerProvider); forecastProvider != nil {
		idleChatOrch.SetForecastProviderWithLabel(forecastProvider, label)
		log.Printf("IdleChat: Forecast session provider set to %s", forecastProviderLogLabel(label))
	}
	if recentGlossaryTopics != nil {
		idleChatOrch.SetRecentTopicProvider(recentGlossaryTopics)
		log.Printf("IdleChat: Glossary topics injected")
	}
	if deps.personaRuntimeStore != nil {
		idleChatOrch.SetPersonaRuntimeRecorder(deps.personaRuntimeStore, deps.personaTriggerDefinitions)
		idleChatOrch.SetPersonaCanonicalResponses(deps.personaCanonicalResponses)
		log.Printf("Persona runtime recorder integrated with IdleChat (%d trigger definitions, %d canonical responses)", len(deps.personaTriggerDefinitions), len(deps.personaCanonicalResponses))
	}
	topicStorePath := filepath.Join(cfg.Session.StorageDir, "idlechat_topics.jsonl")
	if err := idleChatOrch.SetTopicStore(topicStorePath); err != nil {
		log.Printf("WARN: idleChat topic store disabled: %v", err)
	} else {
		log.Printf("IdleChat topic store enabled: %s", topicStorePath)
	}
	if deps.eventHub != nil {
		idleChatTTSPrefetch = newIdleChatTTSPrefetchManager(ttsBridge)
		idleChatOrch.SetTTSPrefetchEmitter(func(ev idlechat.TTSPrefetchEvent) {
			if idleChatTTSPrefetch != nil {
				idleChatTTSPrefetch.Push(ev)
			}
		})
		idleChatOrch.SetEventEmitter(func(ev idlechat.TimelineEvent) <-chan struct{} {
			if ev.Type != "idlechat.tts" {
				viewerType := ev.Type
				if viewerType == "idlechat.viewer" {
					viewerType = "idlechat.message"
				}
				chatID := strings.TrimSpace(ev.SessionID)
				if chatID == "" {
					chatID = "idlechat"
				}
				viewerEvent := orchestrator.NewEvent(
					viewerType,
					ev.From,
					ev.To,
					ev.Content,
					"IDLECHAT",
					"",
					ev.SessionID,
					"idlechat",
					chatID,
				)
				viewerEvent.RawContent = ev.RawContent
				viewerEvent.MessageID = ev.MessageID
				viewerEvent.TurnIndex = ev.TurnIndex
				viewerEvent.Category = string(ev.Category)
				viewerEvent.Strategy = string(ev.Strategy)
				deps.eventHub.OnEvent(viewerEvent)
			}
			if ev.Type == "idlechat.viewer" {
				return nil
			}
			if ev.Type == "idlechat.message" && idleChatTTSPrefetch != nil && idleChatTTSPrefetch.HasActive(ev.SessionID, ev.MessageID) {
				if waitCh, ok := idleChatTTSPrefetch.Close(ev); ok {
					return waitCh
				}
				return nil
			}
			return emitIdleChatTTSAsync(ttsBridge, ev)
		})
	}
	idleChatOrch.SetTTSTimeoutReporter(func(ev idlechat.TTSTimeoutEvent) {
		markIdleChatTTSTimeout(ev)
	})
	if deps.eventRelay != nil {
		deps.eventRelay.SetIdleChat(idleChatOrch)
	}
	if err := idlechatfeature.StartBackground(context.Background(), idlechatfeature.Dependencies{Background: idleChatOrch}); err != nil {
		log.Printf("WARN: idlechat background start failed: %v", err)
	}
	deps.idleChatOrch = idleChatOrch
	log.Printf("IdleChat enabled (participants=%v)", cfg.IdleChat.Participants)
}

func idleChatNewsSourceConfigFromRuntime(cfg config.IdleChatNewsSourcesConfig) idlechat.NewsSourceConfig {
	redditEnabled := cfg.Reddit.Enabled != nil && *cfg.Reddit.Enabled
	xQueries := make([]idlechat.XNewsQuery, 0, len(cfg.X.Queries))
	for _, query := range cfg.X.Queries {
		xQueries = append(xQueries, idlechat.XNewsQuery{
			Name:     query.Name,
			Category: query.Category,
			Query:    query.Query,
			Limit:    query.Limit,
		})
	}
	return idlechat.NewsSourceConfig{
		RedditEnabled:     redditEnabled,
		RedditCommunities: append([]string(nil), cfg.Reddit.Communities...),
		RedditLimit:       cfg.Reddit.Limit,
		XEnabled:          cfg.X.Enabled,
		XBearerToken:      os.Getenv(strings.TrimSpace(cfg.X.BearerTokenEnv)),
		XQueries:          xQueries,
	}
}

func selectForecastProvider(cfg *config.Config, chatProvider, workerProvider, wildProvider llm.LLMProvider) (llm.LLMProvider, string) {
	return selectForecastProviderForRuntime(cfg, workerProvider)
}

func selectForecastProviders(cfg *config.Config) (llm.LLMProvider, string) {
	if cfg == nil {
		return nil, ""
	}
	plans := modulechat.BuildForecastProviderPlans(forecastCoderCandidatesFromRuntime(cfg), cfg.IdleChat.ForecastExternalEnabled)
	for _, plan := range plans {
		cc := forecastCoderConfigByLabel(cfg, plan.Label)
		if !plan.Allowed {
			log.Printf("IdleChat forecast provider skipped: %s provider=%s model=%s: %s", plan.Label, plan.Coder.Provider, plan.Coder.Model, plan.SkipReason)
			continue
		}
		provider, label := createForecastProvider(plan.Label, cc)
		if provider == nil {
			continue
		}
		return provider, label
	}
	return nil, ""
}

func selectForecastProviderForRuntime(cfg *config.Config, workerProvider llm.LLMProvider) (llm.LLMProvider, string) {
	provider, label := selectForecastProviders(cfg)
	if provider != nil {
		return provider, label
	}
	if workerProvider != nil {
		return workerProvider, modulechat.ForecastWorkerFallbackLabel
	}
	return nil, ""
}

func selectForecastTopicProvider(workerProvider llm.LLMProvider) (llm.LLMProvider, string) {
	if workerProvider == nil {
		return nil, ""
	}
	return workerProvider, modulechat.ForecastTopicGeneratorAgent
}

func coderProviderIsExternal(cc config.CoderConfig) bool {
	return modulechat.CoderProviderIsExternal(cc.Provider)
}

func createForecastProvider(label string, cc config.CoderConfig) (llm.LLMProvider, string) {
	if !cc.Enabled {
		return nil, ""
	}
	provider, err := llmfactory.CreateProvider(cc)
	if err != nil {
		log.Printf("WARN: IdleChat forecast provider skipped: %s provider=%s model=%s: %v", label, cc.Provider, cc.Model, err)
		return nil, ""
	}
	if provider == nil {
		return nil, ""
	}
	return provider, modulechat.BuildForecastProviderLabel(label, idleChatCoderProviderConfigFromRuntime(cc))
}

func forecastProviderLogLabel(label string) string {
	return modulechat.ForecastProviderLogLabel(label)
}

func forecastProviderModelLabel(model string) string {
	return modulechat.ForecastProviderModelLabel(model)
}

func idleChatProviderOptionsFromConfig(options map[string]config.IdleChatLLMOptions) map[string]map[string]any {
	return modulechat.IdleChatProviderOptions(idleChatProviderOptionsConfigFromRuntime(options))
}

func idleChatCoderProviderConfigFromRuntime(cc config.CoderConfig) modulechat.IdleChatCoderProviderConfig {
	return modulechat.IdleChatCoderProviderConfig{
		Enabled:  cc.Enabled,
		Provider: cc.Provider,
		Model:    cc.Model,
	}
}

func forecastCoderCandidatesFromRuntime(cfg *config.Config) []modulechat.ForecastCoderCandidate {
	if cfg == nil {
		return nil
	}
	return []modulechat.ForecastCoderCandidate{
		{Label: "Coder1", Coder: idleChatCoderProviderConfigFromRuntime(cfg.Coder1)},
		{Label: "Coder2", Coder: idleChatCoderProviderConfigFromRuntime(cfg.Coder2)},
		{Label: "Coder3", Coder: idleChatCoderProviderConfigFromRuntime(cfg.Coder3)},
		{Label: "Coder4", Coder: idleChatCoderProviderConfigFromRuntime(cfg.Coder4)},
	}
}

func forecastCoderConfigByLabel(cfg *config.Config, label string) config.CoderConfig {
	if cfg == nil {
		return config.CoderConfig{}
	}
	switch modulechat.ForecastCoderLabelIndex(label) {
	case 0:
		return cfg.Coder1
	case 1:
		return cfg.Coder2
	case 2:
		return cfg.Coder3
	case 3:
		return cfg.Coder4
	default:
		return config.CoderConfig{}
	}
}

func idleChatProviderOptionsConfigFromRuntime(options map[string]config.IdleChatLLMOptions) map[string]modulechat.IdleChatLLMOptions {
	out := make(map[string]modulechat.IdleChatLLMOptions, len(options))
	for name, opts := range options {
		out[name] = modulechat.IdleChatLLMOptions{Think: opts.Think}
	}
	return out
}

func idleChatTopicGenerationConfigFromRuntime(cfg config.IdleChatTopicGenerationConfig) idlechat.TopicGenerationConfig {
	return idlechat.TopicGenerationConfig{
		Enabled:              cfg.Enabled,
		CandidatesPerAttempt: cfg.CandidatesPerAttempt,
		MaxAttempts:          cfg.MaxAttempts,
		JudgeEnabled:         cfg.JudgeEnabled,
		MinJudgeTotal:        cfg.MinJudgeTotal,
		MinCategoryFit:       cfg.MinCategoryFit,
		MinSafety:            cfg.MinSafety,
		RecentTopicWindow:    cfg.RecentTopicWindow,
		RecentSimilarity:     cfg.RecentSimilarityThreshold,
		LogCandidates:        cfg.LogCandidates,
		LogJudgeScores:       cfg.LogJudgeScores,
		ProviderName:         "chatworker",
		PromptPaths: idlechat.TopicGenerationPromptPaths{
			Common:   cfg.Prompts.Common,
			Single:   cfg.Prompts.Single,
			Double:   cfg.Prompts.Double,
			External: cfg.Prompts.External,
			Movie:    cfg.Prompts.Movie,
			News:     cfg.Prompts.News,
			Forecast: cfg.Prompts.Forecast,
			Story:    cfg.Prompts.Story,
			Judge:    cfg.Prompts.Judge,
		},
	}
}

func idleChatDialogueInterestingnessConfigFromRuntime(cfg config.IdleChatDialogueInterestingnessConfig) idlechat.DialogueInterestingnessConfig {
	return idlechat.DialogueInterestingnessConfig{
		Enabled:                   cfg.Enabled,
		MaxTurnsPerTopic:          cfg.MaxTurnsPerTopic,
		MinQualityScore:           cfg.MinQualityScore,
		MaxQualityRetries:         cfg.MaxQualityRetries,
		EnforcePreviousUptake:     cfg.EnforcePreviousUptake,
		EnforceOneNewContribution: cfg.EnforceOneNewContribution,
		EnforceCategoryAxis:       cfg.EnforceCategoryAxis,
		ForbidMetaLeak:            cfg.ForbidMetaLeak,
		ForbidUserQuestion:        cfg.ForbidUserQuestion,
		Utterance: idlechat.DialogueUtteranceConfig{
			MinRunes:              cfg.Utterance.MinRunes,
			MaxRunes:              cfg.Utterance.MaxRunes,
			PreferredMaxSentences: cfg.Utterance.PreferredMaxSentences,
		},
		PromptPaths: idlechat.DialoguePromptPaths{
			Common:   cfg.Prompts.Common,
			Single:   cfg.Prompts.Single,
			Double:   cfg.Prompts.Double,
			External: cfg.Prompts.External,
			Movie:    cfg.Prompts.Movie,
			News:     cfg.Prompts.News,
			Forecast: cfg.Prompts.Forecast,
			Story:    cfg.Prompts.Story,
		},
	}
}
