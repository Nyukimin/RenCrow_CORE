package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	conversationpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation"
)

type MemorySnapshotStore interface {
	RecentByNamespace(ctx context.Context, namespace string, limit int) ([]conversationpersistence.L1MemoryEvent, error)
	RecentNewsItems(ctx context.Context, category string, limit int) ([]conversationpersistence.L1NewsItem, error)
	RecentDailyDigests(ctx context.Context, category string, limit int) ([]conversationpersistence.L1DailyDigest, error)
	RecentKnowledgeItems(ctx context.Context, domain string, limit int) ([]conversationpersistence.L1KnowledgeItem, error)
}

func HandleMemorySnapshot(store MemorySnapshotStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "memory snapshot unavailable", http.StatusServiceUnavailable)
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 20, 100)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
		category := strings.TrimSpace(r.URL.Query().Get("category"))
		domain := strings.TrimSpace(r.URL.Query().Get("domain"))

		out := map[string]any{
			"namespace": namespace,
			"category":  category,
			"domain":    domain,
		}
		if namespace != "" {
			items, err := store.RecentByNamespace(r.Context(), namespace, limit)
			if err != nil {
				http.Error(w, "failed to load memory", http.StatusInternalServerError)
				return
			}
			out["memory"] = items
		}
		news, err := store.RecentNewsItems(r.Context(), category, limit)
		if err != nil {
			http.Error(w, "failed to load news", http.StatusInternalServerError)
			return
		}
		digests, err := store.RecentDailyDigests(r.Context(), category, limit)
		if err != nil {
			http.Error(w, "failed to load digests", http.StatusInternalServerError)
			return
		}
		out["news"] = news
		out["digests"] = digests
		if domain != "" {
			knowledge, err := store.RecentKnowledgeItems(r.Context(), domain, limit)
			if err != nil {
				http.Error(w, "failed to load knowledge", http.StatusInternalServerError)
				return
			}
			out["knowledge"] = knowledge
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

func parseViewerLimit(raw string, def int, max int) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		if err != nil {
			return 0, err
		}
		return 0, errors.New("limit must be positive")
	}
	if n > max {
		n = max
	}
	return n, nil
}
