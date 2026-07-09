package viewer

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/jobmanager"
	domainjob "github.com/Nyukimin/RenCrow_CORE/internal/domain/job"
)

type ParallelJobStore interface {
	ListJobs(ctx context.Context, filter domainjob.Filter) ([]domainjob.Job, error)
	GetJob(ctx context.Context, jobID string) (domainjob.Job, error)
	GetContext(ctx context.Context, jobID string) (domainjob.SharedRoleContext, error)
	ListNotifications(ctx context.Context, limit int, interruptOnly bool) ([]domainjob.Notification, error)
}

func HandleParallelJobs(store ParallelJobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit, ok := parseOptionalLimit(w, r, 200)
		if !ok {
			return
		}
		items, err := store.ListJobs(r.Context(), domainjob.Filter{
			Status:   domainjob.Status(strings.TrimSpace(r.URL.Query().Get("status"))),
			ModuleID: strings.TrimSpace(r.URL.Query().Get("module_id")),
			Assignee: strings.TrimSpace(r.URL.Query().Get("assignee")),
			Route:    domainjob.Route(strings.TrimSpace(r.URL.Query().Get("route"))),
			Limit:    limit,
		})
		if err != nil {
			http.Error(w, "failed to list jobs", http.StatusInternalServerError)
			return
		}
		writeMonitorJSON(w, map[string]any{"items": items})
	}
}

func HandleParallelJobDetail(store ParallelJobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		jobID := strings.TrimSpace(r.URL.Query().Get("job_id"))
		if jobID == "" {
			http.Error(w, "job_id is required", http.StatusBadRequest)
			return
		}
		j, err := store.GetJob(r.Context(), jobID)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, jobmanager.ErrNotFound) {
				status = http.StatusNotFound
			}
			http.Error(w, "job not found", status)
			return
		}
		shared, err := store.GetContext(r.Context(), jobID)
		if err != nil && !errors.Is(err, jobmanager.ErrNotFound) {
			http.Error(w, "failed to get job context", http.StatusInternalServerError)
			return
		}
		writeMonitorJSON(w, map[string]any{"job": j, "context": shared})
	}
}

func HandleJobNotifications(store ParallelJobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit, ok := parseOptionalLimit(w, r, 100)
		if !ok {
			return
		}
		interruptOnly := !strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("interrupt_only")), "false")
		items, err := store.ListNotifications(r.Context(), limit, interruptOnly)
		if err != nil {
			http.Error(w, "failed to list notifications", http.StatusInternalServerError)
			return
		}
		writeMonitorJSON(w, map[string]any{"items": items})
	}
}
