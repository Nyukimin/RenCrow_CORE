package viewer

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

type recipientSelectionRequest struct {
	ViewerClientID string `json:"viewer_client_id"`
	Recipient      string `json:"recipient"`
}

// HandleRecipientSelection reports a client-local recipient choice to CORE.
// It intentionally emits an observation only; CORE does not make a browser tab's
// current selection global or use it as the source of truth for message routing.
func HandleRecipientSelection(emit func(orchestrator.OrchestratorEvent)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()
		var request recipientSelectionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		viewerClientID := strings.TrimSpace(request.ViewerClientID)
		if viewerClientID == "" {
			http.Error(w, "viewer_client_id is required", http.StatusBadRequest)
			return
		}
		recipient, err := modulechat.NormalizeViewerRecipient(request.Recipient)
		if err != nil {
			http.Error(w, "invalid recipient", http.StatusBadRequest)
			return
		}
		if emit != nil {
			payload, _ := json.Marshal(map[string]string{
				"viewer_client_id": viewerClientID,
				"recipient":        string(recipient),
			})
			emit(orchestrator.NewEvent(
				"viewer.recipient_selected",
				"viewer",
				string(recipient),
				string(payload),
				"VIEWER",
				"", "", "", "",
			))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":               true,
			"viewer_client_id": viewerClientID,
			"recipient":        recipient,
		})
	}
}
