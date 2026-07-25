package orchestrator

import (
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
)

type distributedEventPort struct {
	listener   EventListener
	identities *conversationIdentityTracker
}

func newDistributedEventPort(listener EventListener) *distributedEventPort {
	return &distributedEventPort{listener: listener, identities: newConversationIdentityTracker()}
}

func (p *distributedEventPort) SetListener(listener EventListener) {
	p.listener = listener
}

func (p *distributedEventPort) Emit(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {
	ev := NewEvent(eventType, from, to, content, route, jobID, sessionID, channel, chatID)
	p.emitWithMessageID(ev, "")
}

func (p *distributedEventPort) EmitMessageReceived(req ProcessMessageRequest, jobID string) {
	recipient := normalizeProcessViewerRecipient(req.To)
	ev := NewEvent("message.received", "user", recipient, req.UserMessage, "", jobID, req.SessionID, req.Channel, req.ChatID)
	p.emitWithMessageID(ev, req.MessageID)
}

func (p *distributedEventPort) emitWithMessageID(ev OrchestratorEvent, messageID string) {
	p.identities.Assign(&ev, messageID)
	if p.listener == nil {
		return
	}
	p.listener.OnEvent(ev)
}

func (p *distributedEventPort) EmitNote(from, to, content, route, jobID, sessionID, channel, chatID string) {
	p.Emit("agent.note", from, to, content, route, jobID, sessionID, channel, chatID)
}

func (p *distributedEventPort) EmitProgress(eventType, from, to, content string, msg domaintransport.Message) {
	route, channel, chatID := routeAndChannelFromMessage(msg)
	p.Emit(eventType, from, to, content, route, msg.JobID, msg.SessionID, channel, chatID)
}

func (p *distributedEventPort) TakeResponseMessageID(jobID string) string {
	return p.identities.TakeResponseMessageID(jobID)
}
