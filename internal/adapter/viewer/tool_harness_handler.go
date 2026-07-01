package viewer

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/toolharness"
)

type ToolHarnessEventLister interface {
	ListRecent(limit int) ([]toolharness.Event, error)
}

func HandleToolHarnessRecent(store ToolHarnessEventLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			writeJSON(w, http.StatusOK, map[string]any{"items": []toolharness.Event{}})
			return
		}
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil || v < 1 || v > 500 {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			limit = v
		}
		items, err := store.ListRecent(limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
