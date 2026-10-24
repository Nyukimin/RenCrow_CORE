package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

// AudioEventAccessFilter is supplied by the trusted runtime to check whether
// the exact queued event still belongs to a current canonical Session. A
// Session ID alone cannot prove that an event belongs to the current lifecycle.
type AudioEventAccessFilter func(context.Context, orchestrator.OrchestratorEvent) (bool, error)

// HandleAudioRouterSSE keeps the legacy entry point closed until its caller
// supplies the trusted runtime session filter through HandleAudioRouterSSEWithFilter.
func HandleAudioRouterSSE(h *EventHub) http.HandlerFunc {
	return HandleAudioRouterSSEWithFilter(h, nil)
}

// HandleAudioRouterSSEWithFilter streams only audio-router relevant audio
// chunks for sessions approved by the trusted runtime filter.
func HandleAudioRouterSSEWithFilter(h *EventHub, filter AudioEventAccessFilter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if filter == nil || h == nil {
			http.Error(w, "audio event access unavailable", http.StatusServiceUnavailable)
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

		ch, _ := h.Subscribe()
		defer h.Unsubscribe(ch)
		flusher.Flush()

		for {
			select {
			case <-r.Context().Done():
				return
			case data, ok := <-ch:
				if !ok {
					return
				}
				var ev orchestrator.OrchestratorEvent
				if err := json.Unmarshal(data, &ev); err != nil {
					continue
				}
				if ev.Type != "tts.audio_chunk" {
					continue
				}
				if strings.TrimSpace(string(ev.SessionID)) == "" {
					continue
				}
				allowed, err := filter(r.Context(), ev)
				if err != nil {
					return
				}
				if !allowed {
					continue
				}
				written, writeErr := writeAudioRouterEvent(w, ev)
				if writeErr != nil {
					return
				}
				if !written {
					continue
				}
				flusher.Flush()
			}
		}
	}
}

func writeAudioRouterEvent(w http.ResponseWriter, ev orchestrator.OrchestratorEvent) (bool, error) {
	if ev.Type != "tts.audio_chunk" || strings.TrimSpace(ev.Content) == "" {
		return false, nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(ev.Content), &payload); err != nil {
		return false, nil
	}
	characterID, _ := payload["character_id"].(string)
	audioURL, _ := payload["audio_url"].(string)
	audioPath, _ := payload["audio_path"].(string)
	if strings.TrimSpace(characterID) == "" {
		return false, nil
	}
	if strings.TrimSpace(audioURL) == "" && strings.TrimSpace(audioPath) == "" {
		return false, nil
	}
	if ev.EventSeq > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", ev.EventSeq); err != nil {
			return false, err
		}
	}
	if _, err := fmt.Fprint(w, "event: tts.audio_chunk\n"); err != nil {
		return false, err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", ev.Content); err != nil {
		return false, err
	}
	return true, nil
}
