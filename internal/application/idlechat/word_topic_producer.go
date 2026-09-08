package idlechat

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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

func (o *IdleChatOrchestrator) SetRunIssuer(issuer idlechatRunIssuer) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.runIssuer = issuer
	o.mu.Unlock()
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
			_, recovering := o.generationCheckpointStore().Get("word:" + string(category))
			if stock.count(category) > 0 && !recovering {
				continue
			}
			if !o.forecastTopicRefillAvailable() || !o.tryBeginTopicProduction() {
				log.Printf("[IdleChat] Word topic bootstrap deferred: category=%s", category)
				return
			}
			target := 1
			if recovering {
				target = wordTopicStockCapacityPerCategory + 1
			}
			if !stock.reserve(category, target, "startup") {
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
	category, ok := o.reserveWordTopicProduction(stock, trigger)
	if !ok {
		o.endTopicProduction()
		return false
	}
	go o.fillWordTopicStock(stock, category, trigger)
	return true
}

func (o *IdleChatOrchestrator) reserveWordTopicProduction(stock *wordTopicStock, trigger string) (TopicCategory, bool) {
	for _, category := range wordTopicStockCategories {
		if _, found := o.generationCheckpointStore().Get("word:" + string(category)); found {
			// Recovery must run even when the pending result filled the last slot.
			return category, stock.reserve(category, wordTopicStockCapacityPerCategory+1, trigger)
		}
	}
	return stock.reserveNext(wordTopicStockCapacityPerCategory, trigger)
}

func (o *IdleChatOrchestrator) fillWordTopicStock(stock *wordTopicStock, category TopicCategory, trigger string) {
	defer o.endTopicProduction()
	err := o.produceWordTopic(stock, category)
	stock.done(category, err)
	if err != nil {
		logWordTopicStockFailure(category, trigger, err)
		return
	}
	log.Printf("[IdleChat] Word topic stock refilled: category=%s trigger=%s count=%d", category, strings.TrimSpace(trigger), stock.count(category))
}

func (o *IdleChatOrchestrator) produceWordTopic(stock *wordTopicStock, category TopicCategory) error {
	ctx := o.topicProductionContext()
	if ctx == nil {
		return errors.New("word topic context is not configured")
	}
	if _, err := generationRunOwnerFromIssuer(o.runIssuer); err != nil {
		return err
	}
	checkpointKey := "word:" + string(category)
	checkpointStore := o.generationCheckpointStore()
	if stock == nil || stock.path == "" || checkpointStore == nil || checkpointStore.path == "" {
		return errors.New("word topic persistence is not configured")
	}
	if err := checkpointStore.LoadError(); err != nil {
		return err
	}
	checkpoint, found := checkpointStore.Get(checkpointKey)
	if found && checkpoint.Stage == "resume_pending" {
		previousRunID := checkpoint.RunID
		if err := reconcileGenerationResume(ctx, o.runIssuer, &checkpoint, checkpointStore); err != nil {
			if checkpoint.RunID != previousRunID {
				completionErr := o.finishWordRun(ctx, checkpoint, domaintask.StatusWaiting, "word resume reconciliation failed", "retry from saved word generation checkpoint")
				return errors.Join(err, completionErr)
			}
			return err
		}
	}
	if found {
		if checkpoint.Category != category {
			return errors.New("word topic checkpoint category mismatch")
		}
		run, err := inspectGenerationRun(ctx, o.runIssuer, checkpoint.TaskID, checkpoint.RunID)
		if err != nil {
			return err
		}
		switch run.Status {
		case domaintask.RunStatusSucceeded:
			if err := verifySavedWordTopic(stock, checkpoint); err != nil {
				return err
			}
			if err := o.finishWordRun(ctx, checkpoint, domaintask.StatusSucceeded, "word topic saved", ""); err != nil {
				return err
			}
			return checkpointStore.Delete(checkpointKey)
		case domaintask.RunStatusFailed, domaintask.RunStatusCancelled:
			status := domaintask.StatusFailed
			if run.Status == domaintask.RunStatusCancelled {
				status = domaintask.StatusCancelled
			}
			if err := o.finishWordRun(ctx, checkpoint, status, "word generation terminated", ""); err != nil {
				return err
			}
			if _, err := stock.takeByRunID(checkpoint.RunID); err != nil {
				return err
			}
			return checkpointStore.Delete(checkpointKey)
		}
		if stock.hasRunID(checkpoint.RunID) {
			if err := verifySavedWordTopic(stock, checkpoint); err != nil {
				return err
			}
			if run.Status == domaintask.RunStatusRunning {
				if err := o.finishWordRun(ctx, checkpoint, domaintask.StatusSucceeded, "word topic saved", ""); err != nil {
					return err
				}
				return checkpointStore.Delete(checkpointKey)
			}
			// This result was never published: its checkpoint still gates playback.
			// Resume its saved generation stages under the new owner-issued Run.
			if _, err := stock.takeByRunID(checkpoint.RunID); err != nil {
				return err
			}
		}
	}
	if !found {
		strategy := StrategySingleGenre
		if category == TopicCategoryDouble {
			strategy = StrategyDoubleGenre
		}
		seed, ok := o.buildTopicSeedForStrategy(strategy)
		if !ok {
			return fmt.Errorf("word_topic_seed_unavailable: category=%s", category)
		}
		taskID, runID, err := issueIdleChatRun(ctx, o.runIssuer, "IdleChat word topic", "shiro", domaintask.RunStartReasonFirst, "")
		if err != nil {
			return err
		}
		checkpoint = GenerationCheckpoint{Key: checkpointKey, Kind: "word", TaskID: taskID, RunID: runID, Stage: "seed", Category: category, Seed: seed}
	} else {
		// Persist the resume intent before the owner can issue its successor.
		// If saving that successor ID fails, the intent allows exact recovery.
		checkpoint.Stage = "resume_pending"
		if err := checkpointStore.Put(checkpoint); err != nil {
			return err
		}
		run, err := resumeIdleChatRun(ctx, o.runIssuer, &checkpoint, checkpointStore)
		if err != nil {
			return err
		}
		checkpoint.RunID = run.RunID
	}
	if checkpoint.Result != nil {
		checkpoint.Stage = "result"
	} else if len(checkpoint.Candidates) > 0 {
		checkpoint.Stage = "candidates"
	} else {
		checkpoint.Stage = "seed"
	}
	if err := checkpointStore.Put(checkpoint); err != nil {
		status, reason := domaintask.StatusFailed, ""
		if found {
			status, reason = domaintask.StatusWaiting, "retry saving resumed generation checkpoint"
		}
		return errors.Join(err, o.finishWordRun(ctx, checkpoint, status, "word checkpoint save failed", reason))
	}
	result, err := o.generateWordTopicWithCodexCheckpoint(&checkpoint)
	if err != nil {
		return errors.Join(err, o.finishWordRun(ctx, checkpoint, domaintask.StatusWaiting, "word generation interrupted", "retry from saved generation checkpoint"))
	}
	policy := ClassifyDialogueContentPolicy(*result)
	item := WordPreparedTopic{
		Category: category, Topic: result.Topic, Seed: result.Seed, Axis: result.InterestingnessAxis,
		OpeningHook: result.OpeningHook, Avoid: result.Avoid, Judge: result.Judge,
		ContentMode: string(policy.Mode), ContentModeReasons: append([]string(nil), policy.Reasons...),
		TaskID: checkpoint.TaskID, RunID: checkpoint.RunID, InitiatedBy: "shiro", Created: time.Now().UTC(),
	}
	added, err := stock.push(item)
	if err != nil {
		return errors.Join(err, o.finishWordRun(ctx, checkpoint, domaintask.StatusWaiting, "word stock save failed", "retry from saved generation checkpoint"))
	}
	if !added && !stock.hasRunID(checkpoint.RunID) {
		err := fmt.Errorf("topic_duplicate_or_full: category=%s", category)
		if completionErr := o.finishWordRun(ctx, checkpoint, domaintask.StatusFailed, "word topic duplicate or stock full", ""); completionErr != nil {
			return errors.Join(err, completionErr)
		}
		return errors.Join(err, checkpointStore.Delete(checkpointKey))
	}
	if err := o.finishWordRun(ctx, checkpoint, domaintask.StatusSucceeded, "word topic saved", ""); err != nil {
		return err
	}
	return checkpointStore.Delete(checkpointKey)
}

func (o *IdleChatOrchestrator) finishWordRun(ctx context.Context, checkpoint GenerationCheckpoint, status domaintask.Status, summary, reason string) error {
	return finishGenerationRun(ctx, o.runIssuer, checkpoint, status, summary, reason)
}

func (o *IdleChatOrchestrator) wordTopicPublicationPending(category TopicCategory, runID modulecore.RunID) (bool, error) {
	store := o.generationCheckpointStore()
	if err := store.LoadError(); err != nil {
		return true, err
	}
	checkpoint, found := store.Get("word:" + string(category))
	return found && checkpoint.RunID == runID, nil
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
	issuer := o.runIssuer
	run, err := inspectGenerationRun(o.topicProductionContext(), issuer, checkpoint.TaskID, checkpoint.RunID)
	if err != nil {
		return nil, err
	}
	provider := newIdleChatCodexLLMProvider(newRunGuardedIdleChatCodexGenerator(issuer, checkpoint.TaskID, checkpoint.RunID, run.Assignee, generator))
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
		for _, entry := range stock.snapshot().Categories {
			if entry.Name != category || len(entry.Topics) == 0 {
				continue
			}
			runID := entry.Topics[0].RunID
			pending, err := o.wordTopicPublicationPending(category, runID)
			if err != nil {
				return nil, err
			}
			if pending {
				return nil, errors.New("word topic publication is pending")
			}
			item, err := stock.takeByRunID(runID)
			if err != nil {
				return nil, err
			}
			if item == nil {
				return nil, errors.New("word topic was consumed concurrently")
			}
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

func verifySavedWordTopic(stock *wordTopicStock, checkpoint GenerationCheckpoint) error {
	for _, category := range stock.snapshot().Categories {
		for _, item := range category.Topics {
			if item.RunID != checkpoint.RunID {
				continue
			}
			if item.TaskID != checkpoint.TaskID || item.Category != checkpoint.Category || (checkpoint.Result != nil && item.Topic != checkpoint.Result.Topic) {
				return errors.New("saved word topic does not match checkpoint")
			}
			return nil
		}
	}
	return errors.New("completed word run has no saved topic")
}
