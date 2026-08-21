package viewer

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	backlogpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/backlog"
)

type BacklogItem = domainbacklog.Item

// BacklogStore remains the viewer-facing name while persistence lives in the
// feature-owned infrastructure package. This keeps old callers source
// compatible without creating a second backlog store.
type BacklogStore = backlogpersistence.JSONLStore

func NewBacklogStore(path string) *BacklogStore {
	return backlogpersistence.NewJSONLStore(path)
}

func HandleBacklog(store *BacklogStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "backlog unavailable", http.StatusServiceUnavailable)
			return
		}
		switch r.Method {
		case http.MethodGet:
			limit, ok := parseOptionalLimit(w, r, 200)
			if !ok {
				return
			}
			items, err := store.List(r.Context(), limit)
			if err != nil {
				http.Error(w, "failed to list backlog", http.StatusInternalServerError)
				return
			}
			kind := strings.TrimSpace(r.URL.Query().Get("kind"))
			status := strings.TrimSpace(r.URL.Query().Get("status"))
			items = filterBacklogItems(items, kind, status)
			writeMonitorJSON(w, map[string]any{"items": items})
		case http.MethodPost:
			var item BacklogItem
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024)).Decode(&item); err != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			item = normalizeBacklogItem(item)
			if err := store.Save(r.Context(), item); err != nil {
				http.Error(w, "failed to save backlog item", http.StatusInternalServerError)
				return
			}
			writeMonitorJSON(w, map[string]any{"item": item})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func normalizeBacklogItem(item BacklogItem) BacklogItem {
	return backlogpersistence.NormalizeForSave(item, time.Now().UTC())
}

func normalizeBacklogItemForRead(item BacklogItem) BacklogItem {
	return backlogpersistence.NormalizeForRead(item)
}

func normalizeBacklogItemBase(item BacklogItem) BacklogItem {
	return backlogpersistence.NormalizeForRead(item)
}

func normalizeBacklogKind(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "unimplemented", "todo", "task":
		return "unimplemented"
	default:
		return "idea"
	}
}

func normalizeBacklogStatus(v string, checkOK bool) string {
	if checkOK {
		return "ok"
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case domainbacklog.StatusProposalReview, "open", "implementing", "testing", "fixing", "blocked", "rejected", "ok":
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return "open"
	}
}

func normalizeBacklogPriority(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "low", "normal", "high", "urgent":
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return "normal"
	}
}

func normalizeBacklogSource(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "mio", "shiro", "ren", "user", "coder", "worker", "heavy", "wild":
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return strings.TrimSpace(v)
	}
}

func filterBacklogItems(items []BacklogItem, kind, status string) []BacklogItem {
	kind = strings.ToLower(strings.TrimSpace(kind))
	status = strings.ToLower(strings.TrimSpace(status))
	if kind == "" && status == "" {
		return items
	}
	out := make([]BacklogItem, 0, len(items))
	for _, item := range items {
		if kind != "" && item.Kind != kind {
			continue
		}
		if status != "" && item.Status != status {
			continue
		}
		out = append(out, item)
	}
	return out
}
