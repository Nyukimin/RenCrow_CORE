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
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	stateFilename         = "task_state.jsonl"
	runFilename           = "task_run.jsonl"
	contextFilename       = "task_context.jsonl"
	notificationsFilename = "task_notifications.jsonl"
)

type JSONLStore struct {
	mu                sync.RWMutex
	root              string
	statePath         string
	runPath           string
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
		statePath: filepath.Join(root, stateFilename), runPath: filepath.Join(root, runFilename), contextPath: filepath.Join(root, contextFilename),
		notificationsPath: filepath.Join(root, notificationsFilename),
	}
	for _, path := range []string{store.statePath, store.runPath, store.contextPath, store.notificationsPath} {
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

func (s *JSONLStore) SaveRun(ctx context.Context, value domaintask.Run) error {
	if err := value.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.loadTasks(ctx)
	if err != nil {
		return err
	}
	knownTask := false
	for _, item := range tasks {
		if item.TaskID == value.TaskID {
			knownTask = true
			break
		}
	}
	if !knownTask {
		return fmt.Errorf("run task is unavailable: %w", domaintask.ErrNotFound)
	}
	runs, err := s.loadRuns(ctx)
	if err != nil {
		return err
	}
	if err := validateRunOwners(runs, tasks); err != nil {
		return err
	}
	for _, item := range runs {
		if item.RunID == value.RunID {
			if err := validateRunUpdate(item, value); err != nil {
				return err
			}
			break
		}
	}
	if value.Status == domaintask.RunStatusRunning {
		for _, item := range runs {
			if item.TaskID == value.TaskID && item.Status == domaintask.RunStatusRunning && item.RunID != value.RunID {
				return fmt.Errorf("task already has active run %s", item.RunID)
			}
		}
	}
	return s.appendJSONUnlocked(ctx, s.runPath, value)
}

func (s *JSONLStore) GetRun(ctx context.Context, runID modulecore.RunID) (domaintask.Run, error) {
	if err := runID.Validate(); err != nil {
		return domaintask.Run{}, err
	}
	s.mu.RLock()
	items, err := s.loadRuns(ctx)
	if err == nil {
		tasks, taskErr := s.loadTasks(ctx)
		if taskErr != nil {
			err = taskErr
		} else {
			err = validateRunOwners(items, tasks)
		}
	}
	s.mu.RUnlock()
	if err != nil {
		return domaintask.Run{}, err
	}
	for _, item := range items {
		if item.RunID == runID {
			return item, nil
		}
	}
	return domaintask.Run{}, domaintask.ErrNotFound
}

func (s *JSONLStore) ListRuns(ctx context.Context, filter domaintask.RunFilter) ([]domaintask.Run, error) {
	if filter.TaskID != "" {
		if err := filter.TaskID.Validate(); err != nil {
			return nil, fmt.Errorf("task_id is invalid: %w", err)
		}
	}
	if filter.Status != "" && !domaintask.ValidRunStatus(filter.Status) {
		return nil, fmt.Errorf("invalid run status: %s", filter.Status)
	}
	s.mu.RLock()
	items, err := s.loadRuns(ctx)
	if err == nil {
		tasks, taskErr := s.loadTasks(ctx)
		if taskErr != nil {
			err = taskErr
		} else {
			err = validateRunOwners(items, tasks)
		}
	}
	s.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	filtered := make([]domaintask.Run, 0, len(items))
	for _, item := range items {
		if filter.TaskID != "" && item.TaskID != filter.TaskID {
			continue
		}
		if filter.Status != "" && item.Status != filter.Status {
			continue
		}
		filtered = append(filtered, item)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].StartedAt.Equal(filtered[j].StartedAt) {
			return string(filtered[i].RunID) < string(filtered[j].RunID)
		}
		return filtered[i].StartedAt.Before(filtered[j].StartedAt)
	})
	if filter.Limit > 0 && len(filtered) > filter.Limit {
		filtered = filtered[:filter.Limit]
	}
	return filtered, nil
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

func (s *JSONLStore) loadRuns(ctx context.Context) ([]domaintask.Run, error) {
	items, err := readJSONLLines[domaintask.Run](ctx, s.runPath)
	if err != nil {
		return nil, err
	}
	latest := make(map[modulecore.RunID]domaintask.Run, len(items))
	activeByTask := make(map[modulecore.TaskID]modulecore.RunID)
	for index, item := range items {
		if err := item.Validate(); err != nil {
			return nil, fmt.Errorf("run record %d is invalid: %w", index, err)
		}
		if previous, ok := latest[item.RunID]; ok {
			if err := validateRunUpdate(previous, item); err != nil {
				return nil, fmt.Errorf("run record %d update is invalid: %w", index, err)
			}
		}
		if item.Status == domaintask.RunStatusRunning {
			if activeID, ok := activeByTask[item.TaskID]; ok && activeID != item.RunID {
				return nil, fmt.Errorf("task %s has multiple active runs", item.TaskID)
			}
			activeByTask[item.TaskID] = item.RunID
		} else if activeID, ok := activeByTask[item.TaskID]; ok && activeID == item.RunID {
			delete(activeByTask, item.TaskID)
		}
		latest[item.RunID] = item
	}
	result := make([]domaintask.Run, 0, len(latest))
	for _, item := range latest {
		result = append(result, item)
	}
	return result, nil
}

func validateRunOwners(runs []domaintask.Run, tasks []domaintask.Task) error {
	knownTasks := make(map[modulecore.TaskID]struct{}, len(tasks))
	for _, item := range tasks {
		knownTasks[item.TaskID] = struct{}{}
	}
	for _, item := range runs {
		if _, ok := knownTasks[item.TaskID]; !ok {
			return fmt.Errorf("run task is unavailable: %w", domaintask.ErrNotFound)
		}
	}
	return nil
}

func validateRunUpdate(existing, next domaintask.Run) error {
	if existing.TaskID != next.TaskID || existing.StartReason != next.StartReason || existing.Assignee != next.Assignee || !existing.StartedAt.Equal(next.StartedAt) {
		return fmt.Errorf("run identity and start fields are immutable")
	}
	if !domaintask.CanRunTransition(existing.Status, next.Status) {
		return fmt.Errorf("invalid run status transition: %s -> %s", existing.Status, next.Status)
	}
	if existing.Status != domaintask.RunStatusRunning {
		if !sameRunTime(existing.CompletedAt, next.CompletedAt) || existing.Summary != next.Summary {
			return fmt.Errorf("closed run is immutable")
		}
	}
	return nil
}

func sameRunTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Equal(*right)
}

func (s *JSONLStore) appendJSON(ctx context.Context, path string, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendJSONUnlocked(ctx, path, value)
}

func (s *JSONLStore) appendJSONUnlocked(ctx context.Context, path string, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
