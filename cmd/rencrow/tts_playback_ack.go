package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type ttsPlaybackAckRequest struct {
	PublicPlaybackRef string `json:"public_playback_ref"`
	SessionID         string `json:"session_id"`
	UtteranceID       string `json:"utterance_id"`
	ViewerClientID    string `json:"viewer_client_id,omitempty"`
	Status            string `json:"status"`
	ErrorCode         string `json:"error_code,omitempty"`
	Error             string `json:"error,omitempty"`
}

func handleTTSPlaybackAck() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()
		var req ttsPlaybackAckRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		publicPlaybackRef := strings.TrimSpace(req.PublicPlaybackRef)
		if publicPlaybackRef == "" {
			http.Error(w, "public_playback_ref is required", http.StatusBadRequest)
			return
		}
		viewerClientID := strings.TrimSpace(req.ViewerClientID)
		activeAudio := activeViewerControl.IsActiveAudio(viewerClientID)
		ok := false
		if moduletts.ShouldConsumePendingForPlaybackAck(activeAudio) {
			ok = notifyIdleChatTTSPlaybackCompleted(publicPlaybackRef)
		}
		receipt := moduletts.BuildPlaybackAckReceipt(moduletts.PlaybackAckInput{
			PublicPlaybackRef: req.PublicPlaybackRef,
			SessionID:         req.SessionID,
			UtteranceID:       req.UtteranceID,
			ViewerClientID:    req.ViewerClientID,
			Status:            req.Status,
			ErrorCode:         req.ErrorCode,
			Error:             req.Error,
		}, activeAudio, ok)
		log.Printf("[TTSPlayback] ack public_playback_ref=%s session=%s utterance=%s viewer_client_id=%s active_audio=%t status=%s matched=%t error_code=%s error=%s",
			receipt.PublicPlaybackRef,
			receipt.SessionID,
			receipt.UtteranceID,
			receipt.ViewerClientID,
			receipt.ActiveAudio,
			receipt.Status,
			receipt.Matched,
			receipt.ErrorCode,
			receipt.Error,
		)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(receipt)
	}
}
