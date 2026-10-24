package idlechat

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrPreparedDialogueInvalid  = errors.New("prepared dialogue artifact is invalid or unavailable")
	ErrPreparedDialogueEmpty    = errors.New("no ready prepared dialogue artifacts")
	ErrPreparedDialogueExpired  = errors.New("prepared dialogue artifact is no longer ready")
	ErrPreparedDialogueReserved = errors.New("prepared dialogue artifact is reserved by another playback Run")
)

type dialoguePlaybackReservation struct {
	TaskID modulecore.TaskID
	RunID  modulecore.RunID
}

func validDialogueEpisodeSchemaVersion(version int) bool {
	return version == DialogueEpisodeLegacySchemaVersion || version == DialogueEpisodeSchemaVersion
}

func dialogueArtifactPreparedForPlayback(artifact DialogueEpisodeArtifact) bool {
	return validDialogueEpisodeSchemaVersion(artifact.SchemaVersion) &&
		artifact.ProductionStatus == DialogueProductionReady && artifact.Validation.Valid &&
		strings.TrimSpace(artifact.PreparedTitle) != "" &&
		strings.TrimSpace(artifact.PreparedSummary) != "" &&
		strings.TrimSpace(artifact.PreparedQualityReview) != ""
}

func prepareDialogueArtifactForPlayback(artifact *DialogueEpisodeArtifact) {
	if artifact == nil {
		return
	}
	topic := strings.TrimSpace(artifact.TopicResult.Topic)
	if topic == "" {
		topic = "IdleChatの対話"
	}
	if strings.TrimSpace(artifact.PreparedTitle) == "" {
		artifact.PreparedTitle = truncate(topic, 48)
	}
	transcript := make([]string, 0, len(artifact.Turns))
	for _, turn := range artifact.Turns {
		if text := strings.TrimSpace(turn.DisplayText); text != "" {
			transcript = append(transcript, turn.Speaker+": "+text)
		}
	}
	if strings.TrimSpace(artifact.PreparedSummary) == "" {
		if len(transcript) == 0 {
			artifact.PreparedSummary = "事前生成された対話です。"
		} else {
			first := stripSpeakerPrefix(transcript[0])
			last := stripSpeakerPrefix(transcript[len(transcript)-1])
			artifact.PreparedSummary = fmt.Sprintf("「%s」をめぐる対話。冒頭では%sが話題になり、最後は%sへ展開しました。", truncate(topic, 32), truncate(first, 48), truncate(last, 48))
		}
	}
	if strings.TrimSpace(artifact.PreparedQualityReview) == "" {
		review, _ := heuristicQualityReview(topic, string(artifact.TopicResult.Category), transcript, artifact.PreparedSummary, "")
		artifact.PreparedQualityReview = review
	}
}

// ListReady returns the immutable ready projection. It never takes the
// production mutex held across dialogue generation and persistence.
func (s *DialogueEpisodeService) ListReady(ctx context.Context) ([]DialogueEpisodeArtifact, error) {
	if s == nil {
		return nil, ErrPreparedDialogueInvalid
	}
	if ctx == nil {
		return nil, errors.New("prepared dialogue list context is nil")
	}
	s.readyMu.RLock()
	if s.readyLoaded {
		items := cloneReadyDialogueArtifacts(s.readyArtifacts)
		err := s.readyLoadErr
		s.readyMu.RUnlock()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrPreparedDialogueInvalid, err)
		}
		return items, nil
	}
	s.readyMu.RUnlock()

	s.readyLoadMu.Lock()
	defer s.readyLoadMu.Unlock()
	s.readyMu.RLock()
	loaded := s.readyLoaded
	s.readyMu.RUnlock()
	if !loaded {
		items, err := s.loadReadyDialogueArtifacts(ctx)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrPreparedDialogueInvalid, err)
		}
		s.readyMu.Lock()
		s.readyArtifacts = cloneReadyDialogueArtifacts(items)
		s.readyLoadErr = nil
		s.readyLoaded = true
		s.readyMu.Unlock()
	}
	s.readyMu.RLock()
	items := cloneReadyDialogueArtifacts(s.readyArtifacts)
	err := s.readyLoadErr
	s.readyMu.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPreparedDialogueInvalid, err)
	}
	return items, nil
}

func (s *DialogueEpisodeService) loadReadyDialogueArtifacts(ctx context.Context) ([]DialogueEpisodeArtifact, error) {
	artifacts, err := s.readPersistedDialogueArtifacts()
	if err != nil {
		return nil, err
	}
	s.readyMu.RLock()
	issuer := s.readyIssuer
	s.readyMu.RUnlock()
	if issuer == nil {
		if len(artifacts) == 0 {
			return nil, nil
		}
		return nil, errors.New("dialogue production Run owner is not configured")
	}
	owner, err := generationRunOwnerFromIssuer(issuer)
	if err != nil {
		return nil, err
	}
	ready := make([]DialogueEpisodeArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if artifact.ProductionStatus != DialogueProductionReady || !artifact.Validation.Valid {
			continue
		}
		if !ValidateDialogueEpisode(artifact, s.config).Valid {
			continue
		}
		if !dialogueArtifactPreparedForPlayback(artifact) {
			continue
		}
		run, err := inspectGenerationRun(ctx, issuer, artifact.TaskID, artifact.RunID)
		if err != nil {
			return nil, fmt.Errorf("verify prepared dialogue production Run: %w", err)
		}
		task, err := owner.Get(ctx, artifact.TaskID)
		if err != nil {
			return nil, fmt.Errorf("verify prepared dialogue production Task: %w", err)
		}
		if task.TaskID != artifact.TaskID || task.Status != domaintask.StatusSucceeded || run.Status != domaintask.RunStatusSucceeded {
			continue
		}
		ready = append(ready, cloneDialogueEpisode(artifact))
	}
	sort.Slice(ready, func(i, j int) bool {
		if ready[i].CreatedAt.Equal(ready[j].CreatedAt) {
			if ready[i].EpisodeID == ready[j].EpisodeID {
				return ready[i].Revision < ready[j].Revision
			}
			return ready[i].EpisodeID < ready[j].EpisodeID
		}
		return ready[i].CreatedAt.Before(ready[j].CreatedAt)
	})
	return ready, nil
}

func cloneReadyDialogueArtifacts(artifacts []DialogueEpisodeArtifact) []DialogueEpisodeArtifact {
	if len(artifacts) == 0 {
		return nil
	}
	cloned := make([]DialogueEpisodeArtifact, len(artifacts))
	for i, artifact := range artifacts {
		cloned[i] = cloneDialogueEpisode(artifact)
	}
	return cloned
}

func dialoguePlaybackReservationKey(artifact DialogueEpisodeArtifact) string {
	return fmt.Sprintf("%s#%d", artifact.EpisodeID, artifact.Revision)
}

// ReserveReady binds an artifact to the already-admitted playback Task/Run.
// The reservation is runtime state derived from that canonical Run and is
// discarded on release or process restart.
func (s *DialogueEpisodeService) ReserveReady(ctx context.Context, episodeID string, revision int, playbackTaskID modulecore.TaskID, playbackRunID modulecore.RunID) (DialogueEpisodeArtifact, error) {
	items, err := s.ListReady(ctx)
	if err != nil {
		return DialogueEpisodeArtifact{}, err
	}
	var selected DialogueEpisodeArtifact
	found := false
	for _, item := range items {
		if item.EpisodeID == strings.TrimSpace(episodeID) && item.Revision == revision {
			selected = item
			found = true
			break
		}
	}
	if !found {
		return DialogueEpisodeArtifact{}, ErrPreparedDialogueExpired
	}
	s.readyMu.RLock()
	issuer := s.readyIssuer
	s.readyMu.RUnlock()
	if issuer == nil {
		return DialogueEpisodeArtifact{}, ErrPreparedDialogueInvalid
	}
	playbackRun, err := inspectGenerationRun(ctx, issuer, playbackTaskID, playbackRunID)
	if err != nil || playbackRun.Status != domaintask.RunStatusRunning {
		if err != nil {
			return DialogueEpisodeArtifact{}, fmt.Errorf("verify playback Run: %w", err)
		}
		return DialogueEpisodeArtifact{}, ErrPreparedDialogueExpired
	}
	owner, err := generationRunOwnerFromIssuer(issuer)
	if err != nil {
		return DialogueEpisodeArtifact{}, err
	}
	playbackTask, err := owner.Get(ctx, playbackTaskID)
	if err != nil || playbackTask.TaskID != playbackTaskID || playbackTask.Status != domaintask.StatusRunning {
		if err != nil {
			return DialogueEpisodeArtifact{}, fmt.Errorf("verify playback Task: %w", err)
		}
		return DialogueEpisodeArtifact{}, ErrPreparedDialogueExpired
	}
	s.readyMu.Lock()
	if s.playbackReservations == nil {
		s.playbackReservations = make(map[string]dialoguePlaybackReservation)
	}
	key := dialoguePlaybackReservationKey(selected)
	if existing, reserved := s.playbackReservations[key]; reserved && (existing.TaskID != playbackTaskID || existing.RunID != playbackRunID) {
		s.readyMu.Unlock()
		return DialogueEpisodeArtifact{}, ErrPreparedDialogueReserved
	}
	s.playbackReservations[key] = dialoguePlaybackReservation{TaskID: playbackTaskID, RunID: playbackRunID}
	s.readyMu.Unlock()
	return selected, nil
}

func (s *DialogueEpisodeService) ReleaseReadyReservation(artifact DialogueEpisodeArtifact, playbackTaskID modulecore.TaskID, playbackRunID modulecore.RunID) {
	if s == nil {
		return
	}
	s.readyMu.Lock()
	key := dialoguePlaybackReservationKey(artifact)
	if existing, ok := s.playbackReservations[key]; ok && existing.TaskID == playbackTaskID && existing.RunID == playbackRunID {
		delete(s.playbackReservations, key)
	}
	s.readyMu.Unlock()
}

func (s *DialogueEpisodeService) publishReadyDialogueArtifact(artifact DialogueEpisodeArtifact) {
	if s == nil || !dialogueArtifactPreparedForPlayback(artifact) {
		return
	}
	s.readyLoadMu.Lock()
	defer s.readyLoadMu.Unlock()
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	if s.readyLoadErr != nil {
		return
	}
	for index, existing := range s.readyArtifacts {
		if existing.EpisodeID == artifact.EpisodeID && existing.Revision == artifact.Revision {
			s.readyArtifacts[index] = cloneDialogueEpisode(artifact)
			s.readyLoaded = true
			return
		}
	}
	s.readyArtifacts = append(s.readyArtifacts, cloneDialogueEpisode(artifact))
	s.readyLoaded = true
}

func (o *IdleChatOrchestrator) runPreparedDialogueSession(artifact DialogueEpisodeArtifact, generation uint64) {
	if o == nil {
		return
	}
	if !dialogueArtifactPreparedForPlayback(artifact) || len(artifact.Turns) == 0 {
		o.markConversationRunFailed(generation, "prepared dialogue artifact is invalid", ErrPreparedDialogueInvalid.Error())
		o.finishTopicStockPlayback(generation)
		return
	}

	sessionID := string(modulecore.NewSessionID())
	startedAt := time.Now().In(jst)
	playbackGeneration, err := o.activateIdleSession(sessionID)
	if err != nil {
		o.markConversationRunFailed(generation, "prepared dialogue session could not start", err.Error())
		o.finishTopicStockPlayback(generation)
		return
	}
	if playbackGeneration != generation {
		o.markConversationRunFailed(generation, "prepared dialogue session owner changed", ErrPreparedDialogueExpired.Error())
		o.finishTopicStockPlayback(generation)
		return
	}
	o.mu.Lock()
	o.currentTopic = strings.TrimSpace(artifact.TopicResult.Topic)
	o.currentTopicResult = &artifact.TopicResult
	o.mu.Unlock()

	completed := false
	transcript := make([]string, 0, len(artifact.Turns))
	defer func() {
		if completed {
			o.markConversationRunSucceeded(generation, artifact.PreparedTitle+"\n"+artifact.PreparedSummary)
		}
		o.finishTopicStockPlayback(generation)
	}()

	strategy := TopicStrategy(strings.TrimSpace(artifact.TopicResult.Strategy))
	if strategy == "" {
		switch artifact.TopicResult.Category {
		case TopicCategoryDouble:
			strategy = StrategyDoubleGenre
		case TopicCategoryForecast:
			strategy = TopicStrategy("forecast")
		default:
			strategy = StrategySingleGenre
		}
	}
	if done := o.emitTopicToTimeline(sessionID, artifact.TopicResult.Topic, strategy, generation); done.Done != nil {
		select {
		case <-done.Done:
		case <-o.idleRunContext().Done():
			return
		}
	}

	for index, turn := range artifact.Turns {
		select {
		case <-o.idleRunContext().Done():
			return
		default:
		}
		if !o.isIdleSessionActive(sessionID, generation) {
			return
		}
		speaker := strings.ToLower(strings.TrimSpace(turn.Speaker))
		displayText := strings.TrimSpace(turn.DisplayText)
		speechText := strings.TrimSpace(turn.SpeechText)
		if (speaker != "mio" && speaker != "shiro") || displayText == "" {
			o.markConversationRunFailed(generation, "prepared dialogue turn is invalid", fmt.Sprintf("turn %d has invalid speaker or display text", index+1))
			return
		}
		if speechText == "" {
			speechText = displayText
		}
		turnIndex := index + 1
		messageID := newIdleChatMessageID()
		message := domaintransport.NewMessage(speaker, "user", sessionID, "", displayText)
		message.Type = domaintransport.MessageTypeIdleChat
		message.Context = preparedDialogueMessageContext(messageID, turnIndex, artifact, turn.TurnIndex)
		if !o.recordIdleMessageForGeneration(generation, sessionID, message) {
			return
		}
		transcript = append(transcript, speaker+": "+displayText)
		event := TimelineEvent{
			Type: "idlechat.message", From: speaker, To: "user", Content: displayText,
			RawContent: speechText, SessionID: sessionID, MessageID: messageID,
			TurnIndex: turnIndex, Category: artifact.TopicResult.Category, Strategy: strategy,
			ownerEpoch: generation,
		}
		done := o.emitTimelineEvent(event)
		o.waitForTTSReadyForEvent(event, done)
		if !o.isIdleSessionActive(sessionID, generation) {
			return
		}
		if index+1 < len(artifact.Turns) {
			o.waitBreak(speakerBreak)
		}
	}
	if !o.isIdleSessionActive(sessionID, generation) {
		return
	}
	o.savePreparedDialogueSummary(artifact, sessionID, generation, transcript, startedAt)
	completed = true
}

func preparedDialogueMessageContext(messageID string, turnIndex int, artifact DialogueEpisodeArtifact, sourceTurnIndex int) map[string]any {
	return map[string]any{
		"message_id":          messageID,
		"turn_index":          turnIndex,
		"prepared_episode_id": artifact.EpisodeID,
		"prepared_revision":   artifact.Revision,
		"source_turn_index":   sourceTurnIndex,
	}
}
