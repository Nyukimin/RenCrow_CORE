package viewer

import (
	"context"
	"net/http"
	"strconv"

	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policydecision"
)

type PolicyDecisionLister interface {
	List(ctx context.Context, limit int) ([]domainpolicy.Record, error)
}

func HandlePolicyDecisions(store PolicyDecisionLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "policy decision store unavailable", http.StatusServiceUnavailable)
			return
		}
		limit := 20
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 || parsed > 100 {
				http.Error(w, "limit must be between 1 and 100", http.StatusBadRequest)
				return
			}
			limit = parsed
		}
		items, err := store.List(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load policy decisions", http.StatusInternalServerError)
			return
		}
		if items == nil {
			items = []domainpolicy.Record{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}
