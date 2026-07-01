package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	moduletts "github.com/Nyukimin/picoclaw_multiLLM/modules/tts"
)

type viewerActiveClaimRequest struct {
	ViewerClientID string `json:"viewer_client_id"`
	Kind           string `json:"kind"`
	Reason         string `json:"reason,omitempty"`
	Action         string `json:"action,omitempty"`
}

var viewerActiveOwnerTTL = 90 * time.Second

var activeViewerControl = moduletts.NewViewerActiveControlStore(viewerActiveOwnerTTL)

func resetActiveViewerControlForTest() {
	activeViewerControl = moduletts.NewViewerActiveControlStore(viewerActiveOwnerTTL)
}

func handleViewerActiveClaim(emit func(orchestrator.OrchestratorEvent)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()
		var req viewerActiveClaimRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		viewerID := strings.TrimSpace(req.ViewerClientID)
		if viewerID == "" {
			http.Error(w, "viewer_client_id is required", http.StatusBadRequest)
			return
		}
		kind := strings.TrimSpace(req.Kind)
		if kind != moduletts.ViewerActiveKindAudio && kind != moduletts.ViewerActiveKindInput {
			http.Error(w, "kind must be audio or input", http.StatusBadRequest)
			return
		}
		action := strings.TrimSpace(req.Action)
		if action == "" {
			action = "claim"
		}
		var snapshot moduletts.ViewerActiveControlSnapshot
		switch action {
		case "claim":
			snapshot = activeViewerControl.Claim(kind, viewerID)
		case "heartbeat":
			snapshot = activeViewerControl.Heartbeat(kind, viewerID)
		case "release":
			snapshot = activeViewerControl.Release(kind, viewerID)
		default:
			http.Error(w, "action must be claim, heartbeat, or release", http.StatusBadRequest)
			return
		}
		log.Printf("[ViewerActive] %s kind=%s viewer_client_id=%s reason=%s active_audio=%s active_input=%s",
			action,
			kind,
			viewerID,
			strings.TrimSpace(req.Reason),
			snapshot.ActiveAudioViewerID,
			snapshot.ActiveInputViewerID,
		)
		if emit != nil {
			payload, _ := json.Marshal(map[string]string{
				"kind":                   kind,
				"viewer_client_id":       viewerID,
				"active_audio_viewer_id": snapshot.ActiveAudioViewerID,
				"active_input_viewer_id": snapshot.ActiveInputViewerID,
				"reason":                 strings.TrimSpace(req.Reason),
				"action":                 action,
			})
			emit(orchestrator.NewEvent("viewer.active_control", "viewer", "viewer", string(payload), "VIEWER", "", "", "", ""))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":                     true,
			"active_audio_viewer_id": snapshot.ActiveAudioViewerID,
			"active_input_viewer_id": snapshot.ActiveInputViewerID,
		})
	}
}
