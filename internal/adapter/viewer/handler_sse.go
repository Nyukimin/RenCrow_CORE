package viewer

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var sseHeartbeatInterval = 15 * time.Second

func (h *EventHub) HandleSSE(w http.ResponseWriter, r *http.Request) {
	lastSeen, err := parseLastEventIDHeader(r.Header.Get("Last-Event-ID"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
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
	clearReplayDeadline := SetReplayWriteDeadline(w)
	defer clearReplayDeadline()
	replayWrote := false
	replayCursor, err := h.Replay(r.Context(), lastSeen, history, func(ev orchestrator.OrchestratorEvent) error {
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

// HandlePage serves the single-page viewer HTML.
