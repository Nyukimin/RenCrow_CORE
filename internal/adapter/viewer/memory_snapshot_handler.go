package viewer

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

type MemorySnapshotStore interface {
	RecentByNamespace(ctx context.Context, namespace string, limit int) ([]l1sqlite.L1MemoryEvent, error)
	RecentNewsItems(ctx context.Context, category string, limit int) ([]l1sqlite.L1NewsItem, error)
	RecentDailyDigests(ctx context.Context, category string, limit int) ([]l1sqlite.L1DailyDigest, error)
	RecentKnowledgeItems(ctx context.Context, domain string, limit int) ([]l1sqlite.L1KnowledgeItem, error)
}

type SourceBalancedNewsStore interface {
	RecentNewsItemsBySource(ctx context.Context, category string, perSource int, limit int) ([]l1sqlite.L1NewsItem, error)
}

type TimeWindowNewsStore interface {
	RecentNewsItemsSince(ctx context.Context, category string, since time.Time) ([]l1sqlite.L1NewsItem, error)
}

func HandleMemorySnapshot(store MemorySnapshotStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireViewerMethod(w, r, http.MethodGet) {
			return
		}
		if !requireViewerStore(w, store == nil, "memory snapshot unavailable") {
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 20, 100)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		perSource, err := parseViewerOptionalLimit(r.URL.Query().Get("per_source"), 20)
		if err != nil {
			http.Error(w, "invalid per_source", http.StatusBadRequest)
			return
		}
		newsWindowHours, err := parseViewerOptionalLimit(r.URL.Query().Get("news_window_hours"), 24*30)
		if err != nil {
			http.Error(w, "invalid news_window_hours", http.StatusBadRequest)
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
			out["memory"] = memoryEventDTOsFromL1(items)
		}
		var news []l1sqlite.L1NewsItem
		if newsWindowHours > 0 {
			windowed, ok := store.(TimeWindowNewsStore)
			if !ok {
				http.Error(w, "time-windowed news unavailable", http.StatusNotImplemented)
				return
			}
			cutoff := time.Now().UTC().Add(-time.Duration(newsWindowHours) * time.Hour)
			news, err = windowed.RecentNewsItemsSince(r.Context(), category, cutoff)
			out["news_window_hours"] = newsWindowHours
			out["news_since"] = cutoff.Format(time.RFC3339)
		} else if perSource > 0 {
			balanced, ok := store.(SourceBalancedNewsStore)
			if !ok {
				http.Error(w, "source-balanced news unavailable", http.StatusNotImplemented)
				return
			}
			news, err = balanced.RecentNewsItemsBySource(r.Context(), category, perSource, limit)
		} else {
			news, err = store.RecentNewsItems(r.Context(), category, limit)
		}
		if err != nil {
			http.Error(w, "failed to load news", http.StatusInternalServerError)
			return
		}
		digests, err := store.RecentDailyDigests(r.Context(), category, limit)
		if err != nil {
			http.Error(w, "failed to load digests", http.StatusInternalServerError)
			return
		}
		out["news"] = newsItemDTOsFromL1(news)
		out["digests"] = dailyDigestDTOsFromL1(digests)
		if domain != "" {
			knowledge, err := store.RecentKnowledgeItems(r.Context(), domain, limit)
			if err != nil {
				http.Error(w, "failed to load knowledge", http.StatusInternalServerError)
				return
			}
			out["knowledge"] = knowledgeItemDTOsFromL1(knowledge)
		}

		writeJSON(w, http.StatusOK, out)
	}
}

func parseViewerOptionalLimit(raw string, max int) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	return parseViewerLimit(raw, 0, max)
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
