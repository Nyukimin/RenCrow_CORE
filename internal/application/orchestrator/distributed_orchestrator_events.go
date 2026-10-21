package orchestrator

import (
	"fmt"
	"log"
	"strings"

	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type distributedEventPort struct {
	listener            EventListener
	identities          *conversationIdentityTracker
	traces              *eventTraceBindings
	timings             *eventTimingBindings
	executionIdentities *eventExecutionIdentityBindings
	publicationFail     *eventPublicationFailureTracker
}

func newDistributedEventPort(listener EventListener) *distributedEventPort {
	return &distributedEventPort{
		listener:            listener,
		identities:          newConversationIdentityTracker(),
		traces:              newEventTraceBindings(),
		timings:             newEventTimingBindings(),
		executionIdentities: newEventExecutionIdentityBindings(),
		publicationFail:     newEventPublicationFailureTracker(),
	}
}

func (p *distributedEventPort) SetListener(listener EventListener) {
	p.listener = listener
}

func (p *distributedEventPort) Emit(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {
	ev := NewEventWithTraceID(p.traces.Resolve(taskID), eventType, from, to, content, route, taskID, sessionID, channel, chatID)
	_, _ = p.emitWithMessageID(ev, "")
}

func (p *distributedEventPort) EmitMessageReceived(req ProcessMessageRequest, taskID string) error {
	recipient := normalizeProcessViewerRecipient(req.To)
	ev := NewEventWithTraceID(p.traces.Resolve(taskID), "message.received", "user", recipient, req.UserMessage, "", taskID, req.SessionID, req.Channel, req.ChatID)
	_, err := p.emitWithMessageID(ev, req.MessageID)
	return err
}

func (p *distributedEventPort) Publish(eventType, from, to, content, route, taskID, sessionID, channel, chatID string, causationEventID modulecore.EventID, dependencyEventIDs []modulecore.EventID) (OrchestratorEvent, error) {
	ev := NewEventWithTraceID(p.traces.Resolve(taskID), eventType, from, to, content, route, taskID, sessionID, channel, chatID)
	ev.CausationEventID = causationEventID
	ev.DependencyEventIDs = append([]modulecore.EventID(nil), dependencyEventIDs...)
	return p.emitWithMessageID(ev, "")
}

func (p *distributedEventPort) BindTrace(taskID string, traceID modulecore.TraceID) {
	p.traces.Bind(taskID, traceID)
}

func (p *distributedEventPort) BindConversationIdentity(taskID string, turnID modulecore.TurnID, traceID modulecore.TraceID, sessionID modulecore.SessionID) {
	p.traces.BindConversationIdentity(taskID, turnID, traceID, sessionID)
}

func (p *distributedEventPort) ReleaseTrace(taskID string) {
	p.traces.Release(taskID)
}

func (p *distributedEventPort) BindTiming(traceID modulecore.TraceID, recorder *turntiming.Recorder) {
	if p != nil {
		p.timings.Bind(traceID, recorder)
	}
}

func (p *distributedEventPort) ReleaseTiming(traceID modulecore.TraceID) {
	if p != nil {
		p.timings.Release(traceID)
	}
}

func (p *distributedEventPort) BindExecutionIdentity(taskID modulecore.TaskID, runID modulecore.RunID, actorKind, actorID string) error {
	if p == nil {
		return fmt.Errorf("distributed event port is unavailable")
	}
	return p.executionIdentities.Bind(taskID, runID, actorKind, actorID)
}

func (p *distributedEventPort) ReleaseExecutionIdentity(taskID modulecore.TaskID) {
	if p == nil || p.executionIdentities == nil {
		return
	}
	p.executionIdentities.Release(taskID)
}

func (p *distributedEventPort) BindResponseMessageID(taskID string, messageID modulecore.MessageID) {
	p.identities.BindResponseMessageID(taskID, messageID)
}

func (p *distributedEventPort) ReleaseResponseMessageID(taskID string) {
	p.identities.ReleaseResponseMessageID(taskID)
}

func (p *distributedEventPort) emitWithMessageID(ev OrchestratorEvent, messageID string) (OrchestratorEvent, error) {
	p.traces.Apply(&ev)
	p.applyExecutionIdentity(&ev)
	traceID := modulecore.TraceID(ev.TraceID)
	if p.publicationFail != nil {
		if err := p.publicationFail.Current(traceID); err != nil {
			return ev, err
		}
	}
	p.identities.Assign(&ev, messageID)
	if p.listener == nil {
		return ev, nil
	}
	if ev.Type == "agent.response" && strings.EqualFold(strings.TrimSpace(ev.To), "user") {
		if recorder := p.timings.Take(traceID); recorder != nil {
			recorder.Mark(turntiming.PointResponsePublicationStarted)
			snapshot := recorder.Snapshot()
			ev.Timing = &snapshot
		}
	}
	if err := p.listener.OnEvent(ev); err != nil {
		if p.publicationFail != nil {
			p.publicationFail.Record(traceID, err)
		}
		log.Printf("[DistributedOrch] ERROR: canonical event publication failed: eventType=%s traceID=%s taskID=%s err=%v", ev.Type, ev.TraceID, ev.TaskID, err)
		return ev, err
	}
	return ev, nil
}

func (p *distributedEventPort) applyExecutionIdentity(ev *OrchestratorEvent) {
	if p == nil || ev == nil || p.executionIdentities == nil {
		return
	}
	identity, ok := p.executionIdentities.Resolve(ev.TaskID)
	if !ok {
		return
	}
	ev.RunID = identity.RunID
	ev.ActorKind = identity.ActorKind
	ev.ActorID = identity.ActorID
}

func (p *distributedEventPort) PublicationError(traceID modulecore.TraceID) error {
	if p == nil || p.publicationFail == nil {
		return nil
	}
	return p.publicationFail.Current(traceID)
}

func (p *distributedEventPort) EmitNote(from, to, content, route, taskID, sessionID, channel, chatID string) {
	p.Emit("agent.note", from, to, content, route, taskID, sessionID, channel, chatID)
}

func (p *distributedEventPort) EmitProgress(eventType, from, to, content string, msg domaintransport.Message) {
	route, channel, chatID := routeAndChannelFromMessage(msg)
	p.Emit(eventType, from, to, content, route, msg.TaskID.String(), msg.SessionID, channel, chatID)
}

func (p *distributedEventPort) TakeResponseMessageID(taskID string) string {
	return p.identities.TakeResponseMessageID(taskID)
}
