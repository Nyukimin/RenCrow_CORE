package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	conversationpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation"
)

type MemoryEventsStore interface {
	RecentEvents(ctx context.Context, namespace string, limit int) ([]conversationpersistence.L1EventLogEntry, error)
	RecentSearchCache(ctx context.Context, limit int) ([]conversationpersistence.L1SearchCacheEntry, error)
}

func HandleMemoryEvents(store MemoryEventsStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "memory events unavailable", http.StatusServiceUnavailable)
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 20, 100)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
		if namespace == "" {
			http.Error(w, "namespace is required", http.StatusBadRequest)
			return
		}
		events, err := store.RecentEvents(r.Context(), namespace, limit)
		if err != nil {
			http.Error(w, "failed to load memory events", http.StatusInternalServerError)
			return
		}
		searchCache, err := store.RecentSearchCache(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load search cache", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"namespace":    namespace,
			"events":       events,
			"search_cache": searchCache,
		})
	}
}
