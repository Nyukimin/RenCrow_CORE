package idlechat

import (
	"context"
	"fmt"
	"log"
	"strings"

	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type idleEventPublicationToken struct {
	traceID            modulecore.TraceID
	sessionID          modulecore.SessionID
	runtimeEpoch       string
	playbackGeneration uint64
	ctx                context.Context
}

func (o *IdleChatOrchestrator) clearIdleEventPublicationLocked() {
	o.idleEventPublicationToken.Store(nil)
}

// EventContext returns the live owner context for one currently accepted
// IdleChat event. Runtime relays use it to abandon a binding wait when the
// foreground stop invalidates this generation.
func (o *IdleChatOrchestrator) EventContext(traceID modulecore.TraceID, sessionID modulecore.SessionID) (context.Context, bool) {
	if o == nil || traceID.Validate() != nil || sessionID.Validate() != nil {
		return nil, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	ctx := o.runCtx
	if !o.chatActive || ctx == nil || ctx.Err() != nil || o.activeTraceID != traceID ||
		o.activeSessionID != string(sessionID) || o.activeTraceSessionID != string(sessionID) {
		return nil, false
	}
	token := o.idleEventPublicationToken.Load()
	if token == nil || token.traceID != traceID || token.sessionID != sessionID {
		o.idleEventPublicationToken.Store(&idleEventPublicationToken{
			traceID: traceID, sessionID: sessionID, runtimeEpoch: o.runtimeEpoch,
			playbackGeneration: o.activeGeneration, ctx: ctx,
		})
	}
	return ctx, true
}

// PlaybackEventContext verifies the immutable playback generation attached to
// one IdleChat timeline event before the relay begins any durable or live work.
func (o *IdleChatOrchestrator) PlaybackEventContext(traceID modulecore.TraceID, sessionID modulecore.SessionID, runtimeEpoch string, generation uint64) (context.Context, bool) {
	if o == nil || traceID.Validate() != nil || sessionID.Validate() != nil || strings.TrimSpace(runtimeEpoch) == "" || generation == 0 {
		return nil, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	ctx := o.runCtx
	if runtimeEpoch != o.runtimeEpoch || generation != o.activeGeneration || !o.chatActive || ctx == nil || ctx.Err() != nil ||
		o.activeTraceID != traceID || o.activeSessionID != string(sessionID) || o.activeTraceSessionID != string(sessionID) {
		return nil, false
	}
	token := o.idleEventPublicationToken.Load()
	if token == nil || token.traceID != traceID || token.sessionID != sessionID || token.runtimeEpoch != runtimeEpoch || token.playbackGeneration != generation {
		o.idleEventPublicationToken.Store(&idleEventPublicationToken{
			traceID: traceID, sessionID: sessionID, runtimeEpoch: runtimeEpoch,
			playbackGeneration: generation, ctx: ctx,
		})
	}
	return ctx, true
}

// IsCurrentEvent rechecks the exact trace/session owner immediately before
// runtime relay acceptance.
func (o *IdleChatOrchestrator) IsCurrentEvent(traceID modulecore.TraceID, sessionID modulecore.SessionID) bool {
	_, ok := o.EventContext(traceID, sessionID)
	return ok
}

func (o *IdleChatOrchestrator) IsCurrentPlaybackEvent(traceID modulecore.TraceID, sessionID modulecore.SessionID, runtimeEpoch string, generation uint64) bool {
	_, ok := o.PlaybackEventContext(traceID, sessionID, runtimeEpoch, generation)
	return ok
}

// PublishIfCurrentEvent atomically accepts presentation against owner
// invalidation, then runs projection outside the owner lock. A stop that clears
// the token first wins; an event whose compare-and-swap succeeds first was
// accepted before the stop transition.
func (o *IdleChatOrchestrator) PublishIfCurrentEvent(traceID modulecore.TraceID, sessionID modulecore.SessionID, publish func() error) (bool, error) {
	if o == nil || traceID.Validate() != nil || sessionID.Validate() != nil {
		return false, nil
	}
	if publish == nil {
		return false, fmt.Errorf("idlechat live publication callback is required")
	}
	token := o.idleEventPublicationToken.Load()
	if token == nil || token.traceID != traceID || token.sessionID != sessionID || token.ctx == nil || token.ctx.Err() != nil ||
		!o.idleEventPublicationToken.CompareAndSwap(token, token) {
		return false, nil
	}
	if err := publish(); err != nil {
		return false, err
	}
	return true, nil
}

// PublishIfCurrentPlaybackEvent is the relay's final RAM admission gate. The
// generation token compare-and-swap linearizes acceptance against stop's token
// invalidation; clients also receive the immutable epoch/generation tuple.
func (o *IdleChatOrchestrator) PublishIfCurrentPlaybackEvent(traceID modulecore.TraceID, sessionID modulecore.SessionID, runtimeEpoch string, generation uint64, publish func() error) (bool, error) {
	if o == nil || traceID.Validate() != nil || sessionID.Validate() != nil || strings.TrimSpace(runtimeEpoch) == "" || generation == 0 {
		return false, nil
	}
	if publish == nil {
		return false, fmt.Errorf("idlechat live publication callback is required")
	}
	token := o.idleEventPublicationToken.Load()
	if token == nil || token.traceID != traceID || token.sessionID != sessionID || token.runtimeEpoch != runtimeEpoch ||
		token.playbackGeneration != generation || token.ctx == nil || token.ctx.Err() != nil ||
		!o.idleEventPublicationToken.CompareAndSwap(token, token) {
		return false, nil
	}
	if err := publish(); err != nil {
		return false, err
	}
	return true, nil
}

func (o *IdleChatOrchestrator) emitTimelineEvent(ev TimelineEvent) TTSLifecycle {
	o.emitMu.Lock()
	defer o.emitMu.Unlock()
	o.mu.Lock()
	if strings.HasPrefix(ev.Type, "idlechat.") && o.interruptedSessions != nil {
		if _, interrupted := o.interruptedSessions[strings.TrimSpace(ev.SessionID)]; interrupted {
			o.mu.Unlock()
			log.Printf("[IdleChat] stale event discarded: type=%s session=%s", ev.Type, ev.SessionID)
			return TTSLifecycle{}
		}
	}
	threadID, threadSeq, threadKind, ok := o.ownerThreadIdentityLocked(ev.SessionID, ev.ownerEpoch)
	if !ok {
		o.mu.Unlock()
		log.Printf("[IdleChat] timeline event rejected without owner generation/thread: type=%s session=%s generation=%d", ev.Type, ev.SessionID, ev.ownerEpoch)
		return TTSLifecycle{}
	}
	if (ev.ThreadID != "" && ev.ThreadID != threadID) ||
		(ev.ThreadSeq != 0 && ev.ThreadSeq != threadSeq) ||
		(ev.ThreadKind != "" && ev.ThreadKind != threadKind) {
		o.mu.Unlock()
		log.Printf("[IdleChat] timeline event rejected with mismatched thread tuple: type=%s session=%s generation=%d", ev.Type, ev.SessionID, ev.ownerEpoch)
		return TTSLifecycle{}
	}
	if ev.TraceID == "" {
		ev.TraceID = o.activeTraceID
	} else if ev.TraceID.Validate() != nil || ev.TraceID != o.activeTraceID {
		o.mu.Unlock()
		log.Printf("[IdleChat] timeline event rejected with mismatched trace owner: type=%s session=%s generation=%d", ev.Type, ev.SessionID, ev.ownerEpoch)
		return TTSLifecycle{}
	}
	if ev.TaskID == "" {
		ev.TaskID = o.activeTaskID
	}
	if ev.RunID == "" {
		ev.RunID = o.activeRunID
	}
	ev.ThreadID = threadID
	ev.ThreadSeq = threadSeq
	ev.ThreadKind = threadKind
	ev.RuntimeEpoch = o.runtimeEpoch
	ev.PlaybackGeneration = ev.ownerEpoch
	emit := o.emitEvent
	o.mu.Unlock()
	o.recordPersonaTimelineEvent(ev)
	if emit != nil {
		return emit(ev)
	}
	return TTSLifecycle{}
}

func (o *IdleChatOrchestrator) emitTTSPrefetchEvent(ev TTSPrefetchEvent) {
	o.emitMu.Lock()
	defer o.emitMu.Unlock()
	traceID, ok := o.traceForSession(ev.SessionID, ev.TraceID, ev.ownerEpoch)
	if !ok {
		log.Printf("[IdleChat] TTS prefetch rejected without owner generation/trace: session=%s message_id=%s generation=%d", ev.SessionID, ev.MessageID, ev.ownerEpoch)
		return
	}
	ev.TraceID = traceID
	o.mu.Lock()
	if ev.TaskID == "" {
		ev.TaskID = o.activeTaskID
	}
	if ev.RunID == "" {
		ev.RunID = o.activeRunID
	}
	ev.RuntimeEpoch = o.runtimeEpoch
	ev.PlaybackGeneration = ev.ownerEpoch
	emit := o.emitTTSPrefetch
	o.mu.Unlock()
	if emit != nil {
		emit(ev)
	}
}

func (o *IdleChatOrchestrator) emitTopicToTimeline(sessionID, topic string, strategy TopicStrategy, generation uint64) TTSLifecycle {
	content := fmt.Sprintf("今日のお題（%s）: %s", strategy, topic)
	messageID := idleChatTopicMessageID()
	category, _ := modulechat.NormalizeTopicCategory(string(strategy))
	return o.emitTimelineEvent(TimelineEvent{
		Type:       "idlechat.topic",
		From:       "user",
		To:         "mio",
		Content:    content,
		SessionID:  sessionID,
		MessageID:  messageID,
		TurnIndex:  0,
		Category:   category,
		Strategy:   strategy,
		ownerEpoch: generation,
	})
}

func (o *IdleChatOrchestrator) recordGenerationErrorToTimeline(speaker, target, sessionID, reason string, turnIndex int, generation uint64) {
	speaker = strings.TrimSpace(speaker)
	if speaker == "" {
		speaker = "unknown"
	}
	target = strings.TrimSpace(target)
	if target == "" {
		target = "idlechat"
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "generation_error"
	}
	o.markConversationRunFailed(generation, "IdleChat conversation generation failed", reason)
	content := fmt.Sprintf("生成エラー: %s の応答生成に失敗しました（%s）。", speaker, reason)
	messageID := o.idleChatMessageID(sessionID, turnIndex)
	msg := domaintransport.NewMessage(speaker, target, sessionID, "", content)
	msg.Type = domaintransport.MessageTypeIdleChat
	msg.Context = idleChatMessageContext(messageID, turnIndex)
	if !o.recordIdleMessageForGeneration(generation, sessionID, msg) {
		log.Printf("[IdleChat] generation error record rejected without active owner: session=%s generation=%d", sessionID, generation)
		return
	}
	o.emitTimelineEvent(TimelineEvent{
		Type:       "idlechat.viewer",
		From:       speaker,
		To:         target,
		Content:    content,
		SessionID:  sessionID,
		MessageID:  messageID,
		TurnIndex:  turnIndex,
		ownerEpoch: generation,
	})
}

func (o *IdleChatOrchestrator) recordIdleMessageForGeneration(generation uint64, sessionID string, msg domaintransport.Message) bool {
	if o == nil || o.memory == nil {
		return false
	}
	o.emitMu.Lock()
	defer o.emitMu.Unlock()
	o.mu.Lock()
	owns := o.ownsIdleSessionLocked(sessionID, generation)
	o.mu.Unlock()
	if !owns {
		return false
	}
	o.memory.RecordMessage(msg)
	return true
}

func (o *IdleChatOrchestrator) idleChatMessageID(sessionID string, turnIndex int) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "idlechat"
	}
	if turnIndex < 0 {
		turnIndex = 0
	}
	return o.cachedIdleChatMessageID(fmt.Sprintf("message\x00%s\x00%d", sessionID, turnIndex))
}

func idleChatTopicMessageID() string {
	return newIdleChatMessageID()
}

func (o *IdleChatOrchestrator) cachedIdleChatMessageID(key string) string {
	if o == nil {
		return string(modulecore.NewMessageID())
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.messageIDs == nil {
		o.messageIDs = make(map[string]string)
	}
	if messageID := o.messageIDs[key]; messageID != "" {
		return messageID
	}
	messageID := string(modulecore.NewMessageID())
	o.messageIDs[key] = messageID
	return messageID
}

func newIdleChatMessageID() string {
	return string(modulecore.NewMessageID())
}

func idleChatMessageContext(messageID string, turnIndex int) map[string]any {
	return map[string]any{
		"message_id": strings.TrimSpace(messageID),
		"turn_index": turnIndex,
	}
}

func (o *IdleChatOrchestrator) nextIdleChatTurnIndex(sessionID string) int {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || o.memory == nil {
		return 1
	}
	maxTurn := 0
	for _, entry := range o.memory.GetUnifiedView(0) {
		msg := entry.Message
		if strings.TrimSpace(msg.SessionID) != sessionID || msg.Type != domaintransport.MessageTypeIdleChat {
			continue
		}
		_, turnIndex := idleChatMessageMetadata(msg, maxTurn)
		if turnIndex > maxTurn {
			maxTurn = turnIndex
		}
	}
	return maxTurn + 1
}
