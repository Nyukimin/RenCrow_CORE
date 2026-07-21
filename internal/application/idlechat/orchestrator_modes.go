package idlechat

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
)

func (o *IdleChatOrchestrator) Start() {
	o.mu.Lock()
	newsSourceConfig := o.newsSourceConfig
	newsSourceConfig.RedditCommunities = append([]string(nil), newsSourceConfig.RedditCommunities...)
	newsSourceConfig.XQueries = append([]XNewsQuery(nil), newsSourceConfig.XQueries...)
	o.mu.Unlock()

	// 起動時に外部シード取得（非同期）
	go func() {
		if err := fetchDailySeeds(newsSourceConfig); err != nil {
			log.Printf("[IdleChat] Daily seeds fetch failed: %v", err)
			return
		}
		o.enrichCurrentDailySeeds()
	}()
	o.startDailySeedRefreshScheduler(newsSourceConfig)

	o.wg.Add(1)
	go o.monitorLoop()
	log.Printf("[IdleChat] Started (participants=%v, interval=%s, maxTurns=%d)",
		o.participants, o.interval, o.maxTurns)
}

func (o *IdleChatOrchestrator) SetIntervalSeconds(seconds int) {
	if seconds < 1 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.interval = time.Duration(seconds) * time.Second
	o.intervalMin = (seconds + 59) / 60
}

// Stop はIdleChatを停止

func (o *IdleChatOrchestrator) Stop() {
	o.cancel()
	o.cancelIdleRun()
	o.wg.Wait()
	log.Println("[IdleChat] Stopped")
}

// NotifyActivity はタスク到着を通知（雑談セッションを中断）

func (o *IdleChatOrchestrator) NotifyActivity() {
	o.Interrupt("activity")
}

// SetChatBusy はChat(mio)の活性状態を更新する。

func (o *IdleChatOrchestrator) SetChatBusy(busy bool) {
	o.mu.Lock()
	o.chatBusy = busy
	o.mu.Unlock()
	if busy {
		o.Interrupt("chat_busy")
	}
}

// SetWorkerBusy はWorker(shiro/coder)の活性状態を更新する。

func (o *IdleChatOrchestrator) SetWorkerBusy(busy bool) {
	o.mu.Lock()
	o.workerBusy = busy
	o.mu.Unlock()
	if busy {
		o.Interrupt("worker_busy")
	}
}

func (o *IdleChatOrchestrator) SetExternalLLMBusyFunc(fn func() bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.externalLLMBusy = fn
}

// StartManualMode starts idle chat mode immediately.

func (o *IdleChatOrchestrator) StopManualMode() {
	o.stopAndDisable("manual_stop")
}

func (o *IdleChatOrchestrator) Interrupt(reason string) {
	o.interruptLockedWithReason(reason)
}

func (o *IdleChatOrchestrator) interruptLockedWithReason(reason string) {
	o.mu.Lock()
	cancel := o.runCancel
	if o.manualMode || o.chatActive {
		log.Printf("[IdleChat] Interrupted: reason=%s generation=%d session=%s", strings.TrimSpace(reason), o.activeGeneration, o.activeSessionID)
	}
	o.manualMode = false
	o.chatActive = false
	o.sessionMode = ""
	o.currentTopic = ""
	o.sessionContext = ""
	o.lastActivity = time.Now()
	if o.activeSessionID != "" {
		if o.interruptedSessions == nil {
			o.interruptedSessions = make(map[string]struct{})
		}
		o.interruptedSessions[o.activeSessionID] = struct{}{}
	}
	o.activeGeneration++
	o.watchdogStage = "interrupted"
	o.watchdogDetail = strings.TrimSpace(reason)
	o.watchdogFrom = ""
	o.watchdogTo = ""
	o.watchdogMessageID = ""
	o.watchdogTurnIndex = 0
	o.watchdogUpdatedAt = time.Now().UTC()
	o.activeSessionID = ""
	o.runCancel = nil
	o.runCtx = o.ctx
	o.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (o *IdleChatOrchestrator) stopAndDisable(reason string) {
	o.mu.Lock()
	o.disabled = true
	o.mu.Unlock()
	o.interruptLockedWithReason(reason)
}

func (o *IdleChatOrchestrator) beginIdleRunLocked() uint64 {
	if o.runCancel != nil {
		o.runCancel()
	}
	o.activeGeneration++
	o.runCtx, o.runCancel = context.WithCancel(o.ctx)
	o.watchdogStage = "run_started"
	o.watchdogDetail = ""
	o.watchdogFrom = ""
	o.watchdogTo = ""
	o.watchdogMessageID = ""
	o.watchdogTurnIndex = 0
	o.watchdogUpdatedAt = time.Now().UTC()
	return o.activeGeneration
}

func (o *IdleChatOrchestrator) cancelIdleRun() {
	o.mu.Lock()
	cancel := o.runCancel
	o.runCancel = nil
	o.runCtx = o.ctx
	o.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (o *IdleChatOrchestrator) cancelIdleRunIfGeneration(generation uint64) {
	o.mu.Lock()
	if generation != 0 && o.activeGeneration != generation {
		o.mu.Unlock()
		return
	}
	cancel := o.runCancel
	o.runCancel = nil
	o.runCtx = o.ctx
	o.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (o *IdleChatOrchestrator) idleRunContext() context.Context {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.runCtx != nil {
		return llm.WithBusySource(o.runCtx, "idlechat")
	}
	return llm.WithBusySource(o.ctx, "idlechat")
}

func (o *IdleChatOrchestrator) activateIdleSession(sessionID string) uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.runCtx == nil {
		o.beginIdleRunLocked()
	}
	o.activeSessionID = strings.TrimSpace(sessionID)
	if o.interruptedSessions != nil {
		delete(o.interruptedSessions, o.activeSessionID)
	}
	return o.activeGeneration
}

func (o *IdleChatOrchestrator) isIdleSessionActive(sessionID string, generation uint64) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if generation != 0 && generation != o.activeGeneration {
		return false
	}
	if strings.TrimSpace(sessionID) != "" && o.activeSessionID != "" && strings.TrimSpace(sessionID) != o.activeSessionID {
		return false
	}
	return o.chatActive
}

func (o *IdleChatOrchestrator) isInterruptedSession(sessionID string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if strings.TrimSpace(sessionID) == "" || o.interruptedSessions == nil {
		return false
	}
	_, ok := o.interruptedSessions[strings.TrimSpace(sessionID)]
	return ok
}

// IsManualMode returns whether manual idle chat mode is enabled.

func idleChatPendingTopic(mode string) string {
	switch strings.TrimSpace(mode) {
	case "forecast":
		return "未来展望のお題を準備中"
	case "story", "story-simple":
		return "物語のお題を準備中"
	default:
		return "今日のお題を準備中"
	}
}

// GetHistory returns newest-first session summaries.

func (o *IdleChatOrchestrator) GetHistory(limit int) []SessionSummary {
	o.mu.Lock()
	store := o.topicStore
	if store != nil {
		o.mu.Unlock()
		return store.GetRecent(limit)
	}
	defer o.mu.Unlock()
	if limit <= 0 || limit > len(o.history) {
		limit = len(o.history)
	}
	out := make([]SessionSummary, 0, limit)
	for i := len(o.history) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, o.history[i])
	}
	return out
}

func (o *IdleChatOrchestrator) ActiveSessionTranscript(limit int) (string, []ActiveTranscriptEntry) {
	o.mu.Lock()
	sessionID := strings.TrimSpace(o.activeSessionID)
	memory := o.memory
	o.mu.Unlock()
	if sessionID == "" || memory == nil {
		return sessionID, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	entries := memory.GetUnifiedView(0)
	out := make([]ActiveTranscriptEntry, 0, limit)
	for _, entry := range entries {
		msg := entry.Message
		if strings.TrimSpace(msg.SessionID) != sessionID || msg.Type != domaintransport.MessageTypeIdleChat {
			continue
		}
		messageID, turnIndex := idleChatMessageMetadata(msg, len(out)+1)
		timestamp := strings.TrimSpace(msg.Timestamp)
		if timestamp == "" && !entry.Timestamp.IsZero() {
			timestamp = entry.Timestamp.In(jst).Format(time.RFC3339)
		}
		out = append(out, ActiveTranscriptEntry{
			Type:      "idlechat.message",
			From:      msg.From,
			To:        msg.To,
			Content:   msg.Content,
			SessionID: sessionID,
			MessageID: messageID,
			TurnIndex: turnIndex,
			Timestamp: timestamp,
		})
		if len(out) >= limit {
			break
		}
	}
	return sessionID, out
}

func idleChatMessageMetadata(msg domaintransport.Message, fallbackIndex int) (string, int) {
	turnIndex := fallbackIndex
	if turnIndex < 1 {
		turnIndex = 1
	}
	if msg.Context != nil {
		switch v := msg.Context["turn_index"].(type) {
		case int:
			turnIndex = v
		case int64:
			turnIndex = int(v)
		case float64:
			turnIndex = int(v)
		}
		if turnIndex < 1 {
			turnIndex = fallbackIndex
			if turnIndex < 1 {
				turnIndex = 1
			}
		}
		if id, ok := msg.Context["message_id"].(string); ok && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id), turnIndex
		}
	}
	return idleChatMessageID(msg.SessionID, turnIndex), turnIndex
}

func (o *IdleChatOrchestrator) getHistoricalTitleThemes(limit int) []string {
	history := o.GetHistory(limit)
	if len(history) == 0 {
		return nil
	}
	themes := make([]string, 0, len(history))
	seen := make(map[string]struct{}, len(history))
	for _, item := range history {
		theme := themeFromSummaryTitle(item.Title)
		if theme == "" {
			continue
		}
		key := normalizeLoopText(theme)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		themes = append(themes, theme)
	}
	return themes
}

func themeFromSummaryTitle(title string) string {
	s := strings.TrimSpace(title)
	if s == "" {
		return ""
	}
	if idx := strings.Index(s, "日の"); idx >= 0 {
		s = strings.TrimSpace(s[idx+len("日の"):])
	}
	for _, suffix := range []string{"の話題まとめ", "のまとめ", "まとめ"} {
		s = strings.TrimSpace(strings.TrimSuffix(s, suffix))
	}
	if strings.HasPrefix(s, "[") {
		if end := strings.Index(s, "]"); end >= 0 {
			s = strings.TrimSpace(s[end+1:])
		}
	}
	return normalizeIdleTopic(s, false)
}

func (o *IdleChatOrchestrator) StartManualMode() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.participants) < 2 {
		return fmt.Errorf("idlechat requires at least 2 participants")
	}
	o.disabled = false
	o.manualMode = true
	o.lastActivity = time.Now()
	log.Println("[IdleChat] Manual mode started")
	return nil
}

// StartForecastMode switches from manual idlechat into forecast mode immediately.

func (o *IdleChatOrchestrator) StartForecastMode() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.participants) < 2 {
		return fmt.Errorf("idlechat requires at least 2 participants")
	}
	if o.chatActive {
		return fmt.Errorf("chat session already active")
	}
	o.disabled = false
	o.manualMode = false
	o.chatActive = true
	o.sessionMode = "forecast"
	o.currentTopic = idleChatPendingTopic("forecast")
	o.sessionContext = ""
	o.beginIdleRunLocked()
	o.lastActivity = time.Now()
	log.Println("[Forecast] Forecast mode started")
	return nil
}

// StartStoryMode switches from manual idlechat into story mode immediately.

func (o *IdleChatOrchestrator) StartStoryMode() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.participants) < 2 {
		return fmt.Errorf("idlechat requires at least 2 participants")
	}
	if o.chatActive {
		return fmt.Errorf("chat session already active")
	}
	o.disabled = false
	o.manualMode = false
	o.chatActive = true
	o.sessionMode = "story"
	o.currentTopic = idleChatPendingTopic("story")
	o.sessionContext = ""
	o.beginIdleRunLocked()
	o.lastActivity = time.Now()
	log.Println("[Story] Story mode started")
	return nil
}

// StopManualMode stops idle chat mode and interrupts an ongoing session.

func (o *IdleChatOrchestrator) IsManualMode() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.manualMode
}

// IsChatActive は雑談セッションが進行中かを返す

func (o *IdleChatOrchestrator) IsChatActive() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.chatActive
}

func (o *IdleChatOrchestrator) IsDisabled() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.disabled
}

// CurrentMode returns the current idlechat/forecast mode.

func (o *IdleChatOrchestrator) CurrentMode() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.chatActive && o.sessionMode != "" {
		return o.sessionMode
	}
	if o.manualMode {
		return "manual"
	}
	return ""
}

// CurrentTopic は現在のIdleChatトピックを返す。

func (o *IdleChatOrchestrator) CurrentTopic() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if strings.TrimSpace(o.currentTopic) == "" && o.chatActive {
		return idleChatPendingTopic(o.sessionMode)
	}
	return o.currentTopic
}
