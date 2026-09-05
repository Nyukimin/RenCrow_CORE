package task

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	stateFilename         = "task_state.jsonl"
	contextFilename       = "task_context.jsonl"
	notificationsFilename = "task_notifications.jsonl"
)

type JSONLStore struct {
	mu                sync.RWMutex
	root              string
	statePath         string
	contextPath       string
	notificationsPath string
}

func NewJSONLStore(root string) (*JSONLStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("task store root is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	for _, filename := range []string{"job_state.jsonl", "job_context.jsonl", "job_notifications.jsonl"} {
		if _, err := os.Lstat(filepath.Join(root, filename)); err == nil {
			return nil, fmt.Errorf("legacy task store file %s is not supported", filename)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	store := &JSONLStore{
		root:      root,
		statePath: filepath.Join(root, stateFilename), contextPath: filepath.Join(root, contextFilename),
		notificationsPath: filepath.Join(root, notificationsFilename),
	}
	for _, path := range []string{store.statePath, store.contextPath, store.notificationsPath} {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (s *JSONLStore) SaveTask(ctx context.Context, value domaintask.Task) error {
	if err := value.Validate(); err != nil {
		return err
	}
	return s.appendJSON(ctx, s.statePath, value)
}

func (s *JSONLStore) GetTask(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	if err := taskID.Validate(); err != nil {
		return domaintask.Task{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items, err := s.loadTasks(ctx)
	if err != nil {
		return domaintask.Task{}, err
	}
	for _, item := range items {
		if item.TaskID == taskID {
			return item, nil
		}
	}
	return domaintask.Task{}, domaintask.ErrNotFound
}

func (s *JSONLStore) ListTasks(ctx context.Context, filter domaintask.Filter) ([]domaintask.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items, err := s.loadTasks(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]domaintask.Task, 0, len(items))
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

func (s *JSONLStore) SaveContext(ctx context.Context, value domaintask.SharedRoleContext) error {
	if err := value.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	if _, err := s.GetTask(ctx, value.TaskID); err != nil {
		return err
	}
	return s.appendJSON(ctx, s.contextPath, value)
}

func (s *JSONLStore) GetContext(ctx context.Context, taskID modulecore.TaskID) (domaintask.SharedRoleContext, error) {
	if err := taskID.Validate(); err != nil {
		return domaintask.SharedRoleContext{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items, err := readJSONLLines[domaintask.SharedRoleContext](ctx, s.contextPath)
	if err != nil {
		return domaintask.SharedRoleContext{}, err
	}
	for index := len(items) - 1; index >= 0; index-- {
		if items[index].TaskID == taskID {
			return items[index], nil
		}
	}
	return domaintask.SharedRoleContext{}, domaintask.ErrNotFound
}

func (s *JSONLStore) SaveNotification(ctx context.Context, value domaintask.Notification) error {
	if err := value.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	if strings.TrimSpace(value.Type) == "" {
		return fmt.Errorf("notification type is required")
	}
	return s.appendJSON(ctx, s.notificationsPath, value)
}

func (s *JSONLStore) ListNotifications(ctx context.Context, limit int, interruptOnly bool) ([]domaintask.Notification, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items, err := readJSONLLines[domaintask.Notification](ctx, s.notificationsPath)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return string(items[i].TaskID) < string(items[j].TaskID)
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	filtered := make([]domaintask.Notification, 0, len(items))
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

func (s *JSONLStore) loadTasks(ctx context.Context) ([]domaintask.Task, error) {
	items, err := readJSONLLines[domaintask.Task](ctx, s.statePath)
	if err != nil {
		return nil, err
	}
	latest := make(map[modulecore.TaskID]domaintask.Task, len(items))
	for _, item := range items {
		if err := item.Validate(); err != nil {
			return nil, err
		}
		latest[item.TaskID] = item
	}
	result := make([]domaintask.Task, 0, len(latest))
	for _, item := range latest {
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return string(result[i].TaskID) < string(result[j].TaskID)
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result, nil
}

func (s *JSONLStore) appendJSON(ctx context.Context, path string, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	return encoder.Encode(value)
}

func readJSONLLines[T any](ctx context.Context, path string) ([]T, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	result := make([]T, 0)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var value T
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			if err == nil {
				return nil, fmt.Errorf("JSONL record contains trailing value")
			}
			return nil, err
		}
		result = append(result, value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
