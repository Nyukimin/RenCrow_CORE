package job

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	domainjob "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/job"
)

type JSONLStore struct {
	mu                sync.Mutex
	root              string
	jobsPath          string
	contextPath       string
	notificationsPath string
}

func NewJSONLStore(root string) (*JSONLStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("job store root is required")
	}
	s := &JSONLStore{
		root:              root,
		jobsPath:          filepath.Join(root, "job_state.jsonl"),
		contextPath:       filepath.Join(root, "job_context.jsonl"),
		notificationsPath: filepath.Join(root, "job_notifications.jsonl"),
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	for _, p := range []string{s.jobsPath, s.contextPath, s.notificationsPath} {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
		_ = f.Close()
	}
	return s, nil
}

func (s *JSONLStore) SaveJob(ctx context.Context, j domainjob.Job) error {
	if err := j.Validate(); err != nil {
		return err
	}
	return s.appendJSON(ctx, s.jobsPath, j)
}

func (s *JSONLStore) GetJob(ctx context.Context, jobID string) (domainjob.Job, error) {
	items, err := s.loadJobs(ctx)
	if err != nil {
		return domainjob.Job{}, err
	}
	for _, item := range items {
		if item.JobID == jobID {
			return item, nil
		}
	}
	return domainjob.Job{}, domainjob.ErrNotFound
}

func (s *JSONLStore) ListJobs(ctx context.Context, filter domainjob.Filter) ([]domainjob.Job, error) {
	items, err := s.loadJobs(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]domainjob.Job, 0, len(items))
	for _, item := range items {
		if filter.Status != "" && item.Status != filter.Status {
			continue
		}
		if filter.ModuleID != "" && item.ModuleID != filter.ModuleID {
			continue
		}
		if filter.Assignee != "" && !strings.EqualFold(item.Assignee, filter.Assignee) {
			continue
		}
		if filter.Route != "" && item.Route != filter.Route {
			continue
		}
		filtered = append(filtered, item)
	}
	if filter.Limit > 0 && len(filtered) > filter.Limit {
		filtered = filtered[:filter.Limit]
	}
	return filtered, nil
}

func (s *JSONLStore) SaveContext(ctx context.Context, c domainjob.SharedRoleContext) error {
	if strings.TrimSpace(c.JobID) == "" {
		return fmt.Errorf("job_id is required")
	}
	return s.appendJSON(ctx, s.contextPath, c)
}

func (s *JSONLStore) GetContext(ctx context.Context, jobID string) (domainjob.SharedRoleContext, error) {
	items, err := s.loadContexts(ctx)
	if err != nil {
		return domainjob.SharedRoleContext{}, err
	}
	for _, item := range items {
		if item.JobID == jobID {
			return item, nil
		}
	}
	return domainjob.SharedRoleContext{}, domainjob.ErrNotFound
}

func (s *JSONLStore) SaveNotification(ctx context.Context, n domainjob.Notification) error {
	if strings.TrimSpace(n.JobID) == "" {
		return fmt.Errorf("job_id is required")
	}
	return s.appendJSON(ctx, s.notificationsPath, n)
}

func (s *JSONLStore) ListNotifications(ctx context.Context, limit int, interruptOnly bool) ([]domainjob.Notification, error) {
	items, err := readJSONLLines[domainjob.Notification](ctx, s.notificationsPath)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	filtered := make([]domainjob.Notification, 0, len(items))
	for _, item := range items {
		if interruptOnly && !item.Interrupt {
			continue
		}
		filtered = append(filtered, item)
	}
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func (s *JSONLStore) loadJobs(ctx context.Context) ([]domainjob.Job, error) {
	items, err := readJSONLLines[domainjob.Job](ctx, s.jobsPath)
	if err != nil {
		return nil, err
	}
	latest := make(map[string]domainjob.Job, len(items))
	for _, item := range items {
		latest[item.JobID] = item
	}
	result := make([]domainjob.Job, 0, len(latest))
	for _, item := range latest {
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result, nil
}

func (s *JSONLStore) loadContexts(ctx context.Context) ([]domainjob.SharedRoleContext, error) {
	items, err := readJSONLLines[domainjob.SharedRoleContext](ctx, s.contextPath)
	if err != nil {
		return nil, err
	}
	latest := make(map[string]domainjob.SharedRoleContext, len(items))
	for _, item := range items {
		latest[item.JobID] = item
	}
	result := make([]domainjob.SharedRoleContext, 0, len(latest))
	for _, item := range latest {
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result, nil
}

func (s *JSONLStore) appendJSON(ctx context.Context, path string, v any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	return enc.Encode(v)
}

func readJSONLLines[T any](ctx context.Context, path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var out []T
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item T
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
