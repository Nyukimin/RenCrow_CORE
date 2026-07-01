package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	domainverification "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/verification"
)

type VerificationReportReader interface {
	ListRecent(ctx context.Context, limit int) ([]domainverification.VerificationReport, error)
	GetByJobID(ctx context.Context, jobID string) (domainverification.VerificationReport, error)
	Summary(ctx context.Context) (map[string]map[string]int, error)
}

func HandleVerificationUnavailable() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Query().Get("viewer_optional") == "1" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":     false,
				"status": http.StatusServiceUnavailable,
				"error":  "verification store unavailable",
			})
			return
		}
		http.Error(w, "verification store unavailable", http.StatusServiceUnavailable)
	}
}

func HandleVerificationRecent(store VerificationReportReader) http.HandlerFunc {
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
		items, err := store.ListRecent(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load verification reports", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	}
}

func HandleVerificationDetail(store VerificationReportReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		jobID := r.URL.Query().Get("job_id")
		if jobID == "" {
			http.Error(w, "job_id is required", http.StatusBadRequest)
			return
		}
		item, err := store.GetByJobID(r.Context(), jobID)
		if err != nil {
			http.Error(w, "verification report not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"item": item})
	}
}

func HandleVerificationSummary(store VerificationReportReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		summary, err := store.Summary(r.Context())
		if err != nil {
			http.Error(w, "failed to summarize verification reports", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"summary": summary})
	}
}
