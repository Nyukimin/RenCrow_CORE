package orchestrator

import (
	"log"

	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type messageEventPort struct {
	listener        EventListener
	identities      *conversationIdentityTracker
	traces          *eventTraceBindings
	publicationFail *eventPublicationFailureTracker
}

func newMessageEventPort(listener EventListener) *messageEventPort {
	return &messageEventPort{
		listener:        listener,
		identities:      newConversationIdentityTracker(),
		traces:          newEventTraceBindings(),
		publicationFail: newEventPublicationFailureTracker(),
	}
}

func (p *messageEventPort) SetListener(listener EventListener) {
	p.listener = listener
}

func (p *messageEventPort) Emit(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {
	ev := NewEventWithTraceID(p.traces.Resolve(jobID), eventType, from, to, content, route, jobID, sessionID, channel, chatID)
	_ = p.emitWithMessageID(ev, "")
}

func (p *messageEventPort) EmitWithMessageID(eventType, from, to, content, route, jobID, sessionID, channel, chatID, messageID string) {
	ev := NewEventWithTraceID(p.traces.Resolve(jobID), eventType, from, to, content, route, jobID, sessionID, channel, chatID)
	_ = p.emitWithMessageID(ev, messageID)
}

func (p *messageEventPort) BindTrace(jobID string, traceID modulecore.TraceID) {
	p.traces.Bind(jobID, traceID)
}

func (p *messageEventPort) ReleaseTrace(jobID string) {
	p.traces.Release(jobID)
}

func (p *messageEventPort) BindResponseMessageID(jobID string, messageID modulecore.MessageID) {
	p.identities.BindResponseMessageID(jobID, messageID)
}

func (p *messageEventPort) ReleaseResponseMessageID(jobID string) {
	p.identities.ReleaseResponseMessageID(jobID)
}

func (p *messageEventPort) emitWithMessageID(ev OrchestratorEvent, messageID string) error {
	traceID := modulecore.TraceID(ev.TraceID)
	if p.publicationFail != nil {
		if err := p.publicationFail.Current(traceID); err != nil {
			return err
		}
	}
	p.identities.Assign(&ev, messageID)
	if p.listener == nil {
		log.Printf("[MessageOrch] emit SKIPPED: no listener (eventType=%s from=%s to=%s)", ev.Type, ev.From, ev.To)
		return nil
	}
	log.Printf("[MessageOrch] emit: eventType=%s from=%s to=%s route=%s jobID=%s", ev.Type, ev.From, ev.To, ev.Route, ev.JobID)
	if err := p.listener.OnEvent(ev); err != nil {
		if p.publicationFail != nil {
			p.publicationFail.Record(traceID, err)
		}
		log.Printf("[MessageOrch] ERROR: canonical event publication failed: eventType=%s traceID=%s jobID=%s err=%v", ev.Type, ev.TraceID, ev.JobID, err)
		return err
	}
	return nil
}

func (p *messageEventPort) PublicationError(traceID modulecore.TraceID) error {
	if p == nil || p.publicationFail == nil {
		return nil
	}
	return p.publicationFail.Current(traceID)
}

func (p *messageEventPort) EmitMessageReceived(req ProcessMessageRequest, jobID string) error {
	recipient := normalizeProcessViewerRecipient(req.To)
	ev := NewEventWithTraceID(p.traces.Resolve(jobID), "message.received", "user", recipient, req.UserMessage, "", jobID, req.SessionID, req.Channel, req.ChatID)
	return p.emitWithMessageID(ev, req.MessageID)
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
