package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const responsePublicationMarkerTimeout = 100 * time.Millisecond

const canonicalEventRelayPageLimit = 1000

const canonicalEventProjectionRestoreTimeout = 2 * time.Minute

type idleAwareEventListener struct {
	hub            *viewer.EventHub
	monitor        *viewer.MonitorStore
	archive        *viewer.CanonicalEventLog
	responseMarker interface {
		MarkConversationTurnResponsePublished(context.Context, modulecore.TurnID, modulecore.TraceID, modulecore.SessionID, time.Time) error
	}
	mu                    sync.RWMutex
	publicationMu         sync.Mutex
	publicationCursor     modulecore.EventSeq
	livePublicationCursor modulecore.EventSeq
	durableCursorAtomic   atomic.Int64
	chatBinding           *chatPublicationBinding
	chatTraceScopes       map[modulecore.TraceID]chatTracePublicationScope
	idleChat              *idlechat.IdleChatOrchestrator
}

type chatPublicationBinding struct {
	traceID modulecore.TraceID
	ctx     context.Context
	end     func() error
	done    chan struct{}
}

type chatTracePublicationScope uint8

const (
	chatTraceScopeUnbound chatTracePublicationScope = iota
	chatTraceScopeAdmitted
	chatTraceScopeSynchronous
)

var errCanonicalEventArchiveRequired = errors.New("canonical event archive is required")

var (
	errChatTraceIDInvalid         = errors.New("CHAT TraceID is invalid")
	errChatBindingActive          = errors.New("an EventStore CHAT admission is already bound")
	errChatTraceIDAlreadyBound    = errors.New("CHAT TraceID is already bound")
	errChatPublicationBusy        = errors.New("canonical event publication is busy")
	errChatTraceIDNotBound        = errors.New("CHAT event has no matching active admission binding")
	errChatPredecessorUnpublished = errors.New("committed orchestrator predecessor is not yet published")
)

func (l *idleAwareEventListener) SetIdleChat(idle *idlechat.IdleChatOrchestrator) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.idleChat = idle
}

// BindChat activates the EventStore RAM admission lease for one explicitly
// reserved CHAT trace. It compares the lease's acquisition-time committed
// orchestrator watermark with the relay's live cursor using RAM only. If a
// durable predecessor is not already live, the call fails closed before the
// runtime starts its session or LLM work.
func (l *idleAwareEventListener) BindChat(traceID modulecore.TraceID, ctx context.Context) (func(), error) {
	if l == nil || l.archive == nil {
		return nil, errCanonicalEventArchiveRequired
	}
	if ctx == nil {
		return nil, errors.New("CHAT admission context is required")
	}
	if err := traceID.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", errChatTraceIDInvalid, err)
	}

	l.mu.RLock()
	active := l.chatBinding
	scope := l.chatTraceScopes[traceID]
	l.mu.RUnlock()
	if active != nil && active.traceID == traceID || scope != chatTraceScopeUnbound {
		return nil, errChatTraceIDAlreadyBound
	}
	if active != nil {
		return nil, fmt.Errorf("%w: active trace %s", errChatBindingActive, active.traceID)
	}
	if !l.publicationMu.TryLock() {
		return nil, errChatPublicationBusy
	}
	defer l.publicationMu.Unlock()
	l.mu.RLock()
	active = l.chatBinding
	scope = l.chatTraceScopes[traceID]
	l.mu.RUnlock()
	if active != nil && active.traceID == traceID || scope != chatTraceScopeUnbound {
		return nil, errChatTraceIDAlreadyBound
	}
	if active != nil {
		return nil, fmt.Errorf("%w: active trace %s", errChatBindingActive, active.traceID)
	}

	endLease, committedHighWater, err := l.archive.BeginChat(ctx)
	if err != nil {
		return nil, err
	}
	if committedHighWater > l.livePublicationCursor {
		_ = endLease()
		return nil, fmt.Errorf("%w: committed=%d live=%d durable=%d", errChatPredecessorUnpublished, committedHighWater, l.livePublicationCursor, l.publicationCursor)
	}
	binding := &chatPublicationBinding{traceID: traceID, ctx: ctx, end: endLease, done: make(chan struct{})}
	l.mu.Lock()
	if l.chatTraceScopes == nil {
		l.chatTraceScopes = make(map[modulecore.TraceID]chatTracePublicationScope)
	}
	l.chatBinding = binding
	l.chatTraceScopes[traceID] = chatTraceScopeAdmitted
	l.mu.Unlock()

	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			l.publicationMu.Lock()
			defer l.publicationMu.Unlock()
			l.mu.Lock()
			if l.chatBinding == binding {
				l.chatBinding = nil
			}
			delete(l.chatTraceScopes, traceID)
			l.mu.Unlock()
			if endErr := binding.end(); endErr != nil {
				log.Printf("[EventRelay] release CHAT admission trace_id=%s failed: %v", traceID, endErr)
			}
			close(binding.done)
		})
	}
	return release, nil
}

// MarkChatSynchronous closes the RAM publication portion of a bound request
// after its queue barrier. The binding remains active until release so any
// late event for this trace waits, then follows the durable compatibility path.
func (l *idleAwareEventListener) MarkChatSynchronous(traceID modulecore.TraceID) error {
	if l == nil || l.archive == nil {
		return errCanonicalEventArchiveRequired
	}
	if err := traceID.Validate(); err != nil {
		return fmt.Errorf("invalid CHAT synchronous trace scope: %w", err)
	}
	l.publicationMu.Lock()
	defer l.publicationMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.chatBinding == nil || l.chatBinding.traceID != traceID || l.chatTraceScopes[traceID] != chatTraceScopeAdmitted {
		return fmt.Errorf("CHAT trace %s has no active admitted publication scope", traceID)
	}
	l.chatTraceScopes[traceID] = chatTraceScopeSynchronous
	return nil
}

// DurableEventSeq reports the latest canonical orchestrator EventSeq that the
// relay reconciled from durable replay. It is a RAM-only read for health paths.
func (l *idleAwareEventListener) DurableEventSeq() modulecore.EventSeq {
	if l == nil {
		return 0
	}
	return modulecore.EventSeq(l.durableCursorAtomic.Load())
}

func (l *idleAwareEventListener) OnEvent(ev orchestrator.OrchestratorEvent) error {
	if l == nil || l.archive == nil {
		return errCanonicalEventArchiveRequired
	}
	for {
		l.mu.RLock()
		binding := l.chatBinding
		scope := l.chatTraceScopes[ev.TraceID]
		l.mu.RUnlock()
		if binding != nil {
			if ev.TraceID == binding.traceID {
				if scope == chatTraceScopeSynchronous {
					<-binding.done
					continue
				}
				if scope != chatTraceScopeAdmitted {
					return fmt.Errorf("%w: active trace %s lost its publication scope", errChatTraceIDNotBound, ev.TraceID)
				}
				l.publicationMu.Lock()
				l.mu.RLock()
				current := l.chatBinding
				currentScope := l.chatTraceScopes[ev.TraceID]
				l.mu.RUnlock()
				if current != binding {
					l.publicationMu.Unlock()
					continue
				}
				if currentScope == chatTraceScopeSynchronous {
					l.publicationMu.Unlock()
					<-binding.done
					continue
				}
				if currentScope != chatTraceScopeAdmitted {
					l.publicationMu.Unlock()
					return fmt.Errorf("%w: admitted trace %s lost its publication scope", errChatTraceIDNotBound, ev.TraceID)
				}
				accepted, _, err := l.archive.AdmitSequenced(binding.ctx, ev)
				if err != nil {
					l.publicationMu.Unlock()
					return err
				}
				if accepted.EventSeq <= l.livePublicationCursor {
					l.publicationMu.Unlock()
					return fmt.Errorf("accepted CHAT EventSeq %d does not follow relay live cursor %d", accepted.EventSeq, l.livePublicationCursor)
				}
				if l.hub != nil {
					if _, err := l.hub.PublishAccepted(accepted); err != nil {
						l.publicationMu.Unlock()
						return err
					}
				}
				if l.monitor != nil {
					l.monitor.OnEvent(accepted)
				}
				l.livePublicationCursor = accepted.EventSeq
				l.publicationMu.Unlock()
				l.afterCanonicalPublication([]orchestrator.OrchestratorEvent{accepted}, "")
				return nil
			}
			// Non-admitted and foreign traces wait outside the publication mutex,
			// then use the legacy durable path after the active lease is released.
			<-binding.done
			continue
		}
		if scope == chatTraceScopeAdmitted {
			return fmt.Errorf("%w: admitted trace %s has no active relay binding", errChatTraceIDNotBound, ev.TraceID)
		}

		// Legacy non-CHAT events keep their synchronous durable path. The second
		// binding check closes the race with a concurrent lease acquisition.
		l.publicationMu.Lock()
		l.mu.RLock()
		binding = l.chatBinding
		scope = l.chatTraceScopes[ev.TraceID]
		l.mu.RUnlock()
		if binding != nil {
			l.publicationMu.Unlock()
			continue
		}
		if scope == chatTraceScopeAdmitted {
			l.publicationMu.Unlock()
			return fmt.Errorf("%w: admitted trace %s has no active relay binding", errChatTraceIDNotBound, ev.TraceID)
		}
		persisted, err := l.archive.AppendSequenced(ev)
		if err != nil {
			l.publicationMu.Unlock()
			return err
		}
		published, err := l.drainCanonicalEvents(context.Background(), persisted.EventID)
		l.publicationMu.Unlock()
		l.afterCanonicalPublication(published, persisted.EventID)
		return err
	}
}

// PublishCommitted publishes already-persisted conversation events by draining
// the canonical orchestrator projection. The bundle is only a recovery signal;
// its events must never be appended a second time here.
func (l *idleAwareEventListener) PublishCommitted(ctx context.Context, _ conversationruntime.CommittedBundle) error {
	if l == nil || l.archive == nil {
		return errCanonicalEventArchiveRequired
	}
	if ctx == nil {
		return errors.New("committed event publication context is required")
	}

	if err := l.lockCanonicalDrain(ctx); err != nil {
		return err
	}
	published, err := l.drainCanonicalEvents(ctx, "")
	l.publicationMu.Unlock()
	l.afterCanonicalPublication(published, "")
	return err
}

// PublishAcceptedInput projects an accepted v2 input that the conversation
// journal has already persisted. The canonical drain orders it with later
// response-first commits and never appends the supplied envelope again.
func (l *idleAwareEventListener) PublishAcceptedInput(ctx context.Context, event modulecore.EventEnvelope) error {
	if l == nil || l.archive == nil {
		return errCanonicalEventArchiveRequired
	}
	if ctx == nil {
		return errors.New("accepted input publication context is required")
	}
	if err := modulecore.ValidateEventEnvelope(event); err != nil {
		return errors.New("accepted input event is not a canonical envelope")
	}
	if event.ComponentID != "orchestrator" || event.EventSeq <= 0 {
		return errors.New("accepted input event must be a persisted orchestrator envelope")
	}

	if err := l.lockCanonicalDrain(ctx); err != nil {
		return err
	}
	published, err := l.drainCanonicalEvents(ctx, "")
	l.publicationMu.Unlock()
	l.afterCanonicalPublication(published, "")
	return err
}

// RestoreCanonicalProjection rebuilds in-memory Viewer projections from the
// canonical event store before the runtime accepts requests. It deliberately
// skips post-publication actor side effects because replay is not new activity.
func (l *idleAwareEventListener) RestoreCanonicalProjection(ctx context.Context) error {
	if l == nil || l.archive == nil {
		return errCanonicalEventArchiveRequired
	}
	if ctx == nil {
		return errors.New("canonical event projection restore context is required")
	}

	if err := l.lockCanonicalDrain(ctx); err != nil {
		return err
	}
	_, err := l.drainCanonicalEvents(ctx, "")
	l.publicationMu.Unlock()
	return err
}

// lockCanonicalDrain waits for any active CHAT lease before taking the
// publication mutex. It rechecks after acquiring the mutex because BindChat
// may have installed a lease between the first check and Lock. A CHAT lease
// and a durable replay can therefore never hold the mutex across one another's
// request work or database I/O.
func (l *idleAwareEventListener) lockCanonicalDrain(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		l.mu.RLock()
		binding := l.chatBinding
		l.mu.RUnlock()
		if binding != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-binding.done:
				continue
			}
		}

		l.publicationMu.Lock()
		l.mu.RLock()
		binding = l.chatBinding
		l.mu.RUnlock()
		if binding == nil {
			return nil
		}
		l.publicationMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-binding.done:
		}
	}
}

// drainCanonicalEvents is called with publicationMu held. Its RAM cursor only
// advances after the corresponding event has reached the Monitor and live Hub.
// It follows the component watermark instead of requiring contiguous global
// EventSeq values because private components can occupy sequence gaps.
func (l *idleAwareEventListener) drainCanonicalEvents(ctx context.Context, allowLiveTransientID modulecore.EventID) ([]orchestrator.OrchestratorEvent, error) {
	cursor := l.publicationCursor
	through := modulecore.EventSeq(0)
	published := make([]orchestrator.OrchestratorEvent, 0)
	for {
		page, watermark, err := l.archive.ReplayPage(ctx, cursor, through, canonicalEventRelayPageLimit)
		if err != nil {
			return published, err
		}
		if through == 0 {
			through = watermark
		} else if watermark != through {
			return published, errors.New("canonical event watermark changed during relay drain")
		}
		if watermark < cursor {
			return published, errors.New("canonical event watermark precedes relay cursor")
		}
		if len(page) == 0 {
			if cursor < through {
				return published, errors.New("canonical event replay ended before relay watermark")
			}
			return published, nil
		}
		for index, event := range page {
			if err := ctx.Err(); err != nil {
				return published, err
			}
			if event.EventSeq <= cursor || (through > 0 && event.EventSeq > through) {
				return published, errors.New("canonical event replay is outside the relay sequence window")
			}
			if index > 0 && event.EventSeq <= page[index-1].EventSeq {
				return published, errors.New("canonical event replay is not in ascending EventSeq order")
			}
			if event.EventSeq > l.livePublicationCursor && (!viewer.IsTransientReplayEvent(event) || event.EventID == allowLiveTransientID) {
				projected := true
				if l.hub != nil {
					var hubErr error
					projected, hubErr = l.hub.PublishLive(event)
					if hubErr != nil {
						return published, hubErr
					}
				}
				if projected {
					if l.monitor != nil {
						l.monitor.OnEvent(event)
					}
					published = append(published, event)
				}
			}
			if event.EventSeq > l.livePublicationCursor {
				l.livePublicationCursor = event.EventSeq
			}
			if l.hub != nil {
				if err := l.hub.MarkDurable(event); err != nil {
					return published, err
				}
			}
			cursor = event.EventSeq
			l.publicationCursor = cursor
			l.durableCursorAtomic.Store(int64(cursor))
		}
		if cursor >= through {
			return published, nil
		}
	}
}

func (l *idleAwareEventListener) afterCanonicalPublication(events []orchestrator.OrchestratorEvent, responseMarkerEventID modulecore.EventID) {
	for _, ev := range events {
		if l.responseMarker != nil && ev.EventID == responseMarkerEventID && ev.Type == "agent.response" && strings.EqualFold(strings.TrimSpace(ev.To), "user") &&
			ev.TurnID.Validate() == nil && ev.TraceID.Validate() == nil && ev.SessionID.Validate() == nil {
			ctx, cancel := context.WithTimeout(context.Background(), responsePublicationMarkerTimeout)
			markErr := l.responseMarker.MarkConversationTurnResponsePublished(ctx, ev.TurnID, ev.TraceID, ev.SessionID, time.Now().UTC())
			cancel()
			if markErr != nil {
				code := domconv.ConversationTurnErrorCodeOf(markErr)
				if code == "" {
					code = "internal"
				}
				log.Printf("[ConversationTurn] response publication marker pending turn_id=%s trace_id=%s error_code=%s", ev.TurnID, ev.TraceID, code)
			}
		}

		if shouldStopIdleChatByEvent(ev) {
			l.mu.RLock()
			idle := l.idleChat
			l.mu.RUnlock()
			if idle != nil {
				idle.NotifyActivity()
			}
		}
	}
}

func shouldStopIdleChatByEvent(ev orchestrator.OrchestratorEvent) bool {
	if strings.EqualFold(ev.Route, "IDLECHAT") {
		return false
	}
	if ev.Type == "tts.audio_chunk" || strings.EqualFold(ev.From, "tts") {
		return false
	}
	if ev.Type == "message.received" {
		return true
	}
	if ev.Type == "entry.stage" {
		stage := strings.ToLower(strings.TrimSpace(ev.Content))
		return stage == "received" || stage == "planning"
	}
	return false
}
