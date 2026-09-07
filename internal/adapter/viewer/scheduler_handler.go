package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	schedulerapp "github.com/Nyukimin/RenCrow_CORE/internal/application/scheduler"
	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
)

type SchedulerStore interface {
	ListSchedules(ctx context.Context, limit int) ([]domainscheduler.Schedule, error)
	SaveSchedule(ctx context.Context, schedule domainscheduler.Schedule) error
	SaveRunLog(ctx context.Context, log domainscheduler.RunLog) error
	ListRunLogs(ctx context.Context, limit int) ([]domainscheduler.RunLog, error)
}

func HandleScheduler(store SchedulerStore) http.HandlerFunc {
	return HandleSchedulerWithExecutor(store, nil)
}

func HandleSchedulerWithExecutor(store SchedulerStore, executor schedulerapp.Executor) http.HandlerFunc {
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
			schedules, err := store.ListSchedules(r.Context(), limit)
			if err != nil {
				http.Error(w, "failed to load scheduler schedules", http.StatusInternalServerError)
				return
			}
			logs, err := store.ListRunLogs(r.Context(), limit)
			if err != nil {
				http.Error(w, "failed to load scheduler run logs", http.StatusInternalServerError)
				return
			}
			if schedules == nil {
				schedules = []domainscheduler.Schedule{}
			}
			if logs == nil {
				logs = []domainscheduler.RunLog{}
			}
			writeJSON(w, http.StatusOK, map[string]any{"schedules": schedules, "run_logs": logs})
		case http.MethodPost:
			handleSchedulerPost(w, r, store, executor)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func handleSchedulerPost(w http.ResponseWriter, r *http.Request, store SchedulerStore, executor schedulerapp.Executor) {
	var req struct {
		Action     string                   `json:"action"`
		Schedule   domainscheduler.Schedule `json:"schedule"`
		ScheduleID string                   `json:"schedule_id"`
		DisabledBy string                   `json:"disabled_by"`
		Trigger    string                   `json:"trigger"`
		Limit      int                      `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid scheduler payload: "+err.Error(), http.StatusBadRequest)
		return
	}
	svc := schedulerapp.NewService(store, executor)
	switch strings.TrimSpace(req.Action) {
	case "create":
		schedule, err := svc.CreateSchedule(r.Context(), req.Schedule)
		if err != nil {
			http.Error(w, "failed to create scheduler schedule: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"schedule": schedule})
	case "run":
		log, err := svc.RunSchedule(r.Context(), firstNonEmptyString(req.ScheduleID, string(req.Schedule.ScheduleID)), firstNonEmptyString(req.Trigger, "manual"))
		if err != nil {
			http.Error(w, "failed to run scheduler schedule: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"run_log": log})
	case "disable":
		schedule, err := svc.DisableSchedule(r.Context(), firstNonEmptyString(req.ScheduleID, string(req.Schedule.ScheduleID)), req.DisabledBy)
		if err != nil {
			http.Error(w, "failed to disable scheduler schedule: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"schedule": schedule})
	case "due":
		limit := req.Limit
		if limit <= 0 {
			limit = 50
		}
		due, err := svc.DueSchedules(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to list due scheduler schedules: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"due": due})
	default:
		http.Error(w, "action must be create, run, disable, or due", http.StatusBadRequest)
	}
}
