package viewer

import (
	"context"
	"net/http"
	"strings"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

type RecallTraceStore interface {
	RecentRecallTraces(ctx context.Context, sessionID string, limit int) ([]domconv.RecallTrace, error)
}

func HandleRecallTraces(store RecallTraceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireViewerMethod(w, r, http.MethodGet) {
			return
		}
		if !requireViewerStore(w, store == nil, "recall trace unavailable") {
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
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"items":      items,
			"session_id": sessionID,
		})
	}
}
