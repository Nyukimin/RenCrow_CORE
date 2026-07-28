package idlechat

import (
	"fmt"
	"log"
	"strings"

	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func (o *IdleChatOrchestrator) emitTimelineEvent(ev TimelineEvent) <-chan struct{} {
	if strings.HasPrefix(ev.Type, "idlechat.") && o.isInterruptedSession(ev.SessionID) {
		log.Printf("[IdleChat] stale event discarded: type=%s session=%s", ev.Type, ev.SessionID)
		return nil
	}
	o.recordPersonaTimelineEvent(ev)
	o.mu.Lock()
	emit := o.emitEvent
	o.mu.Unlock()
	if emit != nil {
		return emit(ev)
	}
	return nil
}

func (o *IdleChatOrchestrator) emitTopicToTimeline(sessionID, topic string, strategy TopicStrategy) <-chan struct{} {
	content := fmt.Sprintf("今日のお題（%s）: %s", strategy, topic)
	messageID := idleChatTopicMessageID()
	category, _ := modulechat.NormalizeTopicCategory(string(strategy))
	return o.emitTimelineEvent(TimelineEvent{
		Type:      "idlechat.topic",
		From:      "user",
		To:        "mio",
		Content:   content,
		SessionID: sessionID,
		MessageID: messageID,
		TurnIndex: 0,
		Category:  category,
		Strategy:  strategy,
	})
}

func (o *IdleChatOrchestrator) recordGenerationErrorToTimeline(speaker, target, sessionID, reason string, turnIndex int) {
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
	content := fmt.Sprintf("生成エラー: %s の応答生成に失敗しました（%s）。", speaker, reason)
	messageID := o.idleChatMessageID(sessionID, turnIndex)
	msg := domaintransport.NewMessage(speaker, target, sessionID, "", content)
	msg.Type = domaintransport.MessageTypeIdleChat
	msg.Context = idleChatMessageContext(messageID, turnIndex)
	o.memory.RecordMessage(msg)
	o.emitTimelineEvent(TimelineEvent{
		Type:      "idlechat.viewer",
		From:      speaker,
		To:        target,
		Content:   content,
		SessionID: sessionID,
		MessageID: messageID,
		TurnIndex: turnIndex,
	})
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
