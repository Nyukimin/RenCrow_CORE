package orchestrator

import (
	"log"
	"sync"

	modulechat "github.com/Nyukimin/picoclaw_multiLLM/modules/chat"
)

type messageEventPort struct {
	listener EventListener
	mu       sync.Mutex
	turns    map[string]int
}

func newMessageEventPort(listener EventListener) *messageEventPort {
	return &messageEventPort{listener: listener, turns: map[string]int{}}
}

func (p *messageEventPort) SetListener(listener EventListener) {
	p.listener = listener
}

func (p *messageEventPort) Emit(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {
	if p.listener == nil {
		log.Printf("[MessageOrch] emit SKIPPED: no listener (eventType=%s from=%s to=%s)", eventType, from, to)
		return
	}
	log.Printf("[MessageOrch] emit: eventType=%s from=%s to=%s route=%s jobID=%s", eventType, from, to, route, jobID)
	ev := NewEvent(eventType, from, to, content, route, jobID, sessionID, channel, chatID)
	p.assignConversationIdentity(&ev)
	p.listener.OnEvent(ev)
}

func (p *messageEventPort) EmitMessageReceived(req ProcessMessageRequest, jobID string) {
	recipient := normalizeProcessViewerRecipient(req.To)
	p.Emit("message.received", "user", recipient, req.UserMessage, "", jobID, req.SessionID, req.Channel, req.ChatID)
}

func (o *MessageOrchestrator) emit(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {
	o.events.Emit(eventType, from, to, content, route, jobID, sessionID, channel, chatID)
}

func (o *MessageOrchestrator) emitMessageReceived(req ProcessMessageRequest) {
	o.events.EmitMessageReceived(req, "")
}

func (p *messageEventPort) assignConversationIdentity(ev *OrchestratorEvent) {
	if ev == nil || !isConversationMessageEvent(ev.Type) {
		return
	}
	sessionID := conversationIdentitySession(ev.SessionID, ev.ChatID)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.turns[sessionID]++
	ev.TurnIndex = p.turns[sessionID]
	ev.MessageID = conversationMessageID(sessionID, ev.TurnIndex)
}

func normalizeProcessViewerRecipient(raw string) string {
	recipient, err := modulechat.NormalizeViewerRecipient(raw)
	if err != nil {
		recipient = modulechat.DefaultViewerRecipient
	}
	return string(recipient)
}
