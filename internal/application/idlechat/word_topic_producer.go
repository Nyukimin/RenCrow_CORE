package idlechat

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
)

var errWordTopicCodexUnavailable = errors.New("word_topic_codex_unavailable")

// SetTopicCodexGenerator configures the only generation mechanism allowed for
// single, double, and Forecast topic production.
func (o *IdleChatOrchestrator) SetTopicCodexGenerator(generator IdleChatCodexGenerator) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.topicCodexGenerator = generator
	codexProvider := newIdleChatCodexLLMProvider(generator)
	o.forecastTopicProvider = codexProvider
	o.forecastTopicProviderLabel = "CodexExe"
	o.forecastProvider = codexProvider
	o.forecastProviderLabel = "CodexExe"
	o.mu.Unlock()
}

func (o *IdleChatOrchestrator) tryBeginTopicProduction() bool {
	if o == nil {
		return false
	}
	o.topicProducerMu.Lock()
	defer o.topicProducerMu.Unlock()
	if o.topicProducerBusy {
		return false
	}
	ctx, cancel := context.WithCancel(o.ctx)
	o.topicProducerBusy = true
	o.topicProducerCtx = ctx
	o.topicProducerCancel = cancel
	return true
}

func (o *IdleChatOrchestrator) endTopicProduction() {
	if o == nil {
		return
	}
	o.topicProducerMu.Lock()
	if o.topicProducerCancel != nil {
		o.topicProducerCancel()
	}
	o.topicProducerBusy = false
	o.topicProducerCtx = nil
	o.topicProducerCancel = nil
	o.topicProducerMu.Unlock()
}

func (o *IdleChatOrchestrator) topicProductionContext() context.Context {
	if o == nil {
		return context.Background()
	}
	o.topicProducerMu.Lock()
	defer o.topicProducerMu.Unlock()
	if o.topicProducerCtx != nil {
		return o.topicProducerCtx
	}
	return o.ctx
}

func (o *IdleChatOrchestrator) cancelTopicProduction(reason string) {
	if o == nil {
		return
	}
	o.topicProducerMu.Lock()
	cancel := o.topicProducerCancel
	busy := o.topicProducerBusy
	o.topicProducerMu.Unlock()
	if cancel != nil {
		cancel()
		if busy {
			log.Printf("[IdleChat] Stock generation yielded to foreground: reason=%s", strings.TrimSpace(reason))
		}
	}
}

func (o *IdleChatOrchestrator) SetGenerationCheckpointStore(store *GenerationCheckpointStore) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.generationCheckpoints = store
	o.mu.Unlock()
}

func (o *IdleChatOrchestrator) generationCheckpointStore() *GenerationCheckpointStore {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.generationCheckpoints
}

func (o *IdleChatOrchestrator) topicProductionBusy() bool {
	if o == nil {
		return false
	}
	o.topicProducerMu.Lock()
	defer o.topicProducerMu.Unlock()
	return o.topicProducerBusy
}

// InitWordTopicStock loads the persistent single/double stock and bootstraps
// one item for each empty category without blocking runtime startup.
func (o *IdleChatOrchestrator) InitWordTopicStock(path string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	if o.wordTopicStock != nil {
		o.mu.Unlock()
		return
	}
	o.wordTopicStock = newWordTopicStock(path)
	stock := o.wordTopicStock
	o.mu.Unlock()
	log.Printf("[IdleChat] Word topic stock initialized (total=%d capacity=%d)", stock.total(), len(wordTopicStockCategories)*wordTopicStockCapacityPerCategory)
	o.bootstrapWordTopicStockAsync(stock)
}

func (o *IdleChatOrchestrator) WordTopicStockSnapshot() WordTopicStockSnapshot {
	if o == nil {
		return (*wordTopicStock)(nil).snapshot()
	}
	o.mu.Lock()
	stock := o.wordTopicStock
	o.mu.Unlock()
	return stock.snapshot()
}

func (o *IdleChatOrchestrator) bootstrapWordTopicStockAsync(stock *wordTopicStock) {
	if stock == nil {
		return
	}
	go func() {
		for _, category := range wordTopicStockCategories {
			if stock.count(category) > 0 {
				continue
			}
			if !o.forecastTopicRefillAvailable() || !o.tryBeginTopicProduction() {
				log.Printf("[IdleChat] Word topic bootstrap deferred: category=%s", category)
				return
			}
			if !stock.reserve(category, 1, "startup") {
				o.endTopicProduction()
				continue
			}
			o.fillWordTopicStock(stock, category, "startup")
		}
	}()
}

// RefillWordTopicStockIfIdle starts at most one single/double topic producer.
func (o *IdleChatOrchestrator) RefillWordTopicStockIfIdle(trigger string) bool {
	if o == nil || !o.forecastTopicRefillAvailable() || !o.tryBeginTopicProduction() {
		return false
	}
	o.mu.Lock()
	stock := o.wordTopicStock
	o.mu.Unlock()
	if stock == nil {
		o.endTopicProduction()
		return false
	}
	category, ok := stock.reserveNext(wordTopicStockCapacityPerCategory, trigger)
	if !ok {
		o.endTopicProduction()
		return false
	}
	go o.fillWordTopicStock(stock, category, trigger)
	return true
}

func (o *IdleChatOrchestrator) fillWordTopicStock(stock *wordTopicStock, category TopicCategory, trigger string) {
	defer o.endTopicProduction()
	checkpointKey := "word:" + string(category)
	checkpointStore := o.generationCheckpointStore()
	checkpoint, found := checkpointStore.Get(checkpointKey)
	if found && checkpoint.Category != category {
		_ = checkpointStore.Delete(checkpointKey)
		found = false
	}
	if found && stock.hasGenerationID(checkpoint.GenerationID) {
		_ = checkpointStore.Delete(checkpointKey)
		stock.done(category, nil)
		return
	}
	if !found {
		strategy := StrategySingleGenre
		if category == TopicCategoryDouble {
			strategy = StrategyDoubleGenre
		}
		seed, ok := o.buildTopicSeedForStrategy(strategy)
		if !ok {
			err := fmt.Errorf("word_topic_seed_unavailable: category=%s", category)
			stock.done(category, err)
			logWordTopicStockFailure(category, trigger, err)
			return
		}
		checkpoint = GenerationCheckpoint{
			Key: checkpointKey, Kind: "word", GenerationID: uuid.NewString(), Stage: "seed",
			Category: category, Seed: seed,
		}
		if err := checkpointStore.Put(checkpoint); err != nil {
			stock.done(category, err)
			logWordTopicStockFailure(category, trigger, err)
			return
		}
	}
	result, err := o.generateWordTopicWithCodexCheckpoint(&checkpoint)
	if err != nil {
		stock.done(category, err)
		logWordTopicStockFailure(category, trigger, err)
		return
	}
	policy := ClassifyDialogueContentPolicy(*result)
	item := WordPreparedTopic{
		Category:           category,
		Topic:              result.Topic,
		Seed:               result.Seed,
		Axis:               result.InterestingnessAxis,
		OpeningHook:        result.OpeningHook,
		Avoid:              result.Avoid,
		Judge:              result.Judge,
		ContentMode:        string(policy.Mode),
		ContentModeReasons: append([]string(nil), policy.Reasons...),
		GenerationID:       checkpoint.GenerationID,
		InitiatedBy:        "shiro",
		Created:            time.Now().UTC(),
	}
	if !stock.push(item) {
		err := fmt.Errorf("topic_duplicate_or_full: category=%s", category)
		if stock.hasGenerationID(checkpoint.GenerationID) {
			_ = checkpointStore.Delete(checkpointKey)
			stock.done(category, nil)
			return
		}
		_ = checkpointStore.Delete(checkpointKey)
		stock.done(category, err)
		logWordTopicStockFailure(category, trigger, err)
		return
	}
	if err := checkpointStore.Delete(checkpointKey); err != nil {
		log.Printf("[IdleChat] Word topic checkpoint cleanup deferred: category=%s error=%v", category, err)
	}
	stock.done(category, nil)
	log.Printf("[IdleChat] Word topic stock refilled: category=%s trigger=%s count=%d", category, strings.TrimSpace(trigger), stock.count(category))
}

func (o *IdleChatOrchestrator) generateWordTopicWithCodexCheckpoint(checkpoint *GenerationCheckpoint) (*TopicGenerationResult, error) {
	if checkpoint == nil || !isWordTopicCategory(checkpoint.Category) {
		return nil, fmt.Errorf("%w: invalid checkpoint", errWordTopicCodexUnavailable)
	}
	o.mu.Lock()
	generator := o.topicCodexGenerator
	config := o.topicGenerationConfig
	stock := o.wordTopicStock
	forecastStock := o.topicStockBuf
	o.mu.Unlock()
	if generator == nil {
		return nil, errWordTopicCodexUnavailable
	}
	if config.CandidatesPerAttempt <= 0 || config.CandidatesPerAttempt > 3 {
		config.CandidatesPerAttempt = 3
	}
	config.ProviderName = "CodexExe"
	if len(checkpoint.Recent) == 0 {
		recent := recentTopicRecords(o.getRecentTopics(config.RecentTopicWindow))
		if stock != nil {
			recent = append(recent, stock.topics()...)
		}
		if forecastStock != nil {
			snapshot := forecastStock.snapshot()
			for _, domain := range snapshot.Domains {
				for _, item := range domain.Topics {
					recent = append(recent, RecentTopic{Topic: item.Topic, Category: TopicCategoryForecast, Strategy: string(StrategyForecast)})
				}
			}
		}
		checkpoint.Recent = recent
		if err := o.generationCheckpointStore().Put(*checkpoint); err != nil {
			return nil, err
		}
	}
	resume := TopicGenerationResumeState{Attempt: checkpoint.Attempt, Candidates: checkpoint.Candidates, Result: checkpoint.Result}
	provider := newIdleChatCodexLLMProvider(generator)
	result, err := NewTopicGenerator(provider, config).GenerateInterestingTopicResumable(
		o.topicProductionContext(), checkpoint.Category, checkpoint.Seed, checkpoint.Recent, resume,
		func(state TopicGenerationResumeState) error {
			checkpoint.Attempt = state.Attempt
			checkpoint.Candidates = append([]TopicCandidate(nil), state.Candidates...)
			checkpoint.Result = state.Result
			if state.Result != nil {
				checkpoint.Stage = "result"
			} else {
				checkpoint.Stage = "candidates"
			}
			return o.generationCheckpointStore().Put(*checkpoint)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("CodexExe word topic generation: %w", err)
	}
	if result == nil || strings.TrimSpace(result.Topic) == "" {
		return nil, errors.New("CodexExe word topic generation returned no topic")
	}
	result.Initiator = "shiro"
	return result, nil
}

func (o *IdleChatOrchestrator) generateWordTopicWithCodex(seed TopicSeed) (*TopicGenerationResult, error) {
	if o == nil || !isWordTopicCategory(seed.Category) {
		return nil, fmt.Errorf("%w: invalid category", errWordTopicCodexUnavailable)
	}
	o.mu.Lock()
	generator := o.topicCodexGenerator
	config := o.topicGenerationConfig
	stock := o.wordTopicStock
	forecastStock := o.topicStockBuf
	o.mu.Unlock()
	if generator == nil {
		return nil, errWordTopicCodexUnavailable
	}
	if config.CandidatesPerAttempt <= 0 || config.CandidatesPerAttempt > 3 {
		config.CandidatesPerAttempt = 3
	}
	config.ProviderName = "CodexExe"
	recent := recentTopicRecords(o.getRecentTopics(config.RecentTopicWindow))
	if stock != nil {
		recent = append(recent, stock.topics()...)
	}
	if forecastStock != nil {
		snapshot := forecastStock.snapshot()
		for _, domain := range snapshot.Domains {
			for _, item := range domain.Topics {
				recent = append(recent, RecentTopic{Topic: item.Topic, Category: TopicCategoryForecast, Strategy: string(StrategyForecast)})
			}
		}
	}
	provider := newIdleChatCodexLLMProvider(generator)
	result, err := NewTopicGenerator(provider, config).GenerateInterestingTopic(o.idleRunContext(), seed.Category, seed, recent)
	if err != nil {
		return nil, fmt.Errorf("CodexExe word topic generation: %w", err)
	}
	if result == nil || strings.TrimSpace(result.Topic) == "" {
		return nil, errors.New("CodexExe word topic generation returned no topic")
	}
	result.Initiator = "shiro"
	return result, nil
}

func wordTopicResultFromItem(item WordPreparedTopic) TopicGenerationResult {
	return TopicGenerationResult{
		Topic:               item.Topic,
		Category:            item.Category,
		Strategy:            string(item.Category),
		InterestingnessAxis: item.Axis,
		OpeningHook:         item.OpeningHook,
		Avoid:               item.Avoid,
		Seed:                item.Seed,
		Judge:               item.Judge,
		Provider:            "CodexExe",
		Initiator:           item.InitiatedBy,
	}
}

func (o *IdleChatOrchestrator) takeWordTopic(strategy TopicStrategy) (*TopicGenerationResult, error) {
	category := TopicCategorySingle
	if strategy == StrategyDoubleGenre {
		category = TopicCategoryDouble
	} else if strategy != StrategySingleGenre {
		return nil, fmt.Errorf("unsupported word topic strategy %s", strategy)
	}
	o.mu.Lock()
	stock := o.wordTopicStock
	o.mu.Unlock()
	if stock != nil {
		if item := stock.pop(category); item != nil {
			result := wordTopicResultFromItem(*item)
			return &result, nil
		}
	}
	seed, ok := o.buildTopicSeedForStrategy(strategy)
	if !ok {
		return nil, fmt.Errorf("word_topic_seed_unavailable: category=%s", category)
	}
	return o.generateWordTopicWithCodex(seed)
}

func wordTopicGenerationError(strategy TopicStrategy, err error) string {
	code := "codex_unavailable"
	if err != nil && !errors.Is(err, errWordTopicCodexUnavailable) {
		code = "generation_failed"
	}
	return fmt.Sprintf("WORD_TOPIC_GENERATION_FAILED error_code=%s category=%s", code, strategy)
}

func isWordTopicGenerationError(topic string) bool {
	return strings.HasPrefix(strings.TrimSpace(topic), "WORD_TOPIC_GENERATION_FAILED ")
}
