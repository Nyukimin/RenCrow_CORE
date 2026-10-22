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

// HandleSSE streams Viewer events. The starting position is chosen as follows:
//
//   - A positive Last-Event-ID resumes after that canonical EventSeq. A cursor
//     ahead of the canonical window is rejected with 409.
//   - Otherwise "?from=now" tails: only events published after the connection
//     is registered are sent, and no history or durable replay is used. Events
//     published while the client is disconnected are not delivered; a client
//     that must not miss them resumes with a positive Last-Event-ID.
//   - Otherwise the in-memory history snapshot is replayed before live events.
//
// The positive Last-Event-ID takes precedence over "?from=now" so that an
// automatic EventSource reconnect resumes instead of tailing again.
func (h *EventHub) HandleSSE(w http.ResponseWriter, r *http.Request) {
	h.HandleSSEWithFilter(w, r, func(_ context.Context, event orchestrator.OrchestratorEvent) (bool, error) {
		return !IsResponseFirstViewerEvent(event), nil
	})
}

// SSEEventFilter controls which canonical events a stream may expose. It is
// evaluated for both the initial replay and live fan-out; an error fails the
// stream closed.
type SSEEventFilter func(context.Context, orchestrator.OrchestratorEvent) (bool, error)

// HandleSSEWithFilter serves the legacy in-memory/canonical stream with an
// owner-supplied event boundary. Filtered events still advance the stream
// cursor so a skipped v2 event cannot be replayed later as a duplicate.
func (h *EventHub) HandleSSEWithFilter(w http.ResponseWriter, r *http.Request, filter SSEEventFilter) {
	if filter == nil {
		http.Error(w, "event stream policy is unavailable", http.StatusServiceUnavailable)
		return
	}
	lastSeen, err := parseLastEventIDHeader(r.Header.Get("Last-Event-ID"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tail, err := parseStreamFrom(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tail = tail && lastSeen == 0
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, history := h.Subscribe()
	defer h.Unsubscribe(ch)
	replayCursor := lastSeen
	if !tail {
		clearReplayDeadline := SetReplayWriteDeadline(w)
		defer clearReplayDeadline()
		replayWrote := false
		replayCursor, err = h.Replay(r.Context(), lastSeen, history, func(ev orchestrator.OrchestratorEvent) error {
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
			data, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			replayWrote = true
			if ev.EventSeq > 0 {
				if _, err := fmt.Fprintf(w, "id: %d\n", ev.EventSeq); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			log.Printf("[Viewer] SSE replay failed: %v", err)
			if !replayWrote {
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
				if _, err := fmt.Fprintf(w, "id: %d\n", ev.EventSeq); err != nil {
					return
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
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
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
	lastSeen, err := parseLastEventIDHeader(r.Header.Get("Last-Event-ID"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tail, err := parseStreamFrom(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if lastSeen < scope.CutoverSeq {
		lastSeen = scope.CutoverSeq
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, _ := h.Subscribe()
	defer h.Unsubscribe(ch)
	cursor := lastSeen
	if !tail || lastSeen > scope.CutoverSeq {
		clearReplayDeadline := SetReplayWriteDeadline(w)
		defer clearReplayDeadline()
		wrote := false
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
			if !wrote {
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
	if event.EventSeq > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", event.EventSeq); err != nil {
			return err
		}
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

func parseLastEventIDHeader(v string) (modulecore.EventSeq, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("Last-Event-ID must be a non-negative EventSeq")
	}
	return modulecore.EventSeq(n), nil
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
