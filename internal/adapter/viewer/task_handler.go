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

// PublicTaskVisibility is trusted runtime policy for exposing a Task through
// the public Viewer routes. The handler never derives visibility from request
// parameters or caller-supplied identity.
type PublicTaskVisibility func(context.Context, domaintask.Task) (bool, error)

func HandleTasks(store TaskStore) http.HandlerFunc {
	return HandleTasksWithPublicVisibility(store, nil)
}

func HandleTasksWithPublicVisibility(store TaskStore, visibility PublicTaskVisibility) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit, ok := parseOptionalLimit(w, r, 200)
		if !ok {
			return
		}
		if visibility == nil {
			writeMonitorJSON(w, map[string]any{"items": []domaintask.Task{}})
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
		// First select candidate Tasks, then revalidate them immediately before
		// response admission. A later origin lookup may block while an earlier
		// Session is revoked or rebound.
		visibleItems := filterPublicTasks(r.Context(), visibility, items)
		visibleItems = filterPublicTasks(r.Context(), visibility, visibleItems)
		writeMonitorJSON(w, map[string]any{"items": visibleItems})
	}
}

func HandleTaskDetail(store TaskStore) http.HandlerFunc {
	return HandleTaskDetailWithPublicVisibility(store, nil)
}

func HandleTaskDetailWithPublicVisibility(store TaskStore, visibility PublicTaskVisibility) http.HandlerFunc {
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
		if visibility == nil {
			http.Error(w, "task not found", http.StatusNotFound)
			return
		}
		value, err := store.GetTask(r.Context(), taskID)
		if err != nil || value.TaskID != taskID {
			http.Error(w, "task not found", http.StatusNotFound)
			return
		}
		if !publicTaskVisible(r.Context(), visibility, value) {
			http.Error(w, "task not found", http.StatusNotFound)
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
		// Loading shared context and Run history can take time. Re-resolve the
		// trusted origin binding at the response boundary so a Session lifecycle
		// change during those reads cannot expose a stale Task snapshot.
		if !publicTaskVisible(r.Context(), visibility, value) {
			http.Error(w, "task not found", http.StatusNotFound)
			return
		}
		writeMonitorJSON(w, map[string]any{"task": value, "context": shared, "runs": runs})
	}
}

func HandleTaskNotifications(store TaskStore) http.HandlerFunc {
	return HandleTaskNotificationsWithPublicVisibility(store, nil)
}

func HandleTaskNotificationsWithPublicVisibility(store TaskStore, visibility PublicTaskVisibility) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit, ok := parseOptionalLimit(w, r, 100)
		if !ok {
			return
		}
		if visibility == nil {
			writeMonitorJSON(w, map[string]any{"items": []domaintask.Notification{}})
			return
		}
		interruptOnly := !strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("interrupt_only")), "false")
		items, err := store.ListNotifications(r.Context(), limit, interruptOnly)
		if err != nil {
			http.Error(w, "failed to list task notifications", http.StatusInternalServerError)
			return
		}
		type notificationCandidate struct {
			item domaintask.Notification
			task domaintask.Task
		}
		candidates := make([]notificationCandidate, 0, len(items))
		for _, item := range items {
			task, err := store.GetTask(r.Context(), item.TaskID)
			if err != nil || task.TaskID != item.TaskID {
				continue
			}
			candidates = append(candidates, notificationCandidate{item: item, task: task})
		}
		// Complete every Task lookup before resolving publication visibility. A
		// later GetTask may be slow while an earlier Session is being rebound.
		visibleCandidates := make([]notificationCandidate, 0, len(candidates))
		for _, candidate := range candidates {
			if publicTaskVisible(r.Context(), visibility, candidate.task) {
				visibleCandidates = append(visibleCandidates, candidate)
			}
		}
		// Re-resolve every candidate at the response boundary after all Task
		// storage I/O and the first pass of current-origin checks.
		visibleItems := make([]domaintask.Notification, 0, len(visibleCandidates))
		for _, candidate := range visibleCandidates {
			if publicTaskVisible(r.Context(), visibility, candidate.task) {
				visibleItems = append(visibleItems, candidate.item)
			}
		}
		writeMonitorJSON(w, map[string]any{"items": visibleItems})
	}
}

func filterPublicTasks(ctx context.Context, visibility PublicTaskVisibility, tasks []domaintask.Task) []domaintask.Task {
	visible := make([]domaintask.Task, 0, len(tasks))
	for _, task := range tasks {
		if publicTaskVisible(ctx, visibility, task) {
			visible = append(visible, task)
		}
	}
	return visible
}

func publicTaskVisible(ctx context.Context, visibility PublicTaskVisibility, task domaintask.Task) bool {
	if visibility == nil {
		return false
	}
	visible, err := visibility(ctx, task)
	return err == nil && visible
}
