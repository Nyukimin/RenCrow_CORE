package idlechat

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const storyTTSPrefetchWindow = 3

func (o *IdleChatOrchestrator) SetStoryEpisodeService(service *StoryEpisodeService) {
	o.mu.Lock()
	o.storyEpisodeService = service
	o.mu.Unlock()
}

func (o *IdleChatOrchestrator) SetStoryTTSPrefetchWindow(utterances int) {
	if utterances < 1 {
		return
	}
	o.mu.Lock()
	o.storyTTSPrefetchWindow = utterances
	o.mu.Unlock()
}

func (o *IdleChatOrchestrator) StoryEpisodeStockSnapshot() StoryEpisodeStockSnapshot {
	o.mu.Lock()
	service := o.storyEpisodeService
	o.mu.Unlock()
	if service == nil {
		return StoryEpisodeStockSnapshot{}
	}
	return service.Snapshot()
}

func (o *IdleChatOrchestrator) StoryEpisode(episodeID string) (StoryEpisodeArtifact, bool) {
	o.mu.Lock()
	service := o.storyEpisodeService
	o.mu.Unlock()
	if service == nil {
		return StoryEpisodeArtifact{}, false
	}
	return service.Episode(episodeID)
}

func (o *IdleChatOrchestrator) PrepareStoryEpisodes(ctx context.Context) error {
	if o == nil {
		return errors.New("story episode producer is not configured")
	}
	o.mu.Lock()
	service := o.storyEpisodeService
	o.mu.Unlock()
	if service == nil {
		return fmt.Errorf("story episode producer is not configured")
	}
	if _, _, ok := o.nightPreparationWorkContext(ctx); !ok {
		return ErrNightPreparationBlocked
	}
	ctx = requireNightPreparationContext(ctx)
	if err := runWithNightPreparationGate(ctx, func(context.Context) error { return nil }); err != nil {
		return err
	}
	return service.PrepareToTarget(ctx)
}

// PrepareRequestedStoryEpisodesWithContext dispatches the scheduler-owned
// absolute Story target. It deliberately leaves that target pending while
// the shared producer is busy, generation is canceled, or the service stops
// short of the requested ready count.
func (o *IdleChatOrchestrator) PrepareRequestedStoryEpisodesWithContext(ctx context.Context) error {
	if o == nil || ctx == nil {
		return ErrNightPreparationBlocked
	}
	scheduler := nightPreparationSchedulerFromContext(ctx)
	target := nightPreparationStoryTargetFromContext(ctx)
	if scheduler == nil || target < 1 || scheduler != o.currentNightPreparationScheduler() {
		return ErrNightPreparationBlocked
	}
	o.mu.Lock()
	service := o.storyEpisodeService
	o.mu.Unlock()
	if service == nil {
		return errors.New("story episode producer is not configured")
	}
	if !service.Snapshot().Enabled {
		return errors.New("story episode stock is unavailable")
	}
	if !o.forecastTopicRefillAvailable() || !o.tryBeginTopicProductionWithScheduler(scheduler) {
		return nil
	}
	if !o.startGenerationWork(func() {
		defer o.endTopicProduction()
		workCtx := withNightPreparationStoryTarget(o.topicProductionContext(), target)
		err := service.PrepareToReadyTarget(workCtx, target)
		snapshot := service.Snapshot()
		if snapshot.Enabled && snapshot.Ready >= target {
			scheduler.CompleteStoryPreparationTarget(target)
			return
		}
		if err != nil {
			log.Printf("[Story] requested preparation incomplete: target=%d ready=%d error=%v", target, snapshot.Ready, err)
			return
		}
		log.Printf("[Story] requested preparation remains pending: target=%d ready=%d", target, snapshot.Ready)
	}) {
		o.endTopicProduction()
		return errIdleChatGenerationWorkClosed
	}
	return nil
}

func (o *IdleChatOrchestrator) PrepareStoryEpisodeCountAsync(count int, reason string) {
	o.PrepareStoryEpisodeCountAsyncWithContext(nil, count, reason)
}

func (o *IdleChatOrchestrator) PrepareStoryEpisodeCountAsyncWithContext(ctx context.Context, count int, reason string) {
	if o == nil {
		return
	}
	_, scheduler, admitted := o.nightPreparationWorkContext(ctx)
	if !admitted {
		return
	}
	o.mu.Lock()
	service := o.storyEpisodeService
	o.mu.Unlock()
	if service == nil {
		return
	}
	if err := runWithNightPreparationGate(ctx, func(context.Context) error {
		if !o.forecastTopicRefillAvailable() || !o.tryBeginTopicProductionWithScheduler(scheduler) {
			return nil
		}
		if !o.startGenerationWork(func() {
			defer o.endTopicProduction()
			workCtx := o.topicProductionContext()
			if err := service.PrepareAdditional(workCtx, count); err != nil {
				log.Printf("[Story] explicit prepare incomplete: reason=%s count=%d error=%v", strings.TrimSpace(reason), count, err)
			}
			if err := service.RepairNeedsRepair(workCtx); err != nil {
				log.Printf("[Story] suffix repair after explicit prepare incomplete: reason=%s error=%v", strings.TrimSpace(reason), err)
			}
		}) {
			o.endTopicProduction()
			return errIdleChatGenerationWorkClosed
		}
		return nil
	}); err != nil {
		log.Printf("[Story] explicit prepare admission failed: reason=%s error=%v", strings.TrimSpace(reason), err)
	}
}

func (o *IdleChatOrchestrator) RefillStoryEpisodesAsync(reason string) {
	o.RefillStoryEpisodesAsyncWithContext(nil, reason)
}

func (o *IdleChatOrchestrator) RefillStoryEpisodesAsyncWithContext(ctx context.Context, reason string) {
	if o == nil {
		return
	}
	_, scheduler, admitted := o.nightPreparationWorkContext(ctx)
	if !admitted {
		return
	}
	o.mu.Lock()
	service := o.storyEpisodeService
	o.mu.Unlock()
	stock := StoryEpisodeStockSnapshot{}
	if service != nil {
		stock = service.Snapshot()
	}
	if service == nil || (stock.Missing == 0 && stock.NeedsRepair == 0 && stock.UntitledReady == 0) || stock.Filling {
		return
	}
	if err := runWithNightPreparationGate(ctx, func(context.Context) error {
		if !o.forecastTopicRefillAvailable() || !o.tryBeginTopicProductionWithScheduler(scheduler) {
			return nil
		}
		if !o.startGenerationWork(func() {
			defer o.endTopicProduction()
			workCtx := o.topicProductionContext()
			if stock.UntitledReady > 0 {
				if err := service.BackfillReadyTitles(workCtx); err != nil {
					log.Printf("[Story] ready title backfill incomplete: reason=%s error=%v", strings.TrimSpace(reason), err)
				}
			}
			if service.Snapshot().Missing > 0 {
				if err := service.PrepareToTarget(workCtx); err != nil {
					log.Printf("[Story] ready stock refill incomplete: reason=%s error=%v", strings.TrimSpace(reason), err)
				}
			}
			if err := service.RepairNeedsRepair(workCtx); err != nil {
				log.Printf("[Story] suffix repair incomplete: reason=%s error=%v", strings.TrimSpace(reason), err)
			}
		}) {
			o.endTopicProduction()
			return errIdleChatGenerationWorkClosed
		}
		return nil
	}); err != nil {
		log.Printf("[Story] refill admission failed: reason=%s error=%v", strings.TrimSpace(reason), err)
	}
}

// RunPreparedStorySession only plays a fully generated, validated ready item.
// It never falls back to live generation while the listener is waiting.
func (o *IdleChatOrchestrator) RunPreparedStorySession(prepared ...StoryEpisodeArtifact) {
	o.runPreparedStorySessionForOwner(0, "", "", prepared...)
}

func (o *IdleChatOrchestrator) runPreparedStorySessionForPlayback(prepared StoryEpisodeArtifact, generation uint64, taskID modulecore.TaskID, runID modulecore.RunID) {
	o.runPreparedStorySessionForOwner(generation, taskID, runID, prepared)
}

func (o *IdleChatOrchestrator) runPreparedStorySessionForOwner(expectedGeneration uint64, expectedTaskID modulecore.TaskID, expectedRunID modulecore.RunID, prepared ...StoryEpisodeArtifact) {
	o.mu.Lock()
	service := o.storyEpisodeService
	o.mu.Unlock()
	if service == nil {
		log.Printf("[Story] prepared story service is not configured")
		if expectedGeneration != 0 {
			o.finishTopicStockPlayback(expectedGeneration)
		} else {
			o.resetIdleSessionState(0)
			if err := o.cancelIdleRun(); err != nil {
				log.Printf("[Story] conversation run finalization failed without service: %v", err)
			}
		}
		return
	}
	artifact, ok := StoryEpisodeArtifact{}, false
	if len(prepared) > 0 {
		artifact = cloneStoryEpisode(prepared[0])
		ok = artifact.ProductionStatus == StoryProductionReady && artifact.Validation.Valid && o.storyProductionRunSucceeded(artifact)
	} else {
		artifact, ok = service.NextReady()
		ok = ok && artifact.ProductionStatus == StoryProductionReady && artifact.Validation.Valid && o.storyProductionRunSucceeded(artifact)
	}
	if !ok {
		log.Printf("[Story] no validated terminal ready episode")
		if expectedGeneration != 0 {
			o.finishTopicStockPlayback(expectedGeneration)
		} else {
			o.resetIdleSessionState(0)
			if err := o.cancelIdleRun(); err != nil {
				log.Printf("[Story] conversation run finalization failed without ready episode: %v", err)
			}
		}
		return
	}

	sessionID := string(modulecore.NewSessionID())
	startedAt := time.Now().In(jst)
	var generation uint64
	if expectedGeneration != 0 {
		var err error
		generation, err = o.activatePreparedStoryPlaybackSession(sessionID, expectedGeneration, expectedTaskID, expectedRunID)
		if err != nil {
			o.finishTopicStockPlayback(expectedGeneration)
			log.Printf("[Story] stale prepared playback rejected before session binding: session=%s generation=%d error=%v", sessionID, expectedGeneration, err)
			return
		}
	} else {
		o.emitMu.Lock()
		o.mu.Lock()
		if !o.chatActive {
			o.chatActive = true
		}
		o.sessionMode = "story"
		o.mu.Unlock()
		o.emitMu.Unlock()
		var err error
		generation, err = o.activateIdleSession(sessionID)
		if err != nil {
			o.resetIdleSessionState(generation)
			log.Printf("[Story] session start failed before playback: session=%s error=%v", sessionID, err)
			return
		}
	}
	o.mu.Lock()
	if o.activeGeneration != generation || !o.chatActive || o.disabled {
		o.mu.Unlock()
		o.finishTopicStockPlayback(generation)
		return
	}
	o.currentTopic = storyEpisodeDisplayTitle(artifact)
	o.mu.Unlock()
	playbackCompleted := false
	defer func() {
		if playbackCompleted {
			o.markConversationRunSucceeded(generation, "prepared story playback completed")
		}
		o.emitMu.Lock()
		o.mu.Lock()
		if o.activeGeneration == generation {
			o.chatActive = false
			o.sessionMode = ""
			o.currentTopic = ""
			o.activeSessionID = ""
		}
		o.lastActivity = time.Now()
		o.mu.Unlock()
		o.emitMu.Unlock()
		if err := o.cancelIdleRunIfGeneration(generation); err != nil {
			log.Printf("[Story] conversation run finalization failed: session=%s error=%v", sessionID, err)
		}
	}()

	transcript := make([]string, 0, len(artifact.Turns))
	prefetched := make(map[int]struct{}, len(artifact.Turns))
	playbackMessageIDs := make([]string, len(artifact.Turns))
	for index := range playbackMessageIDs {
		playbackMessageIDs[index] = newIdleChatMessageID()
	}
	o.mu.Lock()
	prefetchWindow := o.storyTTSPrefetchWindow
	o.mu.Unlock()
	if prefetchWindow < 1 {
		prefetchWindow = storyTTSPrefetchWindow
	}
	for i, turn := range artifact.Turns {
		if !o.isIdleSessionActive(sessionID, generation) {
			return
		}
		playbackTurn := turn
		playbackTurn.TurnIndex = i + 1
		playbackTurn.MessageID = playbackMessageIDs[i]
		for j := i; j < len(artifact.Turns) && j < i+prefetchWindow; j++ {
			if _, done := prefetched[j]; done {
				continue
			}
			next := artifact.Turns[j]
			next.MessageID = playbackMessageIDs[j]
			next.TurnIndex = j + 1
			o.emitStoryTTSPrefetch(sessionID, generation, next)
			prefetched[j] = struct{}{}
		}
		transcript = append(transcript, turn.Speaker+": "+turn.DisplayText)
		o.recordPreparedStoryTurn(sessionID, generation, artifact, turn, playbackTurn)
		event := TimelineEvent{
			Type:       "idlechat.message",
			From:       normalizeStoryAgent(playbackTurn.Speaker),
			To:         "user",
			Content:    strings.TrimSpace(playbackTurn.DisplayText),
			RawContent: strings.TrimSpace(playbackTurn.SpeechText),
			SessionID:  sessionID,
			MessageID:  playbackTurn.MessageID,
			TurnIndex:  playbackTurn.TurnIndex,
			Category:   TopicCategoryStory,
			Strategy:   TopicStrategy("story"),
			ownerEpoch: generation,
		}
		done := o.emitTimelineEvent(event)
		o.waitForTTSReadyForEvent(event, done)
		o.waitBreak(speakerBreak)
	}
	if !o.isIdleSessionActive(sessionID, generation) {
		return
	}
	if err := service.MarkPlayed(artifact.EpisodeID, time.Now().UTC()); err != nil {
		log.Printf("[Story] mark played failed: episode=%s error=%v", artifact.EpisodeID, err)
		o.markConversationRunFailed(generation, "prepared story playback failed", err.Error())
		return
	}
	o.savePreparedStoryReview(artifact, sessionID, generation, transcript, startedAt)
	playbackCompleted = true
}

func (o *IdleChatOrchestrator) activatePreparedStoryPlaybackSession(sessionID string, expectedGeneration uint64, expectedTaskID modulecore.TaskID, expectedRunID modulecore.RunID) (uint64, error) {
	if o == nil || expectedGeneration == 0 {
		return expectedGeneration, errIdleChatConversationRunInvalidated
	}
	o.emitMu.Lock()
	o.mu.Lock()
	if o.disabled || !o.chatActive || o.activeGeneration != expectedGeneration ||
		o.activeTaskID != expectedTaskID || o.activeRunID != expectedRunID ||
		o.runCancel == nil || o.runCtx == nil || o.runCtx.Err() != nil || o.conversationRunStarting || o.pendingConversationRun != nil {
		o.mu.Unlock()
		o.emitMu.Unlock()
		return expectedGeneration, errIdleChatConversationRunInvalidated
	}
	if err := o.bindIdleSessionLocked(sessionID); err != nil {
		o.mu.Unlock()
		o.emitMu.Unlock()
		if finalizeErr := o.cancelIdleRunIfGeneration(expectedGeneration); finalizeErr != nil {
			return expectedGeneration, errors.Join(err, finalizeErr)
		}
		return expectedGeneration, err
	}
	o.mu.Unlock()
	o.emitMu.Unlock()
	return expectedGeneration, nil
}

func (o *IdleChatOrchestrator) emitStoryTTSPrefetch(sessionID string, generation uint64, turn StoryEpisodeTurn) {
	o.mu.Lock()
	emit := o.emitTTSPrefetch
	o.mu.Unlock()
	if emit == nil || strings.TrimSpace(turn.MessageID) == "" || strings.TrimSpace(turn.SpeechText) == "" {
		return
	}
	o.emitTTSPrefetchEvent(TTSPrefetchEvent{
		SessionID:  sessionID,
		MessageID:  turn.MessageID,
		From:       normalizeStoryAgent(turn.Speaker),
		To:         "user",
		TurnIndex:  turn.TurnIndex,
		Token:      turn.SpeechText,
		ownerEpoch: generation,
	})
}

func (o *IdleChatOrchestrator) recordPreparedStoryTurn(sessionID string, generation uint64, artifact StoryEpisodeArtifact, sourceTurn, playbackTurn StoryEpisodeTurn) {
	if o.memory == nil {
		return
	}
	message := domaintransport.NewMessage(normalizeStoryAgent(playbackTurn.Speaker), "user", sessionID, "", playbackTurn.DisplayText)
	message.Type = domaintransport.MessageTypeIdleChat
	message.Context = map[string]any{
		"message_id":          playbackTurn.MessageID,
		"turn_index":          playbackTurn.TurnIndex,
		"prepared_episode_id": artifact.EpisodeID,
		"prepared_revision":   artifact.Revision,
		"source_turn_index":   sourceTurn.TurnIndex,
	}
	if !o.recordIdleMessageForGeneration(generation, sessionID, message) {
		log.Printf("[Story] prepared turn record rejected without active owner: session=%s generation=%d", sessionID, generation)
	}
}

func (o *IdleChatOrchestrator) savePreparedStoryReview(artifact StoryEpisodeArtifact, sessionID string, generation uint64, transcript []string, startedAt time.Time) {
	endedAt := time.Now().In(jst)
	record := SessionSummary{
		SessionID:       sessionID,
		Title:           storyEpisodeDisplayTitle(artifact),
		Topic:           storyEpisodeDisplayTitle(artifact),
		Category:        TopicCategoryStory,
		Strategy:        TopicStrategy("story"),
		Summary:         fmt.Sprintf("%sが読み手、%sが合いの手の事前生成物語。", artifact.Reader, artifact.Listener),
		SourceTitle:     artifact.Source.Title,
		RewriteStyle:    artifact.Contract.TransformationAxis,
		StoryTitle:      storyEpisodeDisplayTitle(artifact),
		StartedAt:       startedAt.Format(time.RFC3339),
		EndedAt:         endedAt.Format(time.RFC3339),
		Turns:           len(transcript),
		TopicProvider:   "codex_exe",
		SummaryProvider: "prevalidated",
		Transcript:      append([]string(nil), transcript...),
	}
	o.emitMu.Lock()
	o.mu.Lock()
	if !o.copyActiveThreadIdentityLocked(&record, sessionID, generation) {
		o.mu.Unlock()
		o.emitMu.Unlock()
		log.Printf("[Story] summary rejected without active thread owner: session=%s generation=%d", sessionID, generation)
		return
	}
	o.history = append(o.history, record)
	store := o.topicStore
	o.mu.Unlock()
	if store != nil {
		if err := store.Append(record); err != nil {
			log.Printf("[Story] topic store append failed: %v", err)
		}
	}
	o.emitMu.Unlock()
}

func storyEpisodeDisplayTitle(artifact StoryEpisodeArtifact) string {
	if title := strings.TrimSpace(artifact.StoryTitle); title != "" {
		return title
	}
	return strings.TrimSpace(artifact.Source.Title)
}
