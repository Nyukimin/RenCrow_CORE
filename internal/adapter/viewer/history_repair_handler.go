package viewer

import (
	"encoding/json"
	"net/http"

	historyrepairapp "github.com/Nyukimin/picoclaw_multiLLM/internal/application/historyrepair"
)

func HandleHistoryRepairJSONL(service *historyrepairapp.JSONLRepairService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if service == nil {
			http.Error(w, "history repair service unavailable", http.StatusServiceUnavailable)
			return
		}
		var req historyrepairapp.JSONLRepairRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid history repair payload", http.StatusBadRequest)
			return
		}
		report, err := service.RepairJSONL(r.Context(), req)
		if err != nil {
			http.Error(w, "history repair failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"report": report})
	}
}
