package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// EvidenceLister provides recent execution reports.
type EvidenceLister interface {
	ListRecent(ctx context.Context, limit int) ([]domainexecution.ExecutionReport, error)
	ListRecentUnique(ctx context.Context, limit int) ([]domainexecution.ExecutionReport, error)
	GetByTaskID(ctx context.Context, taskID modulecore.TaskID) (domainexecution.ExecutionReport, error)
	Summary(ctx context.Context) (map[string]map[string]int, error)
	SummaryUnique(ctx context.Context) (map[string]map[string]int, error)
}

// HandleEvidenceRecent returns recent execution reports as JSON.
func HandleEvidenceRecent(store EvidenceLister) http.HandlerFunc {
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

		// Use ListRecentUnique by default to avoid showing duplicate tasks (retry/repair).
		items, err := store.ListRecentUnique(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load evidence", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": items,
		})
	}
}

// HandleEvidenceDetail returns one execution report by task_id.
func HandleEvidenceDetail(store EvidenceLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		taskID := modulecore.TaskID(r.URL.Query().Get("task_id"))
		if err := taskID.Validate(); err != nil {
			http.Error(w, "valid task_id is required", http.StatusBadRequest)
			return
		}

		item, err := store.GetByTaskID(r.Context(), taskID)
		if err != nil {
			http.Error(w, "report not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"item": item,
		})
	}
}

// HandleEvidenceSummary returns evidence summary counts.
func HandleEvidenceSummary(store EvidenceLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Use SummaryUnique to count unique tasks, not all reports.
		summary, err := store.SummaryUnique(r.Context())
		if err != nil {
			http.Error(w, "failed to summarize evidence", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"summary": summary,
		})
	}
}
