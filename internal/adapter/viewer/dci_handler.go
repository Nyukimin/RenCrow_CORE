package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	domaindci "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/dci"
)

type DCITraceLister interface {
	ListRecent(limit int) ([]domaindci.SearchTrace, error)
}

type DCITraceContextLister interface {
	ListRecent(ctx context.Context, limit int) ([]domaindci.SearchTrace, error)
}

type DCISearcher interface {
	Search(ctx context.Context, query string) (domaindci.SearchResult, error)
}

func HandleDCIRecent(store any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit := 20
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n <= 0 {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			if n > 100 {
				n = 100
			}
			limit = n
		}
		items := []domaindci.SearchTrace{}
		var err error
		switch s := store.(type) {
		case DCITraceContextLister:
			items, err = s.ListRecent(r.Context(), limit)
		case DCITraceLister:
			items, err = s.ListRecent(limit)
		case nil:
			writeJSON(w, http.StatusOK, map[string]any{"items": items})
			return
		default:
			http.Error(w, "dci trace store unavailable", http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			http.Error(w, "failed to load dci traces", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}

func HandleDCISearch(searcher DCISearcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if searcher == nil {
			http.Error(w, "dci searcher unavailable", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Query == "" {
			http.Error(w, "query is required", http.StatusBadRequest)
			return
		}
		result, err := searcher.Search(r.Context(), req.Query)
		if err != nil {
			http.Error(w, "dci search failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}
