package viewer

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

type ExtensionHealthItem struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Source     string `json:"source"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	Configured bool   `json:"configured"`
	Loaded     bool   `json:"loaded"`
}

type ExtensionHealthOptions struct {
	Items []ExtensionHealthItem
	Now   func() time.Time
}

func HandleExtensionHealth(opts ExtensionHealthOptions) http.HandlerFunc {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	items := normalizeExtensionHealthItems(opts.Items)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		summary := map[string]int{}
		for _, item := range items {
			summary[item.Status]++
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"checked_at": now().UTC().Format(time.RFC3339),
			"summary":    summary,
			"extensions": items,
		})
	}
}

func normalizeExtensionHealthItems(items []ExtensionHealthItem) []ExtensionHealthItem {
	out := make([]ExtensionHealthItem, 0, len(items))
	for _, item := range items {
		item.ID = normalizeExtensionHealthToken(item.ID)
		item.Kind = normalizeExtensionHealthToken(item.Kind)
		item.Name = strings.TrimSpace(item.Name)
		item.Source = strings.TrimSpace(item.Source)
		item.Status = normalizeExtensionHealthStatus(item.Status, item.Configured, item.Loaded)
		item.Message = strings.TrimSpace(item.Message)
		if item.ID == "" || item.Kind == "" || item.Name == "" {
			continue
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind == out[j].Kind {
			return out[i].ID < out[j].ID
		}
		return out[i].Kind < out[j].Kind
	})
	if out == nil {
		return []ExtensionHealthItem{}
	}
	return out
}

func normalizeExtensionHealthStatus(status string, configured bool, loaded bool) string {
	status = normalizeExtensionHealthToken(status)
	switch status {
	case "ok", "broken", "permission_denied", "unconfigured", "loaded", "missing":
		return status
	}
	if loaded {
		return "ok"
	}
	if configured {
		return "missing"
	}
	return "unconfigured"
}

func normalizeExtensionHealthToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "_")
	value = strings.ReplaceAll(value, "-", "_")
	return value
}
