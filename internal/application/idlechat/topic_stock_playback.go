package idlechat

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	TopicStockPlaybackPlay     = "play"
	TopicStockPlaybackNext     = "next"
	TopicStockPlaybackPrevious = "previous"
)

var (
	ErrTopicStockEmpty      = errors.New("topic stock has no playable items")
	ErrTopicStockNotFound   = errors.New("topic stock item was not found")
	ErrTopicStockNoPrevious = errors.New("previous topic is not available")
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
	story    *StoryEpisodeArtifact
}

type TopicStockPlaybackSnapshot struct {
	Current     *TopicStockPlaybackItem `json:"current,omitempty"`
	Position    int                     `json:"position"`
	HistorySize int                     `json:"history_size"`
	CanPrevious bool                    `json:"can_previous"`
	CanNext     bool                    `json:"can_next"`
}

func wordPlaybackID(id string) string     { return "word:" + strings.TrimSpace(id) }
func forecastPlaybackID(id string) string { return "forecast:" + strings.TrimSpace(id) }
func storyPlaybackID(id string) string    { return "story:" + strings.TrimSpace(id) }

func (o *IdleChatOrchestrator) availableTopicStockPlaybackItems() []TopicStockPlaybackItem {
	if o == nil {
		return nil
	}
	items := make([]TopicStockPlaybackItem, 0)
	for _, category := range o.WordTopicStockSnapshot().Categories {
		for _, prepared := range category.Topics {
			copy := prepared
			items = append(items, TopicStockPlaybackItem{
				ID: wordPlaybackID(prepared.GenerationID), Stock: string(prepared.Category),
				Label: wordTopicCategoryLabel(prepared.Category), Topic: prepared.Topic, word: &copy,
			})
		}
	}
	for _, domain := range o.ForecastTopicStockSnapshot().Domains {
		for _, prepared := range domain.Topics {
			copy := prepared
			items = append(items, TopicStockPlaybackItem{
				ID: forecastPlaybackID(prepared.GenerationID), Stock: "forecast",
				Label: domain.Name, Topic: prepared.Topic, forecast: &copy,
			})
		}
	}
	for _, artifact := range o.StoryEpisodeStockSnapshot().Episodes {
		if artifact.ProductionStatus != StoryProductionReady || !artifact.Validation.Valid {
			continue
		}
		copy := cloneStoryEpisode(artifact)
		items = append(items, TopicStockPlaybackItem{
			ID: storyPlaybackID(artifact.EpisodeID), Stock: "story", Label: "物語",
			Topic: storyEpisodeDisplayTitle(artifact), story: &copy,
		})
	}
	return items
}

func (o *IdleChatOrchestrator) consumeTopicStockPlaybackItem(id string) (TopicStockPlaybackItem, error) {
	id = strings.TrimSpace(id)
	o.mu.Lock()
	wordStock := o.wordTopicStock
	forecastStock := o.topicStockBuf
	storyService := o.storyEpisodeService
	o.mu.Unlock()
	switch {
	case strings.HasPrefix(id, "word:"):
		if wordStock != nil {
			if prepared := wordStock.takeByGenerationID(strings.TrimPrefix(id, "word:")); prepared != nil {
				return TopicStockPlaybackItem{ID: id, Stock: string(prepared.Category), Label: wordTopicCategoryLabel(prepared.Category), Topic: prepared.Topic, word: prepared}, nil
			}
		}
	case strings.HasPrefix(id, "forecast:"):
		if forecastStock != nil {
			if prepared := forecastStock.takeByGenerationID(strings.TrimPrefix(id, "forecast:")); prepared != nil {
				return TopicStockPlaybackItem{ID: id, Stock: "forecast", Label: prepared.Domain.Name, Topic: prepared.Topic, forecast: prepared}, nil
			}
		}
	case strings.HasPrefix(id, "story:"):
		if storyService != nil {
			if artifact, ok := storyService.Episode(strings.TrimPrefix(id, "story:")); ok && artifact.ProductionStatus == StoryProductionReady && artifact.Validation.Valid {
				return TopicStockPlaybackItem{ID: id, Stock: "story", Label: "物語", Topic: storyEpisodeDisplayTitle(artifact), story: &artifact}, nil
			}
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
	switch action {
	case TopicStockPlaybackPrevious:
		if o.topicPlaybackIndex <= 0 {
			return TopicStockPlaybackItem{}, ErrTopicStockNoPrevious
		}
		o.topicPlaybackIndex--
		return o.topicPlaybackHistory[o.topicPlaybackIndex], nil
	case TopicStockPlaybackPlay:
		requestedID = strings.TrimSpace(requestedID)
		if requestedID != "" {
			for index, item := range o.topicPlaybackHistory {
				if item.ID == requestedID {
					o.topicPlaybackIndex = index
					return item, nil
				}
			}
		} else if o.topicPlaybackIndex >= 0 && o.topicPlaybackIndex < len(o.topicPlaybackHistory) {
			return o.topicPlaybackHistory[o.topicPlaybackIndex], nil
		}
	case TopicStockPlaybackNext:
		if next := o.topicPlaybackIndex + 1; next >= 0 && next < len(o.topicPlaybackHistory) {
			o.topicPlaybackIndex = next
			return o.topicPlaybackHistory[next], nil
		}
	default:
		return TopicStockPlaybackItem{}, fmt.Errorf("unsupported topic stock playback action %q", action)
	}

	if requestedID == "" {
		seen := make(map[string]struct{}, len(o.topicPlaybackHistory))
		for _, item := range o.topicPlaybackHistory {
			seen[item.ID] = struct{}{}
		}
		for _, item := range o.availableTopicStockPlaybackItems() {
			if _, played := seen[item.ID]; !played {
				requestedID = item.ID
				break
			}
		}
	}
	if requestedID == "" {
		return TopicStockPlaybackItem{}, ErrTopicStockEmpty
	}
	item, err := o.consumeTopicStockPlaybackItem(requestedID)
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

func (o *IdleChatOrchestrator) StartTopicStockPlayback(action, requestedID string) (TopicStockPlaybackSnapshot, error) {
	if o == nil {
		return TopicStockPlaybackSnapshot{}, errors.New("idlechat is not configured")
	}
	o.mu.Lock()
	participants := len(o.participants)
	o.mu.Unlock()
	if participants < 2 {
		return o.TopicStockPlaybackSnapshot(), errors.New("idlechat requires at least 2 participants")
	}

	item, err := o.selectTopicStockPlaybackItem(action, requestedID)
	if err != nil {
		return o.TopicStockPlaybackSnapshot(), err
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
	generation := o.beginIdleRunLocked()
	o.lastActivity = time.Now()
	o.mu.Unlock()
	go o.runTopicStockPlayback(item, generation)
	return o.TopicStockPlaybackSnapshot(), nil
}

func (o *IdleChatOrchestrator) runTopicStockPlayback(item TopicStockPlaybackItem, generation uint64) {
	if item.story != nil {
		o.RunPreparedStorySession(*item.story)
		return
	}
	defer o.finishTopicStockPlayback(generation)
	if item.forecast != nil {
		o.runForecastDomainSession(item.forecast.Domain, *item.forecast)
		return
	}
	if item.word != nil {
		strategy := StrategySingleGenre
		if item.word.Category == TopicCategoryDouble {
			strategy = StrategyDoubleGenre
		}
		o.runChatSession(strategy, wordTopicResultFromItem(*item.word))
	}
}

func (o *IdleChatOrchestrator) finishTopicStockPlayback(generation uint64) {
	o.mu.Lock()
	if o.activeGeneration == generation {
		o.chatActive = false
		o.sessionMode = ""
		o.currentTopic = ""
		o.sessionContext = ""
		o.activeSessionID = ""
	}
	o.lastActivity = time.Now()
	o.mu.Unlock()
	o.cancelIdleRunIfGeneration(generation)
}
