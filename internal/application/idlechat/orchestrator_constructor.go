package idlechat

import (
	"context"
	"math/rand"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

func NewIdleChatOrchestrator(
	llmProvider llm.LLMProvider,
	memory *session.CentralMemory,
	participants []string,
	intervalMin int,
	maxTurns int,
	temperature float64,
	personalities map[string]string,
	storyDataDir string,
) *IdleChatOrchestrator {
	randSeedOnce.Do(func() {
		rand.Seed(time.Now().UnixNano())
	})
	// LoadStoryData: Complex Story Mode用、現在はアーカイブ済み
	// Simple Story Mode はハードコードされた昔話リストを使用
	_ = storyDataDir // unused
	ctx, cancel := context.WithCancel(context.Background())
	return &IdleChatOrchestrator{
		llmProvider:    llmProvider,
		speakerLLMs:    make(map[string]llm.LLMProvider),
		speakerOptions: defaultIdleChatSpeakerOptions(participants),
		memory:         memory,
		participants:   participants,
		intervalMin:    intervalMin,
		interval:       time.Duration(intervalMin) * time.Minute,
		maxTurns:       maxTurns,
		temperature:    temperature,
		personalities:  personalities,
		topicGenerationConfig: normalizeTopicGenerationConfig(TopicGenerationConfig{
			Enabled:              true,
			CandidatesPerAttempt: 5,
			MaxAttempts:          3,
			JudgeEnabled:         true,
			RecentTopicWindow:    12,
			RecentSimilarity:     modulechat.RecentTopicSimilarityThreshold,
			LogCandidates:        true,
			LogJudgeScores:       true,
			ProviderName:         "chatworker",
		}),
		dialogueConfig:      DefaultDialogueInterestingnessConfig(),
		lastActivity:        time.Now(),
		history:             make([]SessionSummary, 0, 32),
		ctx:                 ctx,
		cancel:              cancel,
		runCtx:              ctx,
		interruptedSessions: make(map[string]struct{}),
	}
}

func (o *IdleChatOrchestrator) SetTopicGenerationConfig(config TopicGenerationConfig) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.topicGenerationConfig = normalizeTopicGenerationConfig(config)
}

func (o *IdleChatOrchestrator) SetDialogueInterestingnessConfig(config DialogueInterestingnessConfig) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.dialogueConfig = normalizeDialogueInterestingnessConfig(config)
}

func (o *IdleChatOrchestrator) SetEventEmitter(emit func(TimelineEvent) <-chan struct{}) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.emitEvent = emit
}

func (o *IdleChatOrchestrator) SetTTSPrefetchEmitter(emit func(TTSPrefetchEvent)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.emitTTSPrefetch = emit
}

func (o *IdleChatOrchestrator) SetTTSTimeoutReporter(report func(TTSTimeoutEvent)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.reportTTSTimeout = report
}

// SetForecastProvider sets a high-capability LLM for forecast topic generation and keyword extraction.

func (o *IdleChatOrchestrator) SetForecastProvider(provider llm.LLMProvider) {
	o.SetForecastProviderWithLabel(provider, "")
}

func (o *IdleChatOrchestrator) SetForecastProviderWithLabel(provider llm.LLMProvider, label string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.forecastProvider = provider
	o.forecastProviderLabel = strings.TrimSpace(label)
}

func (o *IdleChatOrchestrator) SetRecentTopicProvider(provider func(context.Context, int) ([]string, error)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.recentTopics = provider
}

// SetTopicStore configures persistent storage for topic summaries.

func (o *IdleChatOrchestrator) SetSpeakerProviders(providers map[string]llm.LLMProvider) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.speakerLLMs = make(map[string]llm.LLMProvider, len(providers))
	for name, provider := range providers {
		if provider == nil {
			continue
		}
		o.speakerLLMs[strings.ToLower(strings.TrimSpace(name))] = provider
	}
}

func (o *IdleChatOrchestrator) SetSpeakerProviderOptions(options map[string]map[string]any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.speakerOptions = defaultIdleChatSpeakerOptions(o.participants)
	for name, values := range options {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" || len(values) == 0 {
			continue
		}
		copied := copyProviderOptions(o.speakerOptions[key])
		for optionKey, optionValue := range values {
			if strings.TrimSpace(optionKey) == "" {
				continue
			}
			copied[optionKey] = optionValue
		}
		o.speakerOptions[key] = copied
	}
}

func (o *IdleChatOrchestrator) SetTopicStore(path string) error {
	store, err := NewTopicStore(path)
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.topicStore = store
	o.history = store.GetRecent(200)
	o.mu.Unlock()
	return nil
}

// NewIdleChatOrchestrator は新しいIdleChatOrchestratorを作成

func (o *IdleChatOrchestrator) providerForSpeaker(name string) llm.LLMProvider {
	o.mu.Lock()
	defer o.mu.Unlock()
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "shiro" {
		if provider, ok := o.speakerLLMs["chatworker"]; ok && provider != nil {
			return withProviderOptions(provider, o.speakerOptions["chatworker"])
		}
	}
	if provider, ok := o.speakerLLMs[key]; ok && provider != nil {
		return withProviderOptions(provider, o.speakerOptions[key])
	}
	return withProviderOptions(o.llmProvider, o.speakerOptions[key])
}

func (o *IdleChatOrchestrator) speakerThinkEnabled(agentName string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	options := o.speakerOptions[strings.ToLower(strings.TrimSpace(agentName))]
	if value, ok := options["think"].(bool); ok {
		return value
	}
	return defaultIdleChatThinkForSpeaker(agentName)
}

type providerOptionsWrapper struct {
	base    llm.LLMProvider
	options map[string]any
}

func withProviderOptions(provider llm.LLMProvider, options map[string]any) llm.LLMProvider {
	if provider == nil || len(options) == 0 {
		return provider
	}
	return providerOptionsWrapper{base: provider, options: copyProviderOptions(options)}
}

func (p providerOptionsWrapper) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	req.ProviderOptions = copyProviderOptions(req.ProviderOptions)
	for key, value := range p.options {
		req.ProviderOptions[key] = value
	}
	return p.base.Generate(ctx, req)
}

func (p providerOptionsWrapper) Name() string {
	return p.base.Name()
}

func defaultIdleChatSpeakerOptions(participants []string) map[string]map[string]any {
	options := map[string]map[string]any{
		"mio":        {"think": false},
		"shiro":      {"think": false},
		"chatworker": {"think": false},
	}
	for _, participant := range participants {
		key := strings.ToLower(strings.TrimSpace(participant))
		if key == "" {
			continue
		}
		if _, ok := options[key]; !ok {
			options[key] = map[string]any{"think": defaultIdleChatThinkForSpeaker(key)}
		}
	}
	return options
}

func defaultIdleChatThinkForSpeaker(agentName string) bool {
	switch strings.ToLower(strings.TrimSpace(agentName)) {
	case "mio", "shiro", "chatworker":
		return false
	default:
		return true
	}
}

func copyProviderOptions(options map[string]any) map[string]any {
	copied := make(map[string]any, len(options))
	for key, value := range options {
		copied[key] = value
	}
	return copied
}

// Start はIdleChatの監視ループを開始
