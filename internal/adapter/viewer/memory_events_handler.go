package viewer

import (
	"context"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/l1sqlite"
	"net/http"
	"strings"
)

type MemoryEventsStore interface {
	RecentEvents(ctx context.Context, namespace string, limit int) ([]l1sqlite.L1EventLogEntry, error)
	RecentSearchCache(ctx context.Context, limit int) ([]l1sqlite.L1SearchCacheEntry, error)
}

func HandleMemoryEvents(store MemoryEventsStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireViewerMethod(w, r, http.MethodGet) {
			return
		}
		if !requireViewerStore(w, store == nil, "memory events unavailable") {
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

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"namespace":    namespace,
			"events":       eventLogEntryDTOsFromL1(events),
			"search_cache": searchCacheEntryDTOsFromL1(searchCache),
		})
	}
}
