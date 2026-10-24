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

const (
	TopicStockPlaybackPlay     = "play"
	TopicStockPlaybackNext     = "next"
	TopicStockPlaybackPrevious = "previous"
)

var (
	ErrTopicStockEmpty         = ErrPreparedDialogueEmpty
	ErrTopicStockNotFound      = errors.New("topic stock item was not found")
	ErrTopicStockNoPrevious    = errors.New("previous topic is not available")
	ErrTopicStockPlaybackStale = errors.New("topic stock playback request became stale")
)

// TopicStockPlaybackItem is the stable Viewer-facing identity of one prepared
// item. The prepared payload stays inside CORE so Viewer cannot alter a script.
type TopicStockPlaybackItem struct {
	ID       string `json:"id"`
	Stock    string `json:"stock"`
	Label    string `json:"label"`
	Topic    string `json:"topic"`
	word     *WordPreparedTopic
	forecast *PreparedTopic
	dialogue *DialogueEpisodeArtifact
	story    *StoryEpisodeArtifact
}

type TopicStockPlaybackSnapshot struct {
	Current     *TopicStockPlaybackItem `json:"current,omitempty"`
	Position    int                     `json:"position"`
	HistorySize int                     `json:"history_size"`
	CanPrevious bool                    `json:"can_previous"`
	CanNext     bool                    `json:"can_next"`
}

func wordPlaybackID(runID modulecore.RunID) string { return "word:" + strings.TrimSpace(string(runID)) }
func forecastPlaybackID(runID modulecore.RunID) string {
	return "forecast:" + strings.TrimSpace(string(runID))
}
func storyPlaybackID(id string) string { return "story:" + strings.TrimSpace(id) }

func dialogueArtifactPlaybackID(artifact DialogueEpisodeArtifact) string {
	prefix := "word:"
	if artifact.TopicResult.Category == TopicCategoryForecast {
		prefix = "forecast:"
	}
	return prefix + strings.TrimSpace(artifact.EpisodeID) + "#" + fmt.Sprint(artifact.Revision)
}

func storyArtifactPlaybackID(artifact StoryEpisodeArtifact) string {
	return storyPlaybackID(artifact.EpisodeID) + "#" + fmt.Sprint(artifact.Revision)
}

func (o *IdleChatOrchestrator) availableTopicStockPlaybackItems() []TopicStockPlaybackItem {
	items, err := o.loadReadyTopicStockPlaybackItems()
	if err != nil {
		return nil
	}
	return items
}

func (o *IdleChatOrchestrator) loadReadyTopicStockPlaybackItems() ([]TopicStockPlaybackItem, error) {
	if o == nil {
		return nil, errors.New("idlechat is not configured")
	}
	o.mu.Lock()
	dialogueService := o.dialogueEpisodeService
	storyService := o.storyEpisodeService
	o.mu.Unlock()
	items := make([]TopicStockPlaybackItem, 0)
	if dialogueService != nil {
		artifacts, err := dialogueService.ListReady(context.Background())
		if err != nil {
			return nil, err
		}
		for _, artifact := range artifacts {
			if artifact.TopicResult.Category != TopicCategorySingle && artifact.TopicResult.Category != TopicCategoryDouble && artifact.TopicResult.Category != TopicCategoryForecast {
				continue
			}
			if artifact.TopicResult.Category == TopicCategoryForecast && !preparedForecastArtifactFresh(artifact, time.Now()) {
				continue
			}
			copy := cloneDialogueEpisode(artifact)
			label := wordTopicCategoryLabel(artifact.TopicResult.Category)
			stock := string(artifact.TopicResult.Category)
			if artifact.TopicResult.Category == TopicCategoryForecast {
				stock = "forecast"
				label = strings.TrimSpace(artifact.TopicResult.Seed.ForecastDomain)
			}
			title := strings.TrimSpace(artifact.PreparedTitle)
			if title == "" {
				title = strings.TrimSpace(artifact.TopicResult.Topic)
			}
			items = append(items, TopicStockPlaybackItem{
				ID: dialogueArtifactPlaybackID(artifact), Stock: stock,
				Label: label, Topic: title, dialogue: &copy,
			})
		}
	}
	if storyService != nil {
		for _, artifact := range storyService.Snapshot().Episodes {
			if artifact.ProductionStatus != StoryProductionReady || !artifact.Validation.Valid || !o.storyProductionRunSucceeded(artifact) {
				continue
			}
			copy := cloneStoryEpisode(artifact)
			items = append(items, TopicStockPlaybackItem{
				ID: storyArtifactPlaybackID(artifact), Stock: "story", Label: "物語",
				Topic: storyEpisodeDisplayTitle(artifact), story: &copy,
			})
		}
	}
	return items, nil
}

func preparedForecastArtifactFresh(artifact DialogueEpisodeArtifact, now time.Time) bool {
	if artifact.CreatedAt.IsZero() {
		return false
	}
	return artifact.CreatedAt.In(jst).Format("2006-01-02") == now.In(jst).Format("2006-01-02")
}

func (o *IdleChatOrchestrator) storyProductionRunSucceeded(artifact StoryEpisodeArtifact) bool {
	o.mu.Lock()
	issuer := o.runIssuer
	o.mu.Unlock()
	if issuer == nil {
		return false
	}
	ctx := context.Background()
	run, err := inspectGenerationRun(ctx, issuer, artifact.TaskID, artifact.RunID)
	if err != nil || run.Status != domaintask.RunStatusSucceeded {
		return false
	}
	owner, err := generationRunOwnerFromIssuer(issuer)
	if err != nil {
		return false
	}
	task, err := owner.Get(ctx, artifact.TaskID)
	return err == nil && task.TaskID == artifact.TaskID && task.Status == domaintask.StatusSucceeded
}

func (o *IdleChatOrchestrator) consumeTopicStockPlaybackItem(id string) (TopicStockPlaybackItem, error) {
	id = strings.TrimSpace(id)
	items, err := o.loadReadyTopicStockPlaybackItems()
	if err != nil {
		return TopicStockPlaybackItem{}, err
	}
	if len(items) == 0 {
		return TopicStockPlaybackItem{}, ErrPreparedDialogueEmpty
	}
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
		if item.dialogue != nil && strings.HasPrefix(id, "word:") && strings.TrimPrefix(id, "word:") == string(item.dialogue.RunID) {
			return item, nil
		}
		if item.dialogue != nil && strings.HasPrefix(id, "forecast:") && strings.TrimPrefix(id, "forecast:") == string(item.dialogue.RunID) {
			return item, nil
		}
		if item.story != nil && id == storyPlaybackID(item.story.EpisodeID) {
			return item, nil
		}
	}
	return TopicStockPlaybackItem{}, fmt.Errorf("%w: %s", ErrTopicStockNotFound, id)
}

func (o *IdleChatOrchestrator) selectTopicStockPlaybackItem(action, requestedID string) (TopicStockPlaybackItem, error) {
	o.topicPlaybackMu.Lock()
	defer o.topicPlaybackMu.Unlock()
	if len(o.topicPlaybackHistory) == 0 {
		o.topicPlaybackIndex = -1
	}
	action = strings.ToLower(strings.TrimSpace(action))
	if action == TopicStockPlaybackPrevious && o.topicPlaybackIndex <= 0 {
		return TopicStockPlaybackItem{}, ErrTopicStockNoPrevious
	}
	if action != TopicStockPlaybackPrevious && action != TopicStockPlaybackPlay && action != TopicStockPlaybackNext {
		return TopicStockPlaybackItem{}, fmt.Errorf("unsupported topic stock playback action %q", action)
	}
	items, err := o.loadReadyTopicStockPlaybackItems()
	if err != nil {
		return TopicStockPlaybackItem{}, err
	}
	if len(items) == 0 {
		return TopicStockPlaybackItem{}, ErrPreparedDialogueEmpty
	}
	resolve := func(id string) (TopicStockPlaybackItem, error) {
		for _, candidate := range items {
			aliasMatches := candidate.dialogue != nil &&
				((strings.HasPrefix(id, "word:") && strings.TrimPrefix(id, "word:") == string(candidate.dialogue.RunID)) ||
					(strings.HasPrefix(id, "forecast:") && strings.TrimPrefix(id, "forecast:") == string(candidate.dialogue.RunID)))
			if candidate.ID == id || aliasMatches || (candidate.story != nil && id == storyPlaybackID(candidate.story.EpisodeID)) {
				return candidate, nil
			}
		}
		return TopicStockPlaybackItem{}, fmt.Errorf("%w: %s", ErrTopicStockNotFound, id)
	}
	switch action {
	case TopicStockPlaybackPrevious:
		if o.topicPlaybackIndex <= 0 {
			return TopicStockPlaybackItem{}, ErrTopicStockNoPrevious
		}
		nextIndex := o.topicPlaybackIndex - 1
		item, err := resolve(o.topicPlaybackHistory[nextIndex].ID)
		if err != nil {
			return TopicStockPlaybackItem{}, err
		}
		o.topicPlaybackIndex = nextIndex
		return item, nil
	case TopicStockPlaybackPlay:
		requestedID = strings.TrimSpace(requestedID)
		if requestedID != "" {
			for index, item := range o.topicPlaybackHistory {
				if item.ID == requestedID {
					resolved, err := resolve(item.ID)
					if err != nil {
						return TopicStockPlaybackItem{}, err
					}
					o.topicPlaybackIndex = index
					return resolved, nil
				}
			}
		} else if o.topicPlaybackIndex >= 0 && o.topicPlaybackIndex < len(o.topicPlaybackHistory) {
			return resolve(o.topicPlaybackHistory[o.topicPlaybackIndex].ID)
		}
	case TopicStockPlaybackNext:
		if next := o.topicPlaybackIndex + 1; next >= 0 && next < len(o.topicPlaybackHistory) {
			item, err := resolve(o.topicPlaybackHistory[next].ID)
			if err != nil {
				return TopicStockPlaybackItem{}, err
			}
			o.topicPlaybackIndex = next
			return item, nil
		}
	default:
		return TopicStockPlaybackItem{}, fmt.Errorf("unsupported topic stock playback action %q", action)
	}

	if requestedID == "" {
		seen := make(map[string]struct{}, len(o.topicPlaybackHistory))
		for _, item := range o.topicPlaybackHistory {
			seen[item.ID] = struct{}{}
		}
		for _, item := range items {
			if _, played := seen[item.ID]; !played {
				requestedID = item.ID
				break
			}
		}
	}
	if requestedID == "" {
		return TopicStockPlaybackItem{}, ErrTopicStockEmpty
	}
	item, err := resolve(requestedID)
	if err != nil {
		return TopicStockPlaybackItem{}, err
	}
	if o.topicPlaybackIndex+1 < len(o.topicPlaybackHistory) {
		o.topicPlaybackHistory = append([]TopicStockPlaybackItem(nil), o.topicPlaybackHistory[:o.topicPlaybackIndex+1]...)
	}
	o.topicPlaybackHistory = append(o.topicPlaybackHistory, item)
	o.topicPlaybackIndex = len(o.topicPlaybackHistory) - 1
	return item, nil
}

func (o *IdleChatOrchestrator) TopicStockPlaybackSnapshot() TopicStockPlaybackSnapshot {
	if o == nil {
		return TopicStockPlaybackSnapshot{}
	}
	o.topicPlaybackMu.Lock()
	defer o.topicPlaybackMu.Unlock()
	snapshot := TopicStockPlaybackSnapshot{Position: o.topicPlaybackIndex + 1, HistorySize: len(o.topicPlaybackHistory)}
	if o.topicPlaybackIndex >= 0 && o.topicPlaybackIndex < len(o.topicPlaybackHistory) {
		copy := o.topicPlaybackHistory[o.topicPlaybackIndex]
		snapshot.Current = &copy
		snapshot.CanPrevious = o.topicPlaybackIndex > 0
		snapshot.CanNext = o.topicPlaybackIndex+1 < len(o.topicPlaybackHistory)
	}
	if !snapshot.CanNext {
		seen := make(map[string]struct{}, len(o.topicPlaybackHistory))
		for _, item := range o.topicPlaybackHistory {
			seen[item.ID] = struct{}{}
		}
		for _, item := range o.availableTopicStockPlaybackItems() {
			if _, played := seen[item.ID]; !played {
				snapshot.CanNext = true
				break
			}
		}
	}
	return snapshot
}

func (o *IdleChatOrchestrator) rollbackTopicPlaybackSelection(item TopicStockPlaybackItem, priorIndex, priorSize, selectedIndex int) {
	o.topicPlaybackMu.Lock()
	defer o.topicPlaybackMu.Unlock()
	if len(o.topicPlaybackHistory) == priorSize+1 && len(o.topicPlaybackHistory) > 0 && o.topicPlaybackHistory[len(o.topicPlaybackHistory)-1].ID == item.ID {
		o.topicPlaybackHistory = o.topicPlaybackHistory[:priorSize]
		o.topicPlaybackIndex = priorIndex
		return
	}
	if len(o.topicPlaybackHistory) == priorSize && o.topicPlaybackIndex == selectedIndex {
		o.topicPlaybackIndex = priorIndex
	}
}

func (o *IdleChatOrchestrator) StartTopicStockPlayback(action, requestedID string) (TopicStockPlaybackSnapshot, error) {
	return o.StartTopicStockPlaybackWithGuard(action, requestedID, nil)
}

// StartTopicStockPlaybackWithGuard checks a caller-owned policy/revision guard
// after ready-artifact selection and again after reservation, immediately
// before launching playback. The callback must be read-only and repeatable.
// A failed check rejects this request, rolls back its selection, and finalizes
// any Task/Run and artifact reservation created for it.
func (o *IdleChatOrchestrator) StartTopicStockPlaybackWithGuard(action, requestedID string, beforeActivation func() error) (TopicStockPlaybackSnapshot, error) {
	if o == nil {
		return TopicStockPlaybackSnapshot{}, errors.New("idlechat is not configured")
	}
	o.mu.Lock()
	participants := len(o.participants)
	o.mu.Unlock()
	if participants < 2 {
		return o.TopicStockPlaybackSnapshot(), errors.New("idlechat requires at least 2 participants")
	}

	// Reserve the generation lease before selecting or consuming stock. Stop
	// closes this gate first, so a stopped service cannot mutate playback state
	// or create an owner Task while an admitted playback is being joined.
	lease, admitted := o.acquireGenerationWork()
	if !admitted {
		return o.TopicStockPlaybackSnapshot(), errIdleChatGenerationWorkClosed
	}
	transferred := false
	defer func() {
		if !transferred {
			lease.release()
		}
	}()

	o.topicPlaybackMu.Lock()
	priorIndex, priorSize := o.topicPlaybackIndex, len(o.topicPlaybackHistory)
	o.topicPlaybackMu.Unlock()
	item, err := o.selectTopicStockPlaybackItem(action, requestedID)
	if err != nil {
		return o.TopicStockPlaybackSnapshot(), err
	}
	o.topicPlaybackMu.Lock()
	selectedIndex := o.topicPlaybackIndex
	o.topicPlaybackMu.Unlock()
	if beforeActivation != nil {
		if err := beforeActivation(); err != nil {
			o.rollbackTopicPlaybackSelection(item, priorIndex, priorSize, selectedIndex)
			return o.TopicStockPlaybackSnapshot(), errors.Join(ErrTopicStockPlaybackStale, err)
		}
	}
	if o.IsChatActive() || o.IsManualMode() {
		o.Interrupt("topic_stock_" + strings.ToLower(strings.TrimSpace(action)))
	}

	o.mu.Lock()
	o.disabled = false
	o.manualMode = false
	o.chatActive = true
	o.sessionMode = item.Stock
	o.currentTopic = item.Topic
	o.sessionContext = ""
	o.mu.Unlock()
	generation, err := o.startIdleRun()
	if err != nil {
		o.resetIdleSessionState(generation)
		o.rollbackTopicPlaybackSelection(item, priorIndex, priorSize, selectedIndex)
		return o.TopicStockPlaybackSnapshot(), fmt.Errorf("start topic stock conversation: %w", err)
	}
	var playbackTaskID modulecore.TaskID
	var playbackRunID modulecore.RunID
	o.mu.Lock()
	playbackTaskID = o.activeTaskID
	playbackRunID = o.activeRunID
	o.lastActivity = time.Now()
	dialogueService := o.dialogueEpisodeService
	o.mu.Unlock()
	if item.dialogue != nil {
		if dialogueService == nil {
			err = ErrPreparedDialogueInvalid
		} else {
			var reserved DialogueEpisodeArtifact
			reserved, err = dialogueService.ReserveReady(o.idleRunContext(), item.dialogue.EpisodeID, item.dialogue.Revision, playbackTaskID, playbackRunID)
			if err == nil {
				item.dialogue = &reserved
			}
		}
		if err != nil {
			o.resetIdleSessionState(generation)
			finalizeErr := o.cancelIdleRunIfGeneration(generation)
			o.rollbackTopicPlaybackSelection(item, priorIndex, priorSize, selectedIndex)
			return o.TopicStockPlaybackSnapshot(), errors.Join(fmt.Errorf("reserve prepared dialogue playback: %w", err), finalizeErr)
		}
	}
	if beforeActivation != nil {
		if err := beforeActivation(); err != nil {
			if item.dialogue != nil && dialogueService != nil {
				dialogueService.ReleaseReadyReservation(*item.dialogue, playbackTaskID, playbackRunID)
			}
			o.resetIdleSessionState(generation)
			finalizeErr := o.cancelIdleRunIfGeneration(generation)
			o.rollbackTopicPlaybackSelection(item, priorIndex, priorSize, selectedIndex)
			return o.TopicStockPlaybackSnapshot(), errors.Join(ErrTopicStockPlaybackStale, err, finalizeErr)
		}
	}
	o.launchGenerationWork(lease, func() {
		o.runTopicStockPlayback(item, generation, playbackTaskID, playbackRunID, dialogueService)
	})
	transferred = true
	return o.TopicStockPlaybackSnapshot(), nil
}

func (o *IdleChatOrchestrator) runTopicStockPlayback(item TopicStockPlaybackItem, generation uint64, playbackTaskID modulecore.TaskID, playbackRunID modulecore.RunID, dialogueService *DialogueEpisodeService) {
	if item.story != nil {
		o.runPreparedStorySessionForPlayback(*item.story, generation, playbackTaskID, playbackRunID)
		return
	}
	if item.dialogue != nil {
		defer dialogueService.ReleaseReadyReservation(*item.dialogue, playbackTaskID, playbackRunID)
		o.runPreparedDialogueSession(*item.dialogue, generation)
		return
	}
	defer o.finishTopicStockPlayback(generation)
	o.markConversationRunFailed(generation, "prepared playback item is missing its artifact", ErrPreparedDialogueInvalid.Error())
}

func (o *IdleChatOrchestrator) finishTopicStockPlayback(generation uint64) {
	o.mu.Lock()
	if o.activeGeneration == generation {
		o.chatActive = false
		o.sessionMode = ""
		o.currentTopic = ""
		o.sessionContext = ""
		o.activeSessionID = ""
		o.currentTopicResult = nil
		o.currentDialoguePlan = nil
		o.currentDialogueState = nil
	}
	o.lastActivity = time.Now()
	o.mu.Unlock()
	if err := o.cancelIdleRunIfGeneration(generation); err != nil {
		log.Printf("[IdleChat] topic stock conversation finalization failed: %v", err)
	}
}

func (o *IdleChatOrchestrator) forecastTopicPublicationPending(domain string, runID modulecore.RunID) (bool, error) {
	store := o.generationCheckpointStore()
	if err := store.LoadError(); err != nil {
		return true, err
	}
	cp, found := store.Get("forecast:" + domain)
	return found && cp.RunID == runID, nil
}
