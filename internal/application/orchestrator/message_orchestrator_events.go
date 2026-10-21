package orchestrator

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type eventTimingBindings struct {
	mu      sync.Mutex
	byTrace map[modulecore.TraceID]*turntiming.Recorder
}

func newEventTimingBindings() *eventTimingBindings {
	return &eventTimingBindings{byTrace: make(map[modulecore.TraceID]*turntiming.Recorder)}
}

func (b *eventTimingBindings) Bind(traceID modulecore.TraceID, recorder *turntiming.Recorder) {
	if b == nil || traceID.Validate() != nil || recorder == nil {
		return
	}
	b.mu.Lock()
	b.byTrace[traceID] = recorder
	b.mu.Unlock()
}

func (b *eventTimingBindings) Take(traceID modulecore.TraceID) *turntiming.Recorder {
	if b == nil || traceID.Validate() != nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	recorder := b.byTrace[traceID]
	delete(b.byTrace, traceID)
	return recorder
}

func (b *eventTimingBindings) Release(traceID modulecore.TraceID) {
	if b == nil || traceID.Validate() != nil {
		return
	}
	b.mu.Lock()
	delete(b.byTrace, traceID)
	b.mu.Unlock()
}

type messageEventPort struct {
	listener            EventListener
	identities          *conversationIdentityTracker
	traces              *eventTraceBindings
	timings             *eventTimingBindings
	executionIdentities *eventExecutionIdentityBindings
	publicationFail     *eventPublicationFailureTracker
}

func newMessageEventPort(listener EventListener) *messageEventPort {
	return &messageEventPort{
		listener:            listener,
		identities:          newConversationIdentityTracker(),
		traces:              newEventTraceBindings(),
		timings:             newEventTimingBindings(),
		executionIdentities: newEventExecutionIdentityBindings(),
		publicationFail:     newEventPublicationFailureTracker(),
	}
}

func (p *messageEventPort) SetListener(listener EventListener) {
	p.listener = listener
}

func (p *messageEventPort) Emit(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {
	ev := NewEventWithTraceID(p.traces.Resolve(taskID), eventType, from, to, content, route, taskID, sessionID, channel, chatID)
	_, _ = p.emitWithMessageID(ev, "")
}

func (p *messageEventPort) EmitWithMessageID(eventType, from, to, content, route, taskID, sessionID, channel, chatID, messageID string) {
	ev := NewEventWithTraceID(p.traces.Resolve(taskID), eventType, from, to, content, route, taskID, sessionID, channel, chatID)
	_, _ = p.emitWithMessageID(ev, messageID)
}

func (p *messageEventPort) Publish(eventType, from, to, content, route, taskID, sessionID, channel, chatID string, causationEventID modulecore.EventID, dependencyEventIDs []modulecore.EventID) (OrchestratorEvent, error) {
	ev := NewEventWithTraceID(p.traces.Resolve(taskID), eventType, from, to, content, route, taskID, sessionID, channel, chatID)
	ev.CausationEventID = causationEventID
	ev.DependencyEventIDs = append([]modulecore.EventID(nil), dependencyEventIDs...)
	return p.emitWithMessageID(ev, "")
}

func (p *messageEventPort) BindTrace(taskID string, traceID modulecore.TraceID) {
	p.traces.Bind(taskID, traceID)
}

func (p *messageEventPort) BindConversationIdentity(taskID string, turnID modulecore.TurnID, traceID modulecore.TraceID, sessionID modulecore.SessionID) {
	p.traces.BindConversationIdentity(taskID, turnID, traceID, sessionID)
}

func (p *messageEventPort) ReleaseTrace(taskID string) {
	p.traces.Release(taskID)
}

func (p *messageEventPort) BindTiming(traceID modulecore.TraceID, recorder *turntiming.Recorder) {
	if p != nil {
		p.timings.Bind(traceID, recorder)
	}
}

func (p *messageEventPort) ReleaseTiming(traceID modulecore.TraceID) {
	if p != nil {
		p.timings.Release(traceID)
	}
}

func (p *messageEventPort) BindExecutionIdentity(taskID modulecore.TaskID, runID modulecore.RunID, actorKind, actorID string) error {
	if p == nil {
		return fmt.Errorf("message event port is unavailable")
	}
	return p.executionIdentities.Bind(taskID, runID, actorKind, actorID)
}

func (p *messageEventPort) ReleaseExecutionIdentity(taskID modulecore.TaskID) {
	if p == nil || p.executionIdentities == nil {
		return
	}
	p.executionIdentities.Release(taskID)
}

func (p *messageEventPort) BindResponseMessageID(taskID string, messageID modulecore.MessageID) {
	p.identities.BindResponseMessageID(taskID, messageID)
}

func (p *messageEventPort) ReleaseResponseMessageID(taskID string) {
	p.identities.ReleaseResponseMessageID(taskID)
}

func (p *messageEventPort) emitWithMessageID(ev OrchestratorEvent, messageID string) (OrchestratorEvent, error) {
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
		log.Printf("[MessageOrch] emit SKIPPED: no listener (eventType=%s from=%s to=%s)", ev.Type, ev.From, ev.To)
		return ev, nil
	}
	if ev.Type == "agent.response" && strings.EqualFold(strings.TrimSpace(ev.To), "user") {
		if recorder := p.timings.Take(traceID); recorder != nil {
			recorder.Mark(turntiming.PointResponsePublicationStarted)
			snapshot := recorder.Snapshot()
			ev.Timing = &snapshot
		}
	}
	log.Printf("[MessageOrch] emit: eventType=%s from=%s to=%s route=%s taskID=%s", ev.Type, ev.From, ev.To, ev.Route, ev.TaskID)
	if err := p.listener.OnEvent(ev); err != nil {
		if p.publicationFail != nil {
			p.publicationFail.Record(traceID, err)
		}
		log.Printf("[MessageOrch] ERROR: canonical event publication failed: eventType=%s traceID=%s taskID=%s err=%v", ev.Type, ev.TraceID, ev.TaskID, err)
		return ev, err
	}
	return ev, nil
}

func (p *messageEventPort) applyExecutionIdentity(ev *OrchestratorEvent) {
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

func (p *messageEventPort) PublicationError(traceID modulecore.TraceID) error {
	if p == nil || p.publicationFail == nil {
		return nil
	}
	return p.publicationFail.Current(traceID)
}

func (p *messageEventPort) EmitMessageReceived(req ProcessMessageRequest, taskID string) error {
	recipient := normalizeProcessViewerRecipient(req.To)
	ev := NewEventWithTraceID(p.traces.Resolve(taskID), "message.received", "user", recipient, req.UserMessage, "", taskID, req.SessionID, req.Channel, req.ChatID)
	_, err := p.emitWithMessageID(ev, req.MessageID)
	return err
}

func (o *MessageOrchestrator) emit(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {
	o.events.Emit(eventType, from, to, content, route, taskID, sessionID, channel, chatID)
}

func (p *messageEventPort) TakeResponseMessageID(taskID string) string {
	return p.identities.TakeResponseMessageID(taskID)
}

func normalizeProcessViewerRecipient(raw string) string {
	recipient, err := modulechat.NormalizeViewerRecipient(raw)
	if err != nil {
		recipient = modulechat.DefaultViewerRecipient
	}
	return string(recipient)
}
