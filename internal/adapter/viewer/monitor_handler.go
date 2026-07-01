package viewer

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

func HandleMonitorStatus(store *MonitorStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeMonitorJSON(w, map[string]any{"status": store.Status()})
	}
}

func HandleMonitorAgents(store *MonitorStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeMonitorJSON(w, map[string]any{"agents": store.Agents()})
	}
}

func HandleMonitorAgentDetail(store *MonitorStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		limit, ok := parseOptionalLimit(w, r, 200)
		if !ok {
			return
		}
		item, found := store.AgentDetail(r.Context(), strings.ToLower(id), limit)
		if !found {
			http.Error(w, "agent not found", http.StatusNotFound)
			return
		}
		writeMonitorJSON(w, item)
	}
}

func HandleMonitorJobs(store *MonitorStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit, ok := parseOptionalLimit(w, r, 100)
		if !ok {
			return
		}
		items := store.Jobs(JobFilter{
			Route:     strings.TrimSpace(r.URL.Query().Get("route")),
			Status:    strings.TrimSpace(r.URL.Query().Get("status")),
			Owner:     strings.TrimSpace(r.URL.Query().Get("owner")),
			SessionID: strings.TrimSpace(r.URL.Query().Get("session_id")),
			ChatID:    strings.TrimSpace(r.URL.Query().Get("chat_id")),
			Limit:     limit,
		})
		writeMonitorJSON(w, map[string]any{"items": items})
	}
}

func HandleMonitorLogs(store *MonitorStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit, ok := parseOptionalLimit(w, r, 200)
		if !ok {
			return
		}
		filter := LogFilter{
			Type:      strings.TrimSpace(r.URL.Query().Get("type")),
			Agent:     strings.TrimSpace(r.URL.Query().Get("agent")),
			Route:     strings.TrimSpace(r.URL.Query().Get("route")),
			JobID:     strings.TrimSpace(r.URL.Query().Get("job_id")),
			SessionID: strings.TrimSpace(r.URL.Query().Get("session_id")),
			ChatID:    strings.TrimSpace(r.URL.Query().Get("chat_id")),
			Limit:     limit,
		}
		items := store.Logs(filter)
		if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("scope")), "persisted") {
			archived, err := store.ArchivedLogs(r.Context(), filter)
			if err != nil {
				http.Error(w, "failed to load persisted logs", http.StatusInternalServerError)
				return
			}
			items = archived
		}
		writeMonitorJSON(w, map[string]any{"items": items})
	}
}

func HandleMonitorAuditSummary(store *MonitorStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeMonitorJSON(w, map[string]any{"summary": store.Summary()})
	}
}

func HandleMonitorJobDetail(store *MonitorStore) http.HandlerFunc {
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
		item, ok := store.JobDetail(r.Context(), jobID)
		if !ok {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}
		writeMonitorJSON(w, item)
	}
}

func parseOptionalLimit(w http.ResponseWriter, r *http.Request, max int) (int, bool) {
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return 0, false
		}
		if n > max {
			n = max
		}
		limit = n
	}
	return limit, true
}

func writeMonitorJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
