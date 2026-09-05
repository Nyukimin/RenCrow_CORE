package viewer

import (
	"context"
	"errors"
	"net/http"
	"strings"

	taskmanager "github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type TaskStore interface {
	ListTasks(context.Context, domaintask.Filter) ([]domaintask.Task, error)
	GetTask(context.Context, modulecore.TaskID) (domaintask.Task, error)
	GetContext(context.Context, modulecore.TaskID) (domaintask.SharedRoleContext, error)
	ListRuns(context.Context, domaintask.RunFilter) ([]domaintask.Run, error)
	ListNotifications(context.Context, int, bool) ([]domaintask.Notification, error)
}

func HandleTasks(store TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit, ok := parseOptionalLimit(w, r, 200)
		if !ok {
			return
		}
		items, err := store.ListTasks(r.Context(), domaintask.Filter{
			Status:   domaintask.Status(strings.TrimSpace(r.URL.Query().Get("status"))),
			ModuleID: strings.TrimSpace(r.URL.Query().Get("module_id")),
			Assignee: strings.TrimSpace(r.URL.Query().Get("assignee")),
			Route:    domaintask.Route(strings.TrimSpace(r.URL.Query().Get("route"))), Limit: limit,
		})
		if err != nil {
			http.Error(w, "failed to list tasks", http.StatusInternalServerError)
			return
		}
		writeMonitorJSON(w, map[string]any{"items": items})
	}
}

func HandleTaskDetail(store TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		taskID := modulecore.TaskID(strings.TrimSpace(r.URL.Query().Get("task_id")))
		if taskID == "" {
			http.Error(w, "task_id is required", http.StatusBadRequest)
			return
		}
		if err := taskID.Validate(); err != nil {
			http.Error(w, "task not found", http.StatusBadRequest)
			return
		}
		value, err := store.GetTask(r.Context(), taskID)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, taskmanager.ErrNotFound) {
				status = http.StatusNotFound
			}
			http.Error(w, "task not found", status)
			return
		}
		shared, err := store.GetContext(r.Context(), taskID)
		if err != nil && !errors.Is(err, taskmanager.ErrNotFound) {
			http.Error(w, "failed to get task context", http.StatusInternalServerError)
			return
		}
		runs, err := store.ListRuns(r.Context(), domaintask.RunFilter{TaskID: taskID})
		if err != nil {
			http.Error(w, "failed to list task runs", http.StatusInternalServerError)
			return
		}
		if runs == nil {
			runs = []domaintask.Run{}
		}
		writeMonitorJSON(w, map[string]any{"task": value, "context": shared, "runs": runs})
	}
}

func HandleTaskNotifications(store TaskStore) http.HandlerFunc {
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
			http.Error(w, "failed to list task notifications", http.StatusInternalServerError)
			return
		}
		writeMonitorJSON(w, map[string]any{"items": items})
	}
}
