package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	domconv "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
)

type RecallTraceStore interface {
	RecentRecallTraces(ctx context.Context, sessionID string, limit int) ([]domconv.RecallTrace, error)
}
func HandleRecallTraces(store RecallTraceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "recall trace unavailable", http.StatusServiceUnavailable)
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 20, 100)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
		items, err := store.RecentRecallTraces(r.Context(), sessionID, limit)
		if err != nil {
			http.Error(w, "failed to load recall traces", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"items":      items,
			"session_id": sessionID,
		})
	}
}
