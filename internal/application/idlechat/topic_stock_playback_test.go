package idlechat

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newReadyPlaybackDialogueService(t *testing.T, owner idlechatRunIssuer, categories ...TopicCategory) (*DialogueEpisodeService, []DialogueEpisodeArtifact) {
	t.Helper()
	ctx := nightGateAllowedContext(t)
	responses := make([]string, len(categories))
	for i := range responses {
		responses[i] = `{"turns":[{"speaker":"mio","display_text":"防災設備の赤いランプを店で確認する場面から始めると、判断した人の責任が見えそうです。","speech_text":"防災設備の赤いランプを店で確認する場面から始めると、判断した人の責任が見えそうです。"}]}`
	}
	config := DefaultDialogueInterestingnessConfig()
	config.MaxTurnsPerTopic = 1
	service := NewPersistentDialogueEpisodeService(filepath.Join(t.TempDir(), "dialogue_episodes.jsonl"), &queuedIdleChatCodexGenerator{responses: responses}, map[string]string{"mio": "Mio", "shiro": "Shiro"}, config)
	service.SetRunIssuer(owner)
	artifacts := make([]DialogueEpisodeArtifact, 0, len(categories))
	for i, category := range categories {
		strategy := string(StrategySingleGenre)
		if category == TopicCategoryDouble {
			strategy = string(StrategyDoubleGenre)
		}
		if category == TopicCategoryForecast {
			strategy = "forecast"
		}
		result := TopicGenerationResult{
			Topic:               fmt.Sprintf("防災設備を店頭で選ぶ判断者 %d", i+1),
			Category:            category,
			Strategy:            strategy,
			InterestingnessAxis: "観察",
			Seed:                TopicSeed{Category: category, Genre1: "防災", ForecastDomain: "地域"},
		}
		artifact, err := service.Prepare(ctx, string(modulecore.NewSessionID()), result, 1)
		if err != nil {
			t.Fatalf("prepare ready playback dialogue for %s: %v", category, err)
		}
		artifacts = append(artifacts, artifact)
	}
	return service, artifacts
}

func TestTopicStockPlaybackUsesPreparedDialogueHistoryWithoutConsumingRawStock(t *testing.T) {
	wordStock := newWordTopicStock("")
	wordRunID := modulecore.NewRunID()
	wordTaskID := modulecore.NewTaskID()
	added, pushErr := wordStock.push(WordPreparedTopic{
		Category: TopicCategorySingle, Topic: "駅前の店で防災設備を選ぶ最後の判断者は誰か",
		Seed: TopicSeed{Category: TopicCategorySingle, Genre1: "防災"}, Axis: "観察",
		TaskID: wordTaskID, RunID: wordRunID, Created: time.Now().UTC(),
	})
	if pushErr != nil {
		t.Fatalf("word topic push failed: %v", pushErr)
	}
	if !added {
		t.Fatal("word topic push failed")
	}
	forecastStock := newForecastTopicStock("")
	domain := forecastDomains[0]
	forecastRunID := modulecore.NewRunID()
	forecastTaskID := modulecore.NewTaskID()
	if added, err := forecastStock.push(domain.Name, PreparedTopic{
		Domain: domain, Topic: "AIの普及で地域の窓口が2年後に担う相談の変化",
		Seeds: []string{"窓口", "AI"}, TaskID: forecastTaskID, RunID: forecastRunID, Created: time.Now().UTC(),
	}); err != nil || !added {
		t.Fatal("forecast topic push failed")
	}

	owner := newTestIdleChatRunIssuer(t)
	service, artifacts := newReadyPlaybackDialogueService(t, owner, TopicCategorySingle, TopicCategoryDouble)
	if len(artifacts) != 2 {
		t.Fatalf("prepared artifacts=%d, want 2", len(artifacts))
	}
	orchestrator := &IdleChatOrchestrator{wordTopicStock: wordStock, topicStockBuf: forecastStock, dialogueEpisodeService: service, topicPlaybackIndex: -1}
	first, err := orchestrator.selectTopicStockPlaybackItem(TopicStockPlaybackPlay, dialogueArtifactPlaybackID(artifacts[0]))
	if err != nil {
		t.Fatal(err)
	}
	if first.Stock != "single" || first.dialogue == nil || wordStock.count(TopicCategorySingle) != 1 {
		t.Fatalf("first=%+v word_count=%d", first, wordStock.count(TopicCategorySingle))
	}

	second, err := orchestrator.selectTopicStockPlaybackItem(TopicStockPlaybackNext, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.Stock != "double" || second.dialogue == nil || forecastStock.count(domain.Name) != 1 {
		t.Fatalf("second=%+v forecast_count=%d", second, forecastStock.count(domain.Name))
	}

	previous, err := orchestrator.selectTopicStockPlaybackItem(TopicStockPlaybackPrevious, "")
	if err != nil {
		t.Fatal(err)
	}
	if previous.ID != first.ID {
		t.Fatalf("previous=%q want=%q", previous.ID, first.ID)
	}
	if wordStock.count(TopicCategorySingle) != 1 || forecastStock.count(domain.Name) != 1 {
		t.Fatal("prepared playback must not consume or restore raw topic stock")
	}

	snapshot := orchestrator.TopicStockPlaybackSnapshot()
	if snapshot.Current == nil || snapshot.Current.ID != first.ID || snapshot.Position != 1 || snapshot.HistorySize != 2 || snapshot.CanPrevious || !snapshot.CanNext {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestTopicStockPlaybackRejectsPreviousBeforeHistory(t *testing.T) {
	orchestrator := &IdleChatOrchestrator{}
	_, err := orchestrator.selectTopicStockPlaybackItem(TopicStockPlaybackPrevious, "")
	if !errors.Is(err, ErrTopicStockNoPrevious) {
		t.Fatalf("err=%v", err)
	}
}

func TestTopicStockPlaybackDoesNotExposeTopicWithoutReadyDialogue(t *testing.T) {
	wordStock := newWordTopicStock("")
	wordTaskID, wordRunID := testIdleChatRunIdentityPair()
	if added, err := wordStock.push(WordPreparedTopic{
		Category: TopicCategorySingle,
		Topic:    "防災設備を店頭に導入するとき誰が最後の判断をするか",
		Seed:     TopicSeed{Category: TopicCategorySingle, Genre1: "防災"},
		Axis:     "観察",
		TaskID:   wordTaskID,
		RunID:    wordRunID,
		Created:  time.Now().UTC(),
	}); err != nil || !added {
		t.Fatalf("push word topic: added=%v err=%v", added, err)
	}

	orchestrator := &IdleChatOrchestrator{wordTopicStock: wordStock}
	if got := orchestrator.availableTopicStockPlaybackItems(); len(got) != 0 {
		t.Fatalf("topic-only stock produced playable items: %+v", got)
	}
}

func TestTopicStockPlaybackDoesNotStartLiveGenerationWithoutReadyDialogue(t *testing.T) {
	orchestrator, _ := newConversationLifecycleFixture(t)
	t.Cleanup(orchestrator.Stop)
	wordStock := newWordTopicStock("")
	taskID, runID := testIdleChatRunIdentityPair()
	if added, err := wordStock.push(WordPreparedTopic{
		Category: TopicCategorySingle,
		Topic:    "防災時の店舗運営を誰が決めるか",
		Seed:     TopicSeed{Category: TopicCategorySingle, Genre1: "防災"},
		Axis:     "観察",
		TaskID:   taskID,
		RunID:    runID,
		Created:  time.Now().UTC(),
	}); err != nil || !added {
		t.Fatalf("push word topic: added=%v err=%v", added, err)
	}
	orchestrator.mu.Lock()
	orchestrator.wordTopicStock = wordStock
	orchestrator.mu.Unlock()

	_, err := orchestrator.StartTopicStockPlayback(TopicStockPlaybackPlay, wordPlaybackID(runID))
	if err == nil {
		t.Fatal("playback started from a topic stock entry without a ready dialogue artifact")
	}
	if wordStock.count(TopicCategorySingle) != 1 {
		t.Fatal("failed prepared playback consumed the topic stock item")
	}
}

func TestPreparedDialoguePlaybackUsesSavedTurnsSummaryAndReviewWithoutLLM(t *testing.T) {
	owner := newTestIdleChatRunIssuer(t)
	service, artifacts := newReadyPlaybackDialogueService(t, owner, TopicCategorySingle, TopicCategoryDouble, TopicCategoryForecast)
	provider := &capturingIdleProvider{response: "この応答は呼ばれてはいけません。"}
	orchestrator := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 2, 0.7, nil, "")
	orchestrator.SetRunIssuer(owner)
	orchestrator.SetDialogueEpisodeService(service)
	events := make(chan TimelineEvent, 32)
	orchestrator.SetEventEmitter(func(event TimelineEvent) TTSLifecycle {
		events <- event
		return TTSLifecycle{}
	})
	generator := service.generator.(*queuedIdleChatCodexGenerator)
	productionCalls := len(generator.requests)
	for _, artifact := range artifacts {
		if _, err := orchestrator.StartTopicStockPlayback(TopicStockPlaybackPlay, dialogueArtifactPlaybackID(artifact)); err != nil {
			t.Fatalf("start prepared playback for %s: %v", artifact.TopicResult.Category, err)
		}
		orchestrator.waitGenerationWork()
	}
	if len(provider.requests) != 0 {
		t.Fatalf("conversation LLM calls during prepared playback=%d, want zero", len(provider.requests))
	}
	if len(generator.requests) != productionCalls {
		t.Fatalf("CodexExe calls during prepared playback=%d, want unchanged at %d", len(generator.requests), productionCalls)
	}
	seenMessage, seenSummary := false, false
	for len(events) > 0 {
		event := <-events
		seenMessage = seenMessage || event.Type == "idlechat.message"
		seenSummary = seenSummary || event.Type == "idlechat.summary"
	}
	if !seenMessage || !seenSummary {
		t.Fatalf("prepared playback events message=%t summary=%t", seenMessage, seenSummary)
	}
	orchestrator.mu.Lock()
	if len(orchestrator.history) == 0 {
		orchestrator.mu.Unlock()
		t.Fatal("prepared playback did not save its precomputed summary and review")
	}
	record := orchestrator.history[len(orchestrator.history)-1]
	orchestrator.mu.Unlock()
	if record.Summary != artifacts[len(artifacts)-1].PreparedSummary || record.QualityReview != strings.TrimSpace(artifacts[len(artifacts)-1].PreparedQualityReview) || record.SummaryProvider != "prepared" {
		t.Fatalf("saved prepared summary record=%+v want_summary=%q want_review=%q", record, artifacts[len(artifacts)-1].PreparedSummary, artifacts[len(artifacts)-1].PreparedQualityReview)
	}
	tasks, err := owner.List(context.Background(), domaintask.Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	succeeded := 0
	for _, task := range tasks {
		if task.Status == domaintask.StatusSucceeded {
			succeeded++
		}
	}
	if succeeded < 2*len(artifacts) {
		t.Fatalf("succeeded Tasks=%d, want production and playback Tasks for all categories", succeeded)
	}
}

func TestPreparedDialoguePlaybackReleasesReservationOnInterrupt(t *testing.T) {
	owner := newTestIdleChatRunIssuer(t)
	service, artifacts := newReadyPlaybackDialogueService(t, owner, TopicCategorySingle)
	provider := &capturingIdleProvider{}
	orchestrator := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 2, 0.7, nil, "")
	orchestrator.SetRunIssuer(owner)
	orchestrator.SetDialogueEpisodeService(service)
	topicStarted := make(chan struct{})
	var once sync.Once
	orchestrator.SetEventEmitter(func(event TimelineEvent) TTSLifecycle {
		if event.Type == "idlechat.topic" {
			service.readyMu.RLock()
			reservations := len(service.playbackReservations)
			service.readyMu.RUnlock()
			if reservations == 1 {
				once.Do(func() { close(topicStarted) })
			}
			return TTSLifecycle{Done: make(chan struct{})}
		}
		return TTSLifecycle{}
	})
	if _, err := orchestrator.StartTopicStockPlayback(TopicStockPlaybackPlay, dialogueArtifactPlaybackID(artifacts[0])); err != nil {
		t.Fatalf("start prepared playback: %v", err)
	}
	waitForGenerationWorkSignal(t, topicStarted, "reserved prepared playback")
	playbackTaskID, playbackRunID := activeConversationIDs(orchestrator)
	if playbackTaskID == "" || playbackRunID == "" {
		t.Fatal("prepared playback did not bind its own Task and Run")
	}
	orchestrator.Interrupt("test_interruption")
	orchestrator.waitGenerationWork()
	assertConversationPair(t, owner, playbackTaskID, domaintask.StatusCancelled, domaintask.RunStatusCancelled)
	service.readyMu.RLock()
	reservations := len(service.playbackReservations)
	service.readyMu.RUnlock()
	if reservations != 0 {
		t.Fatalf("reservations after interrupt=%d, want zero", reservations)
	}
	if got := len(orchestrator.history); got != 0 {
		t.Fatalf("interrupted prepared playback wrote summary history: %d", got)
	}
	if len(provider.requests) != 0 {
		t.Fatal("interrupted playback called a conversation LLM")
	}
	playbackTask, playbackRun, err := owner.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "Prepared playback reservation retry", Route: domaintask.RouteGeneral, Assignee: "Shiro",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := service.ReserveReady(context.Background(), artifacts[0].EpisodeID, artifacts[0].Revision, playbackTask.TaskID, playbackRun.RunID)
	if err != nil {
		t.Fatalf("reserve artifact after interrupted playback: %v", err)
	}
	service.ReleaseReadyReservation(reserved, playbackTask.TaskID, playbackRun.RunID)
	if _, err := owner.CompleteRun(context.Background(), playbackTask.TaskID, playbackRun.RunID, "Shiro", domaintask.StatusCancelled, "test done", ""); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedDialoguePlaybackRejectsPolicyChangeDuringReservation(t *testing.T) {
	owner := newTestIdleChatRunIssuer(t)
	service, artifacts := newReadyPlaybackDialogueService(t, owner, TopicCategorySingle)
	provider := &capturingIdleProvider{response: "この応答は呼ばれてはいけません。"}
	orchestrator := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 2, 0.7, nil, "")
	orchestrator.SetRunIssuer(owner)
	orchestrator.SetDialogueEpisodeService(service)
	topicStarted := make(chan struct{})
	topicRelease := make(chan struct{})
	var topicOnce sync.Once
	orchestrator.SetEventEmitter(func(event TimelineEvent) TTSLifecycle {
		if event.Type == "idlechat.topic" {
			topicOnce.Do(func() { close(topicStarted) })
			return TTSLifecycle{Done: topicRelease}
		}
		return TTSLifecycle{}
	})
	t.Cleanup(func() {
		close(topicRelease)
		orchestrator.Stop()
	})
	policyRevisionChanged := errors.New("presence policy revision changed")
	guardCalls := 0
	guard := func() error {
		guardCalls++
		if guardCalls == 1 {
			return nil
		}
		service.readyMu.RLock()
		reservations := len(service.playbackReservations)
		service.readyMu.RUnlock()
		if reservations != 1 {
			t.Errorf("reservation count during final policy check=%d, want 1", reservations)
		}
		return policyRevisionChanged
	}

	snapshot, err := orchestrator.StartTopicStockPlaybackWithGuard(TopicStockPlaybackPlay, dialogueArtifactPlaybackID(artifacts[0]), guard)
	if err == nil {
		waitForGenerationWorkSignal(t, topicStarted, "playback launched despite a changed policy revision")
	}
	if !errors.Is(err, ErrTopicStockPlaybackStale) || !errors.Is(err, policyRevisionChanged) {
		t.Fatalf("start error=%v, want stale playback wrapping policy revision change", err)
	}
	select {
	case <-topicStarted:
		t.Fatal("stale playback published a topic event")
	default:
	}
	if guardCalls != 2 {
		t.Fatalf("policy guard calls=%d, want selection and pre-activation checks", guardCalls)
	}
	if snapshot.HistorySize != 0 || snapshot.Current != nil {
		t.Fatalf("stale playback changed history: %+v", snapshot)
	}
	if orchestrator.IsChatActive() || orchestrator.IsManualMode() {
		t.Fatal("stale policy check left an active playback")
	}
	service.readyMu.RLock()
	reservations := len(service.playbackReservations)
	service.readyMu.RUnlock()
	if reservations != 0 {
		t.Fatalf("reservations after stale rejection=%d, want zero", reservations)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("stale playback generated %d LLM requests, want zero", len(provider.requests))
	}
	tasks, err := owner.List(context.Background(), domaintask.Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("owner Tasks=%d, want production Task and finalized playback Task", len(tasks))
	}
	var rejectedTaskID modulecore.TaskID
	for _, task := range tasks {
		if task.Title == "IdleChat conversation" {
			rejectedTaskID = task.TaskID
			if task.Status != domaintask.StatusCancelled {
				t.Fatalf("stale playback Task status=%s, want cancelled", task.Status)
			}
		}
	}
	if rejectedTaskID == "" {
		t.Fatal("stale playback Task was not created")
	}
	assertConversationPair(t, owner, rejectedTaskID, domaintask.StatusCancelled, domaintask.RunStatusCancelled)
	retryTask, retryRun, err := owner.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "Reserve after stale playback", Route: domaintask.RouteGeneral, Assignee: "Shiro",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := service.ReserveReady(context.Background(), artifacts[0].EpisodeID, artifacts[0].Revision, retryTask.TaskID, retryRun.RunID)
	if err != nil {
		t.Fatalf("stale rejection left artifact unavailable: %v", err)
	}
	service.ReleaseReadyReservation(reserved, retryTask.TaskID, retryRun.RunID)
	if _, err := owner.CompleteRun(context.Background(), retryTask.TaskID, retryRun.RunID, "Shiro", domaintask.StatusCancelled, "test complete", ""); err != nil {
		t.Fatal(err)
	}
}

func TestForecastPlaybackRejectsPreparedArtifactFromPreviousJSTDate(t *testing.T) {
	owner := newTestIdleChatRunIssuer(t)
	service, artifacts := newReadyPlaybackDialogueService(t, owner, TopicCategoryForecast)
	if len(artifacts) != 1 {
		t.Fatalf("prepared artifacts=%d, want 1", len(artifacts))
	}
	if got := (&IdleChatOrchestrator{dialogueEpisodeService: service}).availableTopicStockPlaybackItems(); len(got) != 1 {
		t.Fatalf("same-day forecast ready items=%d, want 1", len(got))
	}
	service.readyMu.Lock()
	service.readyArtifacts[0].CreatedAt = time.Now().In(jst).AddDate(0, 0, -1)
	service.readyMu.Unlock()
	orchestrator := &IdleChatOrchestrator{dialogueEpisodeService: service}
	if got := orchestrator.availableTopicStockPlaybackItems(); len(got) != 0 {
		t.Fatalf("previous-day forecast ready items=%+v", got)
	}
	if _, err := orchestrator.selectTopicStockPlaybackItem(TopicStockPlaybackPlay, dialogueArtifactPlaybackID(artifacts[0])); !errors.Is(err, ErrPreparedDialogueEmpty) {
		t.Fatalf("previous-day forecast playback error=%v, want typed empty", err)
	}
}

func TestPreparedStoryPlaybackUsesTerminalArtifactWithoutRefillOrLLM(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	artifact.ProductionStatus = StoryProductionReady
	artifact.Validation = StoryValidationResult{Valid: true}
	owner, artifact := storyServiceArtifactWithTerminalRun(t, artifact)
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 2)
	if err := store.append(artifact); err != nil {
		t.Fatal(err)
	}
	storyGenerator := &queuedStoryCodexGenerator{}
	service := NewStoryEpisodeService(store, storyGenerator, map[string]string{"mio": "Mio", "shiro": "Shiro"})
	service.SetRunIssuer(owner)
	provider := &capturingIdleProvider{response: "この応答は呼ばれてはいけません。"}
	orchestrator := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 2, 0.7, nil, "")
	orchestrator.SetRunIssuer(owner)
	orchestrator.SetStoryEpisodeService(service)
	events := make(chan TimelineEvent, 32)
	orchestrator.SetEventEmitter(func(event TimelineEvent) TTSLifecycle {
		events <- event
		return TTSLifecycle{}
	})
	if _, err := orchestrator.StartTopicStockPlayback(TopicStockPlaybackPlay, storyArtifactPlaybackID(artifact)); err != nil {
		t.Fatalf("start prepared story playback: %v", err)
	}
	orchestrator.waitGenerationWork()
	if len(provider.requests) != 0 || len(storyGenerator.prompts) != 0 {
		t.Fatalf("playback LLM calls=%d story refill/generation calls=%d, want zero", len(provider.requests), len(storyGenerator.prompts))
	}
	messageEvents := make([]TimelineEvent, 0, len(artifact.Turns))
	for len(events) > 0 {
		event := <-events
		if event.Type == "idlechat.message" {
			messageEvents = append(messageEvents, event)
		}
	}
	if len(messageEvents) != len(artifact.Turns) {
		t.Fatalf("story message events=%d, want %d", len(messageEvents), len(artifact.Turns))
	}
	if messageEvents[0].MessageID == artifact.Turns[0].MessageID {
		t.Fatal("story playback reused the production MessageID")
	}
	if service.Snapshot().Missing != 1 {
		t.Fatalf("story refill changed missing count: %+v", service.Snapshot())
	}
	if service.Snapshot().Episodes[0].PlayCount != 1 {
		t.Fatalf("story artifact was not marked played: %+v", service.Snapshot().Episodes[0])
	}
	tasks, err := owner.List(context.Background(), domaintask.Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) < 2 {
		t.Fatalf("owner Tasks=%d, want story production and playback", len(tasks))
	}
}

func TestPreparedStoryPlaybackDoesNotReactivateAfterInterruptBetweenGuardAndWorker(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	artifact.ProductionStatus = StoryProductionReady
	artifact.Validation = StoryValidationResult{Valid: true}
	owner, artifact := storyServiceArtifactWithTerminalRun(t, artifact)
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 2)
	if err := store.append(artifact); err != nil {
		t.Fatal(err)
	}
	storyGenerator := &queuedStoryCodexGenerator{}
	service := NewStoryEpisodeService(store, storyGenerator, map[string]string{"mio": "Mio", "shiro": "Shiro"})
	service.SetRunIssuer(owner)
	provider := &capturingIdleProvider{response: "この応答は呼ばれてはいけません。"}
	orchestrator := NewIdleChatOrchestrator(provider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 2, 0.7, nil, "")
	orchestrator.SetRunIssuer(owner)
	orchestrator.SetStoryEpisodeService(service)
	events := make(chan TimelineEvent, 32)
	orchestrator.SetEventEmitter(func(event TimelineEvent) TTSLifecycle {
		events <- event
		return TTSLifecycle{}
	})
	guardBlocked := make(chan struct{})
	continueStart := make(chan struct{})
	var continueOnce sync.Once
	releaseStart := func() { continueOnce.Do(func() { close(continueStart) }) }
	t.Cleanup(func() {
		releaseStart()
		orchestrator.Stop()
	})
	guardCalls := 0
	guard := func() error {
		guardCalls++
		if guardCalls == 2 {
			close(guardBlocked)
			<-continueStart
		}
		return nil
	}
	type startResult struct{ err error }
	started := make(chan startResult, 1)
	go func() {
		_, err := orchestrator.StartTopicStockPlaybackWithGuard(TopicStockPlaybackPlay, storyArtifactPlaybackID(artifact), guard)
		started <- startResult{err: err}
	}()
	waitForGenerationWorkSignal(t, guardBlocked, "playback second guard")
	playbackTaskID, playbackRunID := activeConversationIDs(orchestrator)
	if playbackTaskID == "" || playbackRunID == "" {
		t.Fatal("guard barrier reached without the admitted playback Task/Run")
	}
	orchestrator.Interrupt("foreground_claim_after_policy_check")
	assertConversationPair(t, owner, playbackTaskID, domaintask.StatusCancelled, domaintask.RunStatusCancelled)
	releaseStart()
	select {
	case result := <-started:
		if result.err != nil {
			t.Fatalf("playback start after interrupt: %v", result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("playback start did not leave the guard barrier")
	}
	orchestrator.waitGenerationWork()

	messageEvents := 0
	for len(events) > 0 {
		if event := <-events; event.Type == "idlechat.message" {
			messageEvents++
		}
	}
	if messageEvents != 0 {
		t.Fatalf("stale prepared story published %d message events after interrupt", messageEvents)
	}
	if orchestrator.IsChatActive() || orchestrator.IsManualMode() {
		t.Fatal("stale prepared story reactivated the interrupted generation")
	}
	if len(provider.requests) != 0 || len(storyGenerator.prompts) != 0 {
		t.Fatalf("stale story LLM calls: conversation=%d production=%d", len(provider.requests), len(storyGenerator.prompts))
	}
	if got := service.Snapshot().Episodes[0].PlayCount; got != 0 {
		t.Fatalf("stale prepared story marked the artifact played %d times", got)
	}
	tasks, err := owner.List(context.Background(), domaintask.Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("owner Tasks after stale worker=%d, want only production and cancelled playback Tasks", len(tasks))
	}
	assertConversationPair(t, owner, playbackTaskID, domaintask.StatusCancelled, domaintask.RunStatusCancelled)
	if !orchestrator.generationWorkIsQuiesced() {
		t.Fatal("stale prepared story retained its generation-work lease")
	}
}

func TestDialogueReadyListRequiresTerminalProductionTaskAndRunAndIgnoresProductionMutex(t *testing.T) {
	owner := newTestIdleChatRunIssuer(t)
	service, artifacts := newReadyPlaybackDialogueService(t, owner, TopicCategorySingle)
	productionTask, productionRun, err := owner.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "Nonterminal dialogue fixture", Route: domaintask.RouteGeneral, Assignee: "Shiro",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	nonterminal := cloneDialogueEpisode(artifacts[0])
	nonterminal.EpisodeID = "dialogue-nonterminal-fixture"
	nonterminal.TaskID = productionTask.TaskID
	nonterminal.RunID = productionRun.RunID
	if err := service.append(nonterminal); err != nil {
		t.Fatal(err)
	}
	service.SetRunIssuer(owner)
	ready, err := service.ListReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].EpisodeID != artifacts[0].EpisodeID {
		t.Fatalf("ready artifacts=%+v; nonterminal production artifact must be hidden", ready)
	}

	service.mu.Lock()
	loaded := make(chan struct{})
	go func() {
		defer close(loaded)
		_, _ = service.ListReady(context.Background())
	}()
	select {
	case <-loaded:
		service.mu.Unlock()
	case <-time.After(time.Second):
		service.mu.Unlock()
		t.Fatal("ready projection blocked behind production mutex")
	}
	if _, err := owner.CompleteRun(context.Background(), productionTask.TaskID, productionRun.RunID, "Shiro", domaintask.StatusSucceeded, "finish fixture", ""); err != nil {
		t.Fatal(err)
	}
	service.SetRunIssuer(owner)
	ready, err = service.ListReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 2 {
		t.Fatalf("ready artifacts after production completion=%d, want 2", len(ready))
	}
}

func TestForecastPlaybackRequiresPreparedDialogueAfterTopicCheckpointCleanup(t *testing.T) {
	dir := t.TempDir()
	stock := newForecastTopicStock(filepath.Join(dir, "stock.json"))
	taskID, runID := testIdleChatRunIdentityPair()
	domain := forecastDomains[0]
	if added, err := stock.push(domain.Name, PreparedTopic{Domain: domain, Topic: "saved forecast", TaskID: taskID, RunID: runID, Created: time.Now().UTC()}); err != nil || !added {
		t.Fatalf("push: %v", err)
	}
	cp := NewGenerationCheckpointStore(filepath.Join(dir, "checkpoints.json"))
	key := "forecast:" + domain.Name
	if err := cp.Put(GenerationCheckpoint{Key: key, Kind: "forecast", TaskID: taskID, RunID: runID}); err != nil {
		t.Fatal(err)
	}
	o := &IdleChatOrchestrator{topicStockBuf: stock}
	o.SetGenerationCheckpointStore(cp)
	if got := o.availableTopicStockPlaybackItems(); len(got) != 0 {
		t.Fatal("pending forecast was listed")
	}
	if _, err := o.consumeTopicStockPlaybackItem(forecastPlaybackID(runID)); !errors.Is(err, ErrPreparedDialogueEmpty) {
		t.Fatalf("pending raw forecast error=%v, want no prepared dialogue", err)
	}
	if stock.count(domain.Name) != 1 {
		t.Fatal("pending artifact was removed")
	}
	if err := cp.Delete(key); err != nil {
		t.Fatal(err)
	}
	if _, err := o.consumeTopicStockPlaybackItem(forecastPlaybackID(runID)); !errors.Is(err, ErrPreparedDialogueEmpty) {
		t.Fatalf("checkpoint cleanup made raw forecast playable: %v", err)
	}
}
