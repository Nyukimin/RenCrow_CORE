package viewer

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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
		if !validateMonitorLogQuery(w, r) {
			return
		}
		var taskID modulecore.TaskID
		var eventID modulecore.EventID
		if raw := strings.TrimSpace(r.URL.Query().Get("event_id")); raw != "" {
			eventID = modulecore.EventID(raw)
			if err := eventID.Validate(); err != nil {
				http.Error(w, "invalid event_id", http.StatusBadRequest)
				return
			}
		}
		if raw := strings.TrimSpace(r.URL.Query().Get("task_id")); raw != "" {
			parsed, err := modulecore.ParseTaskID(raw)
			if err != nil {
				http.Error(w, "invalid task_id", http.StatusBadRequest)
				return
			}
			taskID = parsed
		}
		filter := LogFilter{
			EventID:   eventID,
			Type:      strings.TrimSpace(r.URL.Query().Get("type")),
			Agent:     strings.TrimSpace(r.URL.Query().Get("agent")),
			Route:     strings.TrimSpace(r.URL.Query().Get("route")),
			TaskID:    taskID,
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

func validateMonitorLogQuery(w http.ResponseWriter, r *http.Request) bool {
	allowed := map[string]struct{}{
		"event_id": {}, "type": {}, "agent": {}, "route": {}, "task_id": {}, "session_id": {},
		"chat_id": {}, "limit": {}, "scope": {},
	}
	for key := range r.URL.Query() {
		if _, ok := allowed[key]; !ok {
			http.Error(w, "unsupported monitor filter", http.StatusBadRequest)
			return false
		}
	}
	return true
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
