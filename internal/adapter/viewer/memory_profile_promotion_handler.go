package viewer

import (
	"context"
	"net/http"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

type MemoryProfilePromotionLister interface {
	ListProfilePromotionJobs(context.Context, int) ([]domainmemory.ProfilePromotionJob, error)
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
				"jobs": []domainmemory.ProfilePromotionJob{}, "job_count": 0, "failed_count": 0,
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
		failed := 0
		for _, job := range jobs {
			if job.State == domainmemory.ProfilePromotionFailed {
				failed++
			}
		}
		status := "ok"
		if failed > 0 {
			status = "needs_review"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": status, "warnings": []string{}, "jobs": jobs,
			"job_count": len(jobs), "failed_count": failed,
		})
	}
}
