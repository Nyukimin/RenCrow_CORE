package viewer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

// HandleAudioRouterSSE streams only audio-router relevant tts.audio_chunk payloads.
func HandleAudioRouterSSE(h *EventHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
