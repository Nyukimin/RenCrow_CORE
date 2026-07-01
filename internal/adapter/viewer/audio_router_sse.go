package viewer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
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

		ch := h.Subscribe()
		defer h.Unsubscribe(ch)
		lastSeen := parseLastEventIDHeader(r.Header.Get("Last-Event-ID"))

		for _, ev := range h.History() {
			if ev.Seq > 0 && ev.Seq <= lastSeen {
				continue
			}
			if isTransientReplayEvent(ev) {
				continue
			}
			if !writeAudioRouterEvent(w, ev) {
				continue
			}
		}
		flusher.Flush()

		for {
			select {
			case <-r.Context().Done():
				return
			case data := <-ch:
				var ev orchestrator.OrchestratorEvent
				if err := json.Unmarshal(data, &ev); err != nil {
					continue
				}
				if !writeAudioRouterEvent(w, ev) {
					continue
				}
				flusher.Flush()
			}
		}
	}
}

func writeAudioRouterEvent(w http.ResponseWriter, ev orchestrator.OrchestratorEvent) bool {
	if ev.Type != "tts.audio_chunk" || strings.TrimSpace(ev.Content) == "" {
		return false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(ev.Content), &payload); err != nil {
		return false
	}
	characterID, _ := payload["character_id"].(string)
	audioURL, _ := payload["audio_url"].(string)
	audioPath, _ := payload["audio_path"].(string)
	if strings.TrimSpace(characterID) == "" {
		return false
	}
	if strings.TrimSpace(audioURL) == "" && strings.TrimSpace(audioPath) == "" {
		return false
	}
	if ev.Seq > 0 {
		fmt.Fprintf(w, "id: %d\n", ev.Seq)
	}
	fmt.Fprint(w, "event: tts.audio_chunk\n")
	fmt.Fprintf(w, "data: %s\n\n", ev.Content)
	return true
}
