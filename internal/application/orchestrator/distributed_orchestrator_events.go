package orchestrator

import (
	"log"

	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type distributedEventPort struct {
	listener        EventListener
	identities      *conversationIdentityTracker
	traces          *eventTraceBindings
	publicationFail *eventPublicationFailureTracker
}

func newDistributedEventPort(listener EventListener) *distributedEventPort {
	return &distributedEventPort{
		listener:        listener,
		identities:      newConversationIdentityTracker(),
		traces:          newEventTraceBindings(),
		publicationFail: newEventPublicationFailureTracker(),
	}
}

func (p *distributedEventPort) SetListener(listener EventListener) {
	p.listener = listener
}

func (p *distributedEventPort) Emit(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {
	ev := NewEventWithTraceID(p.traces.Resolve(jobID), eventType, from, to, content, route, jobID, sessionID, channel, chatID)
	_ = p.emitWithMessageID(ev, "")
}

func (p *distributedEventPort) EmitMessageReceived(req ProcessMessageRequest, jobID string) error {
	recipient := normalizeProcessViewerRecipient(req.To)
	ev := NewEventWithTraceID(p.traces.Resolve(jobID), "message.received", "user", recipient, req.UserMessage, "", jobID, req.SessionID, req.Channel, req.ChatID)
	return p.emitWithMessageID(ev, req.MessageID)
}

func (p *distributedEventPort) BindTrace(jobID string, traceID modulecore.TraceID) {
	p.traces.Bind(jobID, traceID)
}

func (p *distributedEventPort) ReleaseTrace(jobID string) {
	p.traces.Release(jobID)
}

func (p *distributedEventPort) BindResponseMessageID(jobID string, messageID modulecore.MessageID) {
	p.identities.BindResponseMessageID(jobID, messageID)
}

func (p *distributedEventPort) ReleaseResponseMessageID(jobID string) {
	p.identities.ReleaseResponseMessageID(jobID)
}

func (p *distributedEventPort) emitWithMessageID(ev OrchestratorEvent, messageID string) error {
	traceID := modulecore.TraceID(ev.TraceID)
	if p.publicationFail != nil {
		if err := p.publicationFail.Current(traceID); err != nil {
			return err
		}
	}
	p.identities.Assign(&ev, messageID)
	if p.listener == nil {
		return nil
	}
	if err := p.listener.OnEvent(ev); err != nil {
		if p.publicationFail != nil {
			p.publicationFail.Record(traceID, err)
		}
		log.Printf("[DistributedOrch] ERROR: canonical event publication failed: eventType=%s traceID=%s jobID=%s err=%v", ev.Type, ev.TraceID, ev.JobID, err)
		return err
	}
	return nil
}

func (p *distributedEventPort) PublicationError(traceID modulecore.TraceID) error {
	if p == nil || p.publicationFail == nil {
		return nil
	}
	return p.publicationFail.Current(traceID)
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
