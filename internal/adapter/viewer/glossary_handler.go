package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	glossaryentity "github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/domain/entity"
)

type GlossaryLister interface {
	GetRecentGlossary(ctx context.Context, limit int) ([]*glossaryentity.GlossaryItem, error)
}

func HandleGlossaryRecent(store GlossaryLister) http.HandlerFunc {
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

		items, err := store.GetRecentGlossary(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load glossary", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": items,
		})
	}
}
