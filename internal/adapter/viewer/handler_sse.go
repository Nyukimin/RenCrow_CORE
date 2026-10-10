package viewer

import (
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
			if err := json.Unmarshal(data, &ev); err == nil && ev.EventSeq > 0 {
				if ev.EventSeq <= replayCursor {
					continue
				}
				if _, err := fmt.Fprintf(w, "id: %d\n", ev.EventSeq); err != nil {
					return
				}
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
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
