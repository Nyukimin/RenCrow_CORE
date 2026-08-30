package orchestrator

import (
	"log"

	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type messageEventPort struct {
	listener   EventListener
	identities *conversationIdentityTracker
	traces     *eventTraceBindings
}

func newMessageEventPort(listener EventListener) *messageEventPort {
	return &messageEventPort{listener: listener, identities: newConversationIdentityTracker(), traces: newEventTraceBindings()}
}

func (p *messageEventPort) SetListener(listener EventListener) {
	p.listener = listener
}

func (p *messageEventPort) Emit(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {
	ev := NewEventWithTraceID(p.traces.Resolve(jobID), eventType, from, to, content, route, jobID, sessionID, channel, chatID)
	p.emitWithMessageID(ev, "")
}

func (p *messageEventPort) EmitWithMessageID(eventType, from, to, content, route, jobID, sessionID, channel, chatID, messageID string) {
	ev := NewEventWithTraceID(p.traces.Resolve(jobID), eventType, from, to, content, route, jobID, sessionID, channel, chatID)
	p.emitWithMessageID(ev, messageID)
}

func (p *messageEventPort) BindTrace(jobID string, traceID modulecore.TraceID) {
	p.traces.Bind(jobID, traceID)
}

func (p *messageEventPort) ReleaseTrace(jobID string) {
	p.traces.Release(jobID)
}

func (p *messageEventPort) emitWithMessageID(ev OrchestratorEvent, messageID string) {
	p.identities.Assign(&ev, messageID)
	if p.listener == nil {
		log.Printf("[MessageOrch] emit SKIPPED: no listener (eventType=%s from=%s to=%s)", ev.Type, ev.From, ev.To)
		return
	}
	log.Printf("[MessageOrch] emit: eventType=%s from=%s to=%s route=%s jobID=%s", ev.Type, ev.From, ev.To, ev.Route, ev.JobID)
	p.listener.OnEvent(ev)
}

func (p *messageEventPort) EmitMessageReceived(req ProcessMessageRequest, jobID string) {
	recipient := normalizeProcessViewerRecipient(req.To)
	p.EmitWithMessageID("message.received", "user", recipient, req.UserMessage, "", jobID, req.SessionID, req.Channel, req.ChatID, req.MessageID)
}

func (o *MessageOrchestrator) emit(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {
	o.events.Emit(eventType, from, to, content, route, jobID, sessionID, channel, chatID)
}

func (p *messageEventPort) TakeResponseMessageID(jobID string) string {
	return p.identities.TakeResponseMessageID(jobID)
}

func normalizeProcessViewerRecipient(raw string) string {
	recipient, err := modulechat.NormalizeViewerRecipient(raw)
	if err != nil {
		recipient = modulechat.DefaultViewerRecipient
	}
	return string(recipient)
}
