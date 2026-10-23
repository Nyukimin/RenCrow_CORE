package main

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
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
	mu                sync.RWMutex
	publicationMu     sync.Mutex
	publicationCursor modulecore.EventSeq
	idleChat          *idlechat.IdleChatOrchestrator
}

var errCanonicalEventArchiveRequired = errors.New("canonical event archive is required")

func (l *idleAwareEventListener) SetIdleChat(idle *idlechat.IdleChatOrchestrator) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.idleChat = idle
}

func (l *idleAwareEventListener) OnEvent(ev orchestrator.OrchestratorEvent) error {
	if l == nil || l.archive == nil {
		return errCanonicalEventArchiveRequired
	}

	// Canonical persistence is the single publication gate. Legacy events are
	// appended once, then both legacy and committed events use the same ordered
	// canonical drain for downstream projections.
	l.publicationMu.Lock()
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

	l.publicationMu.Lock()
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

	l.publicationMu.Lock()
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

	l.publicationMu.Lock()
	_, err := l.drainCanonicalEvents(ctx, "")
	l.publicationMu.Unlock()
	return err
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
			if !viewer.IsTransientReplayEvent(event) || event.EventID == allowLiveTransientID {
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
			cursor = event.EventSeq
			l.publicationCursor = cursor
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
