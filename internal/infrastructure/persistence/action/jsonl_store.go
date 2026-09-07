package action

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	actionFilename  = "action_state.jsonl"
	attemptFilename = "action_attempt.jsonl"
)

type JSONLStore struct {
	mu          sync.RWMutex
	root        string
	actionPath  string
	attemptPath string
}

func NewJSONLStore(root string) (*JSONLStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("action store root is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	store := &JSONLStore{
		root:        root,
		actionPath:  filepath.Join(root, actionFilename),
		attemptPath: filepath.Join(root, attemptFilename),
	}
	for _, path := range []string{store.actionPath, store.attemptPath} {
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

func (s *JSONLStore) SaveAction(ctx context.Context, value domainaction.Action) error {
	if err := value.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	actions, err := s.loadActions(ctx)
	if err != nil {
		return err
	}
	for _, item := range actions {
		if item.ActionID == value.ActionID {
			if item.TaskID != value.TaskID || item.RunID != value.RunID || item.Kind != value.Kind || !item.CreatedAt.Equal(value.CreatedAt) {
				return fmt.Errorf("action identity fields are immutable")
			}
			break
		}
	}
	return s.appendJSONUnlocked(ctx, s.actionPath, value)
}

func (s *JSONLStore) GetAction(ctx context.Context, actionID modulecore.ActionID) (domainaction.Action, error) {
	if err := actionID.Validate(); err != nil {
		return domainaction.Action{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items, err := s.loadActions(ctx)
	if err != nil {
		return domainaction.Action{}, err
	}
	for _, item := range items {
		if item.ActionID == actionID {
			return item, nil
		}
	}
	return domainaction.Action{}, domainaction.ErrNotFound
}

func (s *JSONLStore) ListActions(ctx context.Context, filter domainaction.Filter) ([]domainaction.Action, error) {
	if filter.TaskID != "" {
		if err := filter.TaskID.Validate(); err != nil {
			return nil, fmt.Errorf("task_id is invalid: %w", err)
		}
	}
	if filter.RunID != "" {
		if err := filter.RunID.Validate(); err != nil {
			return nil, fmt.Errorf("run_id is invalid: %w", err)
		}
	}
	if filter.Status != "" && !domainaction.ValidStatus(filter.Status) {
		return nil, fmt.Errorf("invalid action status: %s", filter.Status)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items, err := s.loadActions(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]domainaction.Action, 0, len(items))
	for _, item := range items {
		if filter.TaskID != "" && item.TaskID != filter.TaskID {
			continue
		}
		if filter.RunID != "" && item.RunID != filter.RunID {
			continue
		}
		if filter.Status != "" && item.Status != filter.Status {
			continue
		}
		filtered = append(filtered, item)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].CreatedAt.Equal(filtered[j].CreatedAt) {
			return string(filtered[i].ActionID) < string(filtered[j].ActionID)
		}
		return filtered[i].CreatedAt.Before(filtered[j].CreatedAt)
	})
	if filter.Limit > 0 && len(filtered) > filter.Limit {
		filtered = filtered[:filter.Limit]
	}
	return filtered, nil
}

func (s *JSONLStore) SaveAttempt(ctx context.Context, value domainaction.Attempt) error {
	if err := value.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	actions, err := s.loadActions(ctx)
	if err != nil {
		return err
	}
	knownAction := false
	for _, item := range actions {
		if item.ActionID == value.ActionID {
			knownAction = true
			break
		}
	}
	if !knownAction {
		return fmt.Errorf("attempt action is unavailable: %w", domainaction.ErrNotFound)
	}
	attempts, err := s.loadAttempts(ctx)
	if err != nil {
		return err
	}
	for _, item := range attempts {
		if item.AttemptID == value.AttemptID {
			if item.ActionID != value.ActionID || item.StartReason != value.StartReason || !item.StartedAt.Equal(value.StartedAt) {
				return fmt.Errorf("attempt identity and start fields are immutable")
			}
			break
		}
	}
	if value.Status == domainaction.AttemptStatusRunning {
		for _, item := range attempts {
			if item.ActionID == value.ActionID && item.Status == domainaction.AttemptStatusRunning && item.AttemptID != value.AttemptID {
				return fmt.Errorf("action already has active attempt %s", item.AttemptID)
			}
		}
	}
	return s.appendJSONUnlocked(ctx, s.attemptPath, value)
}

func (s *JSONLStore) GetAttempt(ctx context.Context, attemptID modulecore.AttemptID) (domainaction.Attempt, error) {
	if err := attemptID.Validate(); err != nil {
		return domainaction.Attempt{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items, err := s.loadAttempts(ctx)
	if err != nil {
		return domainaction.Attempt{}, err
	}
	for _, item := range items {
		if item.AttemptID == attemptID {
			return item, nil
		}
	}
	return domainaction.Attempt{}, domainaction.ErrNotFound
}

func (s *JSONLStore) ListAttempts(ctx context.Context, filter domainaction.AttemptFilter) ([]domainaction.Attempt, error) {
	if filter.ActionID != "" {
		if err := filter.ActionID.Validate(); err != nil {
			return nil, fmt.Errorf("action_id is invalid: %w", err)
		}
	}
	if filter.Status != "" && !domainaction.ValidAttemptStatus(filter.Status) {
		return nil, fmt.Errorf("invalid attempt status: %s", filter.Status)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items, err := s.loadAttempts(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]domainaction.Attempt, 0, len(items))
	for _, item := range items {
		if filter.ActionID != "" && item.ActionID != filter.ActionID {
			continue
		}
		if filter.Status != "" && item.Status != filter.Status {
			continue
		}
		filtered = append(filtered, item)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].StartedAt.Equal(filtered[j].StartedAt) {
			return string(filtered[i].AttemptID) < string(filtered[j].AttemptID)
		}
		return filtered[i].StartedAt.Before(filtered[j].StartedAt)
	})
	if filter.Limit > 0 && len(filtered) > filter.Limit {
		filtered = filtered[:filter.Limit]
	}
	return filtered, nil
}

func (s *JSONLStore) loadActions(ctx context.Context) ([]domainaction.Action, error) {
	items, err := readJSONLLines[domainaction.Action](ctx, s.actionPath)
	if err != nil {
		return nil, err
	}
	latest := make(map[modulecore.ActionID]domainaction.Action, len(items))
	for index, item := range items {
		if err := item.Validate(); err != nil {
			return nil, fmt.Errorf("action record %d is invalid: %w", index, err)
		}
		latest[item.ActionID] = item
	}
	result := make([]domainaction.Action, 0, len(latest))
	for _, item := range latest {
		result = append(result, item)
	}
	return result, nil
}

func (s *JSONLStore) loadAttempts(ctx context.Context) ([]domainaction.Attempt, error) {
	items, err := readJSONLLines[domainaction.Attempt](ctx, s.attemptPath)
	if err != nil {
		return nil, err
	}
	latest := make(map[modulecore.AttemptID]domainaction.Attempt, len(items))
	activeByAction := make(map[modulecore.ActionID]modulecore.AttemptID)
	for index, item := range items {
		if err := item.Validate(); err != nil {
			return nil, fmt.Errorf("attempt record %d is invalid: %w", index, err)
		}
		if item.Status == domainaction.AttemptStatusRunning {
			if activeID, ok := activeByAction[item.ActionID]; ok && activeID != item.AttemptID {
				return nil, fmt.Errorf("action %s has multiple active attempts", item.ActionID)
			}
			activeByAction[item.ActionID] = item.AttemptID
		} else if activeID, ok := activeByAction[item.ActionID]; ok && activeID == item.AttemptID {
			delete(activeByAction, item.ActionID)
		}
		latest[item.AttemptID] = item
	}
	result := make([]domainaction.Attempt, 0, len(latest))
	for _, item := range latest {
		result = append(result, item)
	}
	return result, nil
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
	return json.NewEncoder(file).Encode(value)
}

func readJSONLLines[T any](ctx context.Context, path string) ([]T, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	var items []T
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := strings.TrimSpace(string(line))
			if trimmed != "" {
				var item T
				if unmarshalErr := json.Unmarshal([]byte(trimmed), &item); unmarshalErr != nil {
					return nil, unmarshalErr
				}
				items = append(items, item)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}
