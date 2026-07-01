package viewer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	domainbacklog "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/backlog"
)

type BacklogItem = domainbacklog.Item

type BacklogStore struct {
	path string
	mu   sync.Mutex
}

func NewBacklogStore(path string) *BacklogStore {
	return &BacklogStore{path: path}
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

func (s *BacklogStore) List(_ context.Context, limit int) ([]BacklogItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	latest := map[string]BacklogItem{}
	file, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []BacklogItem{}, nil
		}
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item BacklogItem
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			continue
		}
		item = normalizeBacklogItemForRead(item)
		latest[item.ItemID] = item
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	items := make([]BacklogItem, 0, len(latest))
	for _, item := range latest {
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].CheckOK != items[j].CheckOK {
			return !items[i].CheckOK
		}
		return items[i].UpdatedAt > items[j].UpdatedAt
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *BacklogStore) Save(_ context.Context, item BacklogItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	encoded, err := json.Marshal(normalizeBacklogItem(item))
	if err != nil {
		return err
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return nil
}

func normalizeBacklogItem(item BacklogItem) BacklogItem {
	now := time.Now().Format(time.RFC3339)
	item = normalizeBacklogItemBase(item)
	if item.CreatedAt == "" {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	if item.CheckOK {
		item.Status = "ok"
	}
	return item
}

func normalizeBacklogItemForRead(item BacklogItem) BacklogItem {
	now := time.Now().Format(time.RFC3339)
	item = normalizeBacklogItemBase(item)
	if item.CreatedAt == "" {
		item.CreatedAt = now
	}
	if item.UpdatedAt == "" {
		item.UpdatedAt = item.CreatedAt
	}
	if item.CheckOK {
		item.Status = "ok"
	}
	return item
}

func normalizeBacklogItemBase(item BacklogItem) BacklogItem {
	item.ItemID = strings.TrimSpace(item.ItemID)
	if item.ItemID == "" {
		item.ItemID = fmt.Sprintf("backlog-%d", time.Now().UnixNano())
	}
	item.Kind = normalizeBacklogKind(item.Kind)
	item.Title = strings.TrimSpace(item.Title)
	if item.Title == "" {
		item.Title = "untitled"
	}
	item.Body = strings.TrimSpace(item.Body)
	item.Source = normalizeBacklogSource(item.Source)
	item.Owner = normalizeBacklogSource(item.Owner)
	item.Status = normalizeBacklogStatus(item.Status, item.CheckOK)
	item.Priority = normalizeBacklogPriority(item.Priority)
	item.Implementer = normalizeBacklogSource(item.Implementer)
	item.Implementation = strings.TrimSpace(item.Implementation)
	item.TestResult = strings.TrimSpace(item.TestResult)
	item.CheckedBy = normalizeBacklogSource(item.CheckedBy)
	return item
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
	case "open", "implementing", "testing", "fixing", "blocked", "rejected", "ok":
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
