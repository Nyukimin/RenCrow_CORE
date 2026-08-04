package idlechat

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
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
	o.mu.Lock()
	service := o.storyEpisodeService
	o.mu.Unlock()
	if service == nil {
		return fmt.Errorf("story episode producer is not configured")
	}
	return service.PrepareToTarget(ctx)
}

func (o *IdleChatOrchestrator) PrepareStoryEpisodeCountAsync(count int, reason string) {
	o.mu.Lock()
	service := o.storyEpisodeService
	ctx := o.ctx
	o.mu.Unlock()
	if service == nil {
		return
	}
	go func() {
		if err := service.PrepareAdditional(ctx, count); err != nil {
			log.Printf("[Story] explicit prepare incomplete: reason=%s count=%d error=%v", strings.TrimSpace(reason), count, err)
		}
		if err := service.RepairNeedsRepair(ctx); err != nil {
			log.Printf("[Story] suffix repair after explicit prepare incomplete: reason=%s error=%v", strings.TrimSpace(reason), err)
		}
	}()
}

func (o *IdleChatOrchestrator) RefillStoryEpisodesAsync(reason string) {
	o.mu.Lock()
	service := o.storyEpisodeService
	ctx := o.ctx
	o.mu.Unlock()
	stock := StoryEpisodeStockSnapshot{}
	if service != nil {
		stock = service.Snapshot()
	}
	if service == nil || (stock.Missing == 0 && stock.NeedsRepair == 0) || stock.Filling {
		return
	}
	go func() {
		if service.Snapshot().Missing > 0 {
			if err := service.PrepareToTarget(ctx); err != nil {
				log.Printf("[Story] ready stock refill incomplete: reason=%s error=%v", strings.TrimSpace(reason), err)
			}
		}
		if err := service.RepairNeedsRepair(ctx); err != nil {
			log.Printf("[Story] suffix repair incomplete: reason=%s error=%v", strings.TrimSpace(reason), err)
		}
	}()
}

// RunPreparedStorySession only plays a fully generated, validated ready item.
// It never falls back to live generation while the listener is waiting.
func (o *IdleChatOrchestrator) RunPreparedStorySession() {
	o.mu.Lock()
	service := o.storyEpisodeService
	o.mu.Unlock()
	if service == nil {
		log.Printf("[Story] prepared story service is not configured")
		return
	}
	artifact, ok := service.NextReady()
	if !ok {
		log.Printf("[Story] no ready episode; refill requested")
		o.RefillStoryEpisodesAsync("playback_empty")
		return
	}

	sessionID := "story-episode-" + artifact.EpisodeID
	startedAt := time.Now().In(jst)
	o.mu.Lock()
	o.chatActive = true
	o.sessionMode = "story"
	generation := o.beginIdleRunLocked()
	o.activeSessionID = sessionID
	o.currentTopic = artifact.Source.Title
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		if o.activeGeneration == generation {
			o.chatActive = false
			o.sessionMode = ""
			o.currentTopic = ""
			o.activeSessionID = ""
		}
		o.lastActivity = time.Now()
		o.mu.Unlock()
		o.cancelIdleRunIfGeneration(generation)
	}()

	transcript := make([]string, 0, len(artifact.Turns))
	prefetched := make(map[int]struct{}, len(artifact.Turns))
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
		for j := i; j < len(artifact.Turns) && j < i+prefetchWindow; j++ {
			if _, done := prefetched[j]; done {
				continue
			}
			next := artifact.Turns[j]
			o.emitStoryTTSPrefetch(sessionID, next)
			prefetched[j] = struct{}{}
		}
		transcript = append(transcript, turn.Speaker+": "+turn.DisplayText)
		o.recordPreparedStoryTurn(sessionID, turn)
		event := TimelineEvent{
			Type:       "idlechat.message",
			From:       normalizeStoryAgent(turn.Speaker),
			To:         "user",
			Content:    strings.TrimSpace(turn.DisplayText),
			RawContent: strings.TrimSpace(turn.SpeechText),
			SessionID:  sessionID,
			MessageID:  turn.MessageID,
			TurnIndex:  turn.TurnIndex,
			Category:   TopicCategoryStory,
			Strategy:   TopicStrategy("story"),
		}
		done := o.emitTimelineEvent(event)
		o.waitForTTSDoneForEvent(event, done)
		o.waitBreak(speakerBreak)
	}
	if !o.isIdleSessionActive(sessionID, generation) {
		return
	}
	if err := service.MarkPlayed(artifact.EpisodeID, time.Now().UTC()); err != nil {
		log.Printf("[Story] mark played failed: episode=%s error=%v", artifact.EpisodeID, err)
	}
	o.savePreparedStoryReview(artifact, sessionID, transcript, startedAt)
	o.RefillStoryEpisodesAsync("played")
}

func (o *IdleChatOrchestrator) emitStoryTTSPrefetch(sessionID string, turn StoryEpisodeTurn) {
	o.mu.Lock()
	emit := o.emitTTSPrefetch
	o.mu.Unlock()
	if emit == nil || strings.TrimSpace(turn.MessageID) == "" || strings.TrimSpace(turn.SpeechText) == "" {
		return
	}
	emit(TTSPrefetchEvent{
		SessionID: sessionID,
		MessageID: turn.MessageID,
		From:      normalizeStoryAgent(turn.Speaker),
		To:        "user",
		TurnIndex: turn.TurnIndex,
		Token:     turn.SpeechText,
	})
}

func (o *IdleChatOrchestrator) recordPreparedStoryTurn(sessionID string, turn StoryEpisodeTurn) {
	if o.memory == nil {
		return
	}
	message := domaintransport.NewMessage(normalizeStoryAgent(turn.Speaker), "user", sessionID, "", turn.DisplayText)
	message.Type = domaintransport.MessageTypeIdleChat
	o.memory.RecordMessage(message)
}

func (o *IdleChatOrchestrator) savePreparedStoryReview(artifact StoryEpisodeArtifact, sessionID string, transcript []string, startedAt time.Time) {
	endedAt := time.Now().In(jst)
	record := SessionSummary{
		SessionID:       sessionID,
		Title:           artifact.Source.Title,
		Topic:           artifact.Source.Title,
		Category:        TopicCategoryStory,
		Strategy:        TopicStrategy("story"),
		Summary:         fmt.Sprintf("%sが読み手、%sが合いの手の事前生成物語。", artifact.Reader, artifact.Listener),
		SourceTitle:     artifact.Source.Title,
		RewriteStyle:    artifact.Contract.TransformationAxis,
		StoryTitle:      artifact.Source.Title,
		StartedAt:       startedAt.Format(time.RFC3339),
		EndedAt:         endedAt.Format(time.RFC3339),
		Turns:           len(transcript),
		TopicProvider:   "codex_exe",
		SummaryProvider: "prevalidated",
		Transcript:      append([]string(nil), transcript...),
	}
	o.mu.Lock()
	o.history = append(o.history, record)
	store := o.topicStore
	o.mu.Unlock()
	if store != nil {
		if err := store.Append(record); err != nil {
			log.Printf("[Story] topic store append failed: %v", err)
		}
	}
}
