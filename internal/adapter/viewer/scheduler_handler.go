package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	schedulerapp "github.com/Nyukimin/picoclaw_multiLLM/internal/application/scheduler"
	domainscheduler "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/scheduler"
)

type SchedulerStore interface {
	ListJobs(ctx context.Context, limit int) ([]domainscheduler.Job, error)
	SaveJob(ctx context.Context, job domainscheduler.Job) error
	SaveRunLog(ctx context.Context, log domainscheduler.RunLog) error
	ListRunLogs(ctx context.Context, limit int) ([]domainscheduler.RunLog, error)
}

func HandleScheduler(store SchedulerStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "scheduler store unavailable", http.StatusServiceUnavailable)
			return
		}
		switch r.Method {
		case http.MethodGet:
			limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 50, 200)
			if err != nil {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			jobs, err := store.ListJobs(r.Context(), limit)
			if err != nil {
				http.Error(w, "failed to load scheduler jobs", http.StatusInternalServerError)
				return
			}
			logs, err := store.ListRunLogs(r.Context(), limit)
			if err != nil {
				http.Error(w, "failed to load scheduler run logs", http.StatusInternalServerError)
				return
			}
			if jobs == nil {
				jobs = []domainscheduler.Job{}
			}
			if logs == nil {
				logs = []domainscheduler.RunLog{}
			}
			writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs, "run_logs": logs})
		case http.MethodPost:
			handleSchedulerPost(w, r, store)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func handleSchedulerPost(w http.ResponseWriter, r *http.Request, store SchedulerStore) {
	var req struct {
		Action     string              `json:"action"`
		Job        domainscheduler.Job `json:"job"`
		JobID      string              `json:"job_id"`
		DisabledBy string              `json:"disabled_by"`
		Trigger    string              `json:"trigger"`
		Limit      int                 `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid scheduler payload: "+err.Error(), http.StatusBadRequest)
		return
	}
	svc := schedulerapp.NewService(store, nil)
	switch strings.TrimSpace(req.Action) {
	case "create":
		job, err := svc.CreateJob(r.Context(), req.Job)
		if err != nil {
			http.Error(w, "failed to create scheduler job: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"job": job})
	case "run":
		log, err := svc.RunJob(r.Context(), firstNonEmptyString(req.JobID, req.Job.JobID), firstNonEmptyString(req.Trigger, "manual"))
		if err != nil {
			http.Error(w, "failed to run scheduler job: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"run_log": log})
	case "disable":
		job, err := svc.DisableJob(r.Context(), firstNonEmptyString(req.JobID, req.Job.JobID), req.DisabledBy)
		if err != nil {
			http.Error(w, "failed to disable scheduler job: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"job": job})
	case "due":
		limit := req.Limit
		if limit <= 0 {
			limit = 50
		}
		due, err := svc.DueJobs(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to list due scheduler jobs: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"due": due})
	default:
		http.Error(w, "action must be create, run, disable, or due", http.StatusBadRequest)
	}
}
