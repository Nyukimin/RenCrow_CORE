package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var sseHeartbeatInterval = 15 * time.Second

// streamFromNow is the only accepted value of the "from" query parameter on
// GET /viewer/events. It requests a tail subscription.
const streamFromNow = "now"

// sseResetCursor is not a canonical EventID. EventSource retains it across
// reconnects until a durable event replaces it, allowing interrupted reset
// replay to continue from the available beginning even with ?from=now.
const sseResetCursor = "reset"

// HandleSSE streams Viewer events. The starting position is chosen as follows:
//
//   - A canonical EventID resumes after the exact event it identifies. If the
//     event is no longer available, a reset frame precedes replay of the
//     available canonical window.
//   - Otherwise "?from=now" tails: only events published after the connection
//     is registered are sent, and no history or durable replay is used. Events
//     published while the client is disconnected are not delivered; a client
//     that must not miss them reconnects with the last canonical EventID.
//   - Otherwise the in-memory history snapshot is replayed before live events.
//
// A supplied cursor takes precedence over "?from=now". Legacy numeric cursors
// trigger a reset because EventSeq can be reused after a WAL tail rollback.
func (h *EventHub) HandleSSE(w http.ResponseWriter, r *http.Request) {
	h.HandleSSEWithFilter(w, r, func(_ context.Context, event orchestrator.OrchestratorEvent) (bool, error) {
		return !IsResponseFirstViewerEvent(event), nil
	})
}

// SSEEventFilter controls which canonical events a stream may expose. It is
// evaluated for both the initial replay and live fan-out; an error fails the
// stream closed.
type SSEEventFilter func(context.Context, orchestrator.OrchestratorEvent) (bool, error)

// SessionPublicEventCursorResolver resolves an EventID only after checking it
// against the trusted owner/session public-message projection.
type SessionPublicEventCursorResolver interface {
	ResolveSessionPublicEventID(context.Context, SessionPublicReplayScope, modulecore.EventID) (modulecore.EventSeq, bool, error)
}

// HandleSSEWithFilter serves the legacy in-memory/canonical stream with an
// owner-supplied event boundary. Filtered events still advance the stream
// cursor so a skipped v2 event cannot be replayed later as a duplicate.
func (h *EventHub) HandleSSEWithFilter(w http.ResponseWriter, r *http.Request, filter SSEEventFilter) {
	if filter == nil {
		http.Error(w, "event stream policy is unavailable", http.StatusServiceUnavailable)
		return
	}
	lastID, err := parseLastEventIDHeader(r.Header.Get("Last-Event-ID"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tail, err := parseStreamFrom(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tail = tail && !lastID.hasCursor()
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ch, history := h.Subscribe()
	defer h.Unsubscribe(ch)
	lastSeen := modulecore.EventSeq(0)
	resetReason := ""
	if lastID.legacySequence {
		resetReason = "legacy_sequence_cursor"
	} else if lastID.resetContinuation {
		resetReason = "reset_continuation"
	} else if lastID.eventID != "" {
		h.mu.RLock()
		reader := h.replayReader
		h.mu.RUnlock()
		resolver, supported := reader.(EventCursorResolver)
		if !supported {
			resetReason = "event_id_unavailable"
		} else {
			event, found, resolveErr := resolver.GetByEventID(r.Context(), lastID.eventID)
			if resolveErr != nil {
				log.Printf("[Viewer] SSE cursor resolution failed: %v", resolveErr)
				http.Error(w, "event cursor unavailable", http.StatusServiceUnavailable)
				return
			}
			if !found {
				resetReason = "event_id_unavailable"
			} else if event.EventID != lastID.eventID || event.EventSeq <= 0 {
				log.Printf("[Viewer] SSE cursor resolution returned a mismatched canonical event")
				http.Error(w, "event cursor unavailable", http.StatusServiceUnavailable)
				return
			} else {
				include, filterErr := filter(r.Context(), event)
				if filterErr != nil {
					log.Printf("[Viewer] SSE cursor filter failed: %v", filterErr)
					http.Error(w, "event cursor unavailable", http.StatusServiceUnavailable)
					return
				}
				if !include || IsTransientReplayEvent(event) {
					resetReason = "event_id_unavailable"
				} else {
					lastSeen = event.EventSeq
				}
			}
		}
	}
	if resetReason != "" {
		tail = false
	}
	if resetReason != "" {
		lastSeen = 0
	}
	replayCursor := lastSeen
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	if !tail {
		clearReplayDeadline := SetReplayWriteDeadline(w)
		defer clearReplayDeadline()
		replayWrote := false
		if resetReason != "" {
			if err := writeSSEReset(w, resetReason); err != nil {
				return
			}
		}
		emit := func(ev orchestrator.OrchestratorEvent) error {
			if IsTransientReplayEvent(ev) {
				return nil
			}
			include, filterErr := filter(r.Context(), ev)
			if filterErr != nil {
				return filterErr
			}
			if !include {
				return nil
			}
			if err := writeSSEEvent(w, ev); err != nil {
				return err
			}
			replayWrote = true
			return nil
		}
		if resetReason != "" {
			replayCursor, err = h.ReplayAvailable(r.Context(), history, emit)
		} else {
			replayCursor, err = h.Replay(r.Context(), lastSeen, history, emit)
		}
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			log.Printf("[Viewer] SSE replay failed: %v", err)
			if !replayWrote && resetReason == "" {
				http.Error(w, "event replay unavailable", http.StatusConflict)
			}
			return
		}
		if replayCursor < lastSeen {
			// A successful empty replay must retain the caller's cursor so that a
			// concurrent live event at or below it cannot be emitted twice.
			replayCursor = lastSeen
		}
		flusher.Flush()
		clearReplayDeadline()
	} else {
		// The registration in Subscribe and the history append plus fan-out in
		// OnEvent share one lock, so every event published after this point
		// arrives on ch exactly once and the history snapshot is not needed.
		flusher.Flush()
	}

	var heartbeat <-chan time.Time
	var ticker *time.Ticker
	if sseHeartbeatInterval > 0 {
		ticker = time.NewTicker(sseHeartbeatInterval)
		defer ticker.Stop()
		heartbeat = ticker.C
	}

	// Stream new events
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case data, ok := <-ch:
			if !ok {
				return
			}
			var ev orchestrator.OrchestratorEvent
			liveSequence := modulecore.EventSeq(0)
			if err := json.Unmarshal(data, &ev); err != nil {
				continue
			}
			if ev.EventSeq > 0 {
				if ev.EventSeq <= replayCursor {
					continue
				}
				liveSequence = ev.EventSeq
				include, filterErr := filter(r.Context(), ev)
				if filterErr != nil {
					log.Printf("[Viewer] SSE live event filter failed: %v", filterErr)
					return
				}
				if !include {
					replayCursor = liveSequence
					continue
				}
			} else {
				include, filterErr := filter(r.Context(), ev)
				if filterErr != nil {
					log.Printf("[Viewer] SSE live event filter failed: %v", filterErr)
					return
				}
				if !include {
					continue
				}
			}
			if err := writeSSEEvent(w, ev); err != nil {
				return
			}
			flusher.Flush()
			if liveSequence > replayCursor {
				replayCursor = liveSequence
			}
		}
	}
}

// IsResponseFirstViewerEvent identifies v2 public message envelopes that must
// not flow through the unscoped legacy stream, even if the session lookup is
// temporarily unavailable.
func IsResponseFirstViewerEvent(event orchestrator.OrchestratorEvent) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(event.Type)), "conversation.") {
		return true
	}
	if event.SessionID.Validate() == nil && event.MessageID.Validate() == nil && event.TurnID.Validate() == nil &&
		event.Visibility != "" {
		return true
	}
	return false
}

// HandleSessionPublicSSE streams only public message-index rows for one
// already owner-authorized v2 session. Scope is supplied by the runtime route,
// never by query parameters or request headers.
func (h *EventHub) HandleSessionPublicSSE(w http.ResponseWriter, r *http.Request, scope SessionPublicReplayScope) {
	if h == nil || scope.SessionID.Validate() != nil || strings.TrimSpace(scope.OwnerID) == "" || strings.TrimSpace(scope.ActorID) == "" || scope.CutoverSeq < 0 {
		http.Error(w, "owner-scoped conversation stream is unavailable", http.StatusServiceUnavailable)
		return
	}
	h.mu.RLock()
	reader := h.sessionReplayReader
	h.mu.RUnlock()
	if reader == nil {
		http.Error(w, "owner-scoped conversation replay is unavailable", http.StatusServiceUnavailable)
		return
	}
	lastID, err := parseLastEventIDHeader(r.Header.Get("Last-Event-ID"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tail, err := parseStreamFrom(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tail = tail && !lastID.hasCursor()
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ch, _ := h.Subscribe()
	defer h.Unsubscribe(ch)
	lastSeen := scope.CutoverSeq
	resetReason := ""
	if lastID.legacySequence {
		resetReason = "legacy_sequence_cursor"
	} else if lastID.resetContinuation {
		resetReason = "reset_continuation"
	} else if lastID.eventID != "" {
		resolver, supported := reader.(SessionPublicEventCursorResolver)
		if !supported {
			resetReason = "event_id_unavailable"
		} else {
			sequence, found, resolveErr := resolver.ResolveSessionPublicEventID(r.Context(), scope, lastID.eventID)
			if resolveErr != nil {
				log.Printf("[Viewer] owner-scoped SSE cursor resolution failed: %v", resolveErr)
				http.Error(w, "conversation cursor unavailable", http.StatusServiceUnavailable)
				return
			}
			if !found {
				resetReason = "event_id_unavailable"
			} else if sequence <= scope.CutoverSeq {
				log.Printf("[Viewer] owner-scoped SSE cursor resolved before the session cutover")
				http.Error(w, "conversation cursor unavailable", http.StatusServiceUnavailable)
				return
			} else {
				lastSeen = sequence
			}
		}
	}
	if resetReason != "" {
		lastSeen = scope.CutoverSeq
		tail = false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	cursor := lastSeen
	if resetReason != "" || !tail || lastSeen > scope.CutoverSeq {
		clearReplayDeadline := SetReplayWriteDeadline(w)
		defer clearReplayDeadline()
		wrote := false
		if resetReason != "" {
			if err := writeSSEReset(w, resetReason); err != nil {
				return
			}
		}
		cursor, err = replaySessionPublic(r.Context(), reader, scope, lastSeen, func(event orchestrator.OrchestratorEvent) error {
			if IsTransientReplayEvent(event) {
				return nil
			}
			if err := writeSSEEvent(w, event); err != nil {
				return err
			}
			wrote = true
			return nil
		})
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			log.Printf("[Viewer] owner-scoped SSE replay failed: %v", err)
			if !wrote && resetReason == "" {
				http.Error(w, "conversation replay unavailable", http.StatusConflict)
			}
			return
		}
		flusher.Flush()
		clearReplayDeadline()
	} else {
		flusher.Flush()
	}

	var heartbeat <-chan time.Time
	var ticker *time.Ticker
	if sseHeartbeatInterval > 0 {
		ticker = time.NewTicker(sseHeartbeatInterval)
		defer ticker.Stop()
		heartbeat = ticker.C
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case data, ok := <-ch:
			if !ok {
				return
			}
			var event orchestrator.OrchestratorEvent
			if err := json.Unmarshal(data, &event); err != nil || event.EventSeq <= 0 {
				continue
			}
			if event.EventSeq <= cursor {
				continue
			}
			cursor = event.EventSeq
			if event.SessionID != scope.SessionID || event.EventID.Validate() != nil {
				continue
			}
			canonicalEvent, public, err := reader.ReadSessionPublicMessage(r.Context(), scope, event.EventID, event.EventSeq)
			if err != nil {
				log.Printf("[Viewer] owner-scoped SSE live authorization failed: %v", err)
				return
			}
			if !public {
				continue
			}
			if canonicalEvent.EventID != event.EventID || canonicalEvent.EventSeq != event.EventSeq || canonicalEvent.SessionID != scope.SessionID || canonicalEvent.Visibility != "public" {
				log.Printf("[Viewer] owner-scoped SSE canonical live event mismatch")
				return
			}
			if err := writeSSEEvent(w, canonicalEvent); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func replaySessionPublic(ctx context.Context, reader SessionPublicEventReplayReader, scope SessionPublicReplayScope, after modulecore.EventSeq, emit func(orchestrator.OrchestratorEvent) error) (modulecore.EventSeq, error) {
	if ctx == nil || emit == nil || after < scope.CutoverSeq {
		return after, fmt.Errorf("invalid owner-scoped conversation replay")
	}
	bounded, cancel := context.WithTimeout(ctx, replayTimeout)
	defer cancel()
	cursor := after
	through := modulecore.EventSeq(0)
	for {
		if err := replayContextError(bounded); err != nil {
			return cursor, err
		}
		page, watermark, err := reader.ReplaySessionPublicPage(bounded, scope, cursor, through, replayPageLimit)
		if err != nil {
			return cursor, err
		}
		if through == 0 {
			through = watermark
		} else if watermark != through {
			return cursor, fmt.Errorf("owner-scoped replay watermark changed")
		}
		if watermark < cursor || len(page) > replayPageLimit {
			return cursor, fmt.Errorf("owner-scoped replay returned an invalid sequence window")
		}
		if len(page) == 0 {
			if cursor < through {
				return cursor, fmt.Errorf("owner-scoped replay ended before fixed watermark %d", through)
			}
			return cursor, nil
		}
		for _, event := range page {
			if event.EventSeq <= cursor || event.EventSeq > through || event.SessionID != scope.SessionID || event.Visibility != "public" || event.MessageID.Validate() != nil || event.TurnID.Validate() != nil {
				return cursor, fmt.Errorf("owner-scoped replay event violates its message boundary")
			}
			if err := emit(event); err != nil {
				return cursor, err
			}
			cursor = event.EventSeq
		}
		if cursor >= through {
			return cursor, nil
		}
		if len(page) < replayPageLimit {
			return cursor, fmt.Errorf("owner-scoped replay ended before fixed watermark %d", through)
		}
	}
}

func writeSSEEvent(w http.ResponseWriter, event orchestrator.OrchestratorEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if event.EventSeq > 0 && !IsTransientReplayEvent(event) {
		if err := event.EventID.Validate(); err != nil {
			return fmt.Errorf("sequenced SSE event has an invalid EventID: %w", err)
		}
		if _, err := fmt.Fprintf(w, "id: %s\n", event.EventID); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	return err
}

func writeSSEReset(w http.ResponseWriter, reason string) error {
	data, err := json.Marshal(struct {
		Reason string `json:"reason"`
	}{Reason: reason})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %s\n", sseResetCursor); err != nil {
		return err
	}
	if _, err := fmt.Fprint(w, "event: reset\n"); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	return err
}

// SetReplayWriteDeadline bounds writes while an SSE client receives its
// initial replay. The returned cleanup is idempotent so callers can defer it
// and also clear the deadline immediately after the initial flush.
func SetReplayWriteDeadline(w http.ResponseWriter) func() {
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(replayTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		log.Printf("[Viewer] SSE replay write deadline unavailable: %v", err)
	}
	cleared := false
	return func() {
		if cleared {
			return
		}
		cleared = true
		if err := controller.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
			log.Printf("[Viewer] SSE live write deadline reset failed: %v", err)
		}
	}
}

type lastEventIDCursor struct {
	eventID           modulecore.EventID
	legacySequence    bool
	resetContinuation bool
}

func (cursor lastEventIDCursor) hasCursor() bool {
	return cursor.eventID != "" || cursor.legacySequence || cursor.resetContinuation
}

func parseLastEventIDHeader(v string) (lastEventIDCursor, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return lastEventIDCursor{}, nil
	}
	if v == sseResetCursor {
		return lastEventIDCursor{resetContinuation: true}, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err == nil {
		if n < 0 {
			return lastEventIDCursor{}, fmt.Errorf("Last-Event-ID must be a canonical EventID or non-negative legacy cursor")
		}
		if n == 0 {
			return lastEventIDCursor{}, nil
		}
		return lastEventIDCursor{legacySequence: true}, nil
	}
	eventID := modulecore.EventID(v)
	if err := eventID.Validate(); err != nil {
		return lastEventIDCursor{}, fmt.Errorf("Last-Event-ID must be a canonical EventID or non-negative legacy cursor")
	}
	return lastEventIDCursor{eventID: eventID}, nil
}

// parseStreamFrom reports whether the request asks for a tail subscription.
// "from" is a reserved key: when present it must appear once with the value
// "now". Any other query parameter is ignored.
func parseStreamFrom(query url.Values) (bool, error) {
	values, present := query["from"]
	if !present {
		return false, nil
	}
	if len(values) != 1 || values[0] != streamFromNow {
		return false, fmt.Errorf("from must be exactly %q", streamFromNow)
	}
	return true, nil
}

// HandlePage serves the single-page viewer HTML.
