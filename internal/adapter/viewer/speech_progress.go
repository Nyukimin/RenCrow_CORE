package viewer

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
)

var errViewerPlaybackReported = errors.New("Viewer playback reported an error")

type speechProgressRequest struct {
	PublicPlaybackRef string `json:"public_playback_ref"`
	TurnID            string `json:"turn_id"`
	UtteranceIndex    *int   `json:"utterance_index"`
	Phase             string `json:"phase"`
	ViewerClientID    string `json:"viewer_client_id"`
}

// HandleProgress creates the Viewer pacing-notification route. The callback
// only selects the current audio owner; progress never records a playback
// receipt or treats the client-provided identity as a credential.
func (r *SpeechCancellationRegistry) HandleProgress(isActiveAudio func(string) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if req.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r == nil {
			http.Error(w, "speech progress unavailable", http.StatusServiceUnavailable)
			return
		}
		defer req.Body.Close()
		decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, speechCancellationMaxBodyBytes))
		decoder.DisallowUnknownFields()
		var body speechProgressRequest
		if err := decoder.Decode(&body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		body.PublicPlaybackRef = strings.TrimSpace(body.PublicPlaybackRef)
		body.TurnID = strings.TrimSpace(body.TurnID)
		body.ViewerClientID = strings.TrimSpace(body.ViewerClientID)
		if body.PublicPlaybackRef == "" || body.TurnID == "" || body.ViewerClientID == "" || body.UtteranceIndex == nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if *body.UtteranceIndex < 0 || *body.UtteranceIndex >= speech.MaxUtterances {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if body.Phase != string(speech.PlaybackPhaseStarted) &&
			body.Phase != string(speech.PlaybackPhaseEnded) &&
			body.Phase != string(speech.PlaybackPhaseError) &&
			body.Phase != string(speech.PlaybackPhaseCancelled) {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		// Unknown, expired, mismatched, or no-longer-active playback reports are
		// intentionally indistinguishable and never disclose turn registry state.
		r.applyProgress(body, isActiveAudio)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (r *SpeechCancellationRegistry) applyProgress(body speechProgressRequest, isActiveAudio func(string) bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.progressByTurnID[body.TurnID]
	if entry == nil || entry.rootTaskID != body.PublicPlaybackRef || entry.turnID != body.TurnID || entry.cancellation == nil {
		return
	}
	if !entry.cancellation.IsViewerPlayback() {
		return
	}
	if isActiveAudio == nil || !isActiveAudio(body.ViewerClientID) {
		return
	}

	index := *body.UtteranceIndex
	if !entry.cancellation.IsReady(index) {
		return
	}
	switch body.Phase {
	case string(speech.PlaybackPhaseStarted):
		_ = entry.cancellation.MarkStarted(index)
	case string(speech.PlaybackPhaseEnded):
		_ = entry.cancellation.MarkEnded(index)
	case string(speech.PlaybackPhaseError):
		_ = entry.cancellation.MarkError(index, errViewerPlaybackReported)
	case string(speech.PlaybackPhaseCancelled):
		_ = entry.cancellation.MarkCancelled(index)
	}
}
