package viewer

import (
	"context"
	"net/http"
	"strings"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

type MemoryProfilePromotionLister interface {
	ListProfilePromotionJobs(context.Context, int) ([]domainmemory.ProfilePromotionJob, error)
	ProfilePromotionDiagnostics(context.Context) (domainmemory.ProfilePromotionDiagnostics, error)
}

type MemoryProfilePromotionRetryer interface {
	RetryFailedProfilePromotionJobs(context.Context, time.Time) (domainmemory.ProfilePromotionRetryResult, error)
}

func HandleMemoryProfilePromotions(store MemoryProfilePromotionLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "unavailable", "warnings": []string{"L1 store unavailable"},
				"jobs": []domainmemory.ProfilePromotionJob{}, "job_count": 0,
				"state_counts": map[string]int{}, "failed_count": 0,
				"retryable_failed_count": 0, "missing_evidence_failed_count": 0,
				"db_pool_stats": domainmemory.L1DBPoolStats{},
			})
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 50, 200)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		jobs, err := store.ListProfilePromotionJobs(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load profile promotion jobs", http.StatusInternalServerError)
			return
		}
		if jobs == nil {
			jobs = []domainmemory.ProfilePromotionJob{}
		}
		diagnostics, err := store.ProfilePromotionDiagnostics(r.Context())
		if err != nil {
			http.Error(w, "failed to load profile promotion diagnostics", http.StatusInternalServerError)
			return
		}
		failed := diagnostics.FailedCount
		status := "ok"
		if failed > 0 {
			status = "needs_review"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": status, "warnings": []string{}, "jobs": jobs,
			"job_count": len(jobs), "state_counts": diagnostics.StateCounts,
			"failed_count": failed, "retryable_failed_count": diagnostics.RetryableFailedCount,
			"missing_evidence_failed_count": diagnostics.MissingEvidenceFailedCount,
			"db_pool_stats":                 diagnostics.DBPoolStats,
		})
	}
}

func HandleMemoryProfilePromotionRetry(store MemoryProfilePromotionRetryer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(r.Header.Get("X-RenCrow-Client")) != "RenCrow_CMD" ||
			strings.ToLower(strings.TrimSpace(r.Header.Get("X-RenCrow-Interaction-Profile"))) != "cmd-control" {
			http.Error(w, "interaction profileでは許可されていない操作です", http.StatusForbidden)
			return
		}
		if store == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "unavailable", "warnings": []string{"L1 store unavailable"},
			})
			return
		}
		result, err := store.RetryFailedProfilePromotionJobs(r.Context(), time.Now().UTC())
		if err != nil {
			http.Error(w, "failed to retry profile promotion jobs", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}
