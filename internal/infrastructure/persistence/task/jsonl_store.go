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
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	stateFilename         = "task_state.jsonl"
	runFilename           = "task_run.jsonl"
	contextFilename       = "task_context.jsonl"
	notificationsFilename = "task_notifications.jsonl"
)

type JSONLStore struct {
	writerLock        *os.File
	writerGeneration  uint64
	closed            bool
	readOnly          bool
	mu                sync.RWMutex
	lifecycle         *taskStoreLifecycle
	executionFence    *taskExecutionFence
	batch             *jsonlbatch.Store
	root              string
	statePath         string
	runPath           string
	contextPath       string
	notificationsPath string
}

func NewJSONLStore(root string) (*JSONLStore, error) { return openJSONLStore(root, false) }

// NewJSONLReader opens the canonical files without acquiring write ownership.
func NewJSONLReader(root string) (*JSONLStore, error) { return openJSONLStore(root, true) }

func openJSONLStore(root string, readOnly bool) (*JSONLStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("task store root is required")
	}
	root = filepath.Clean(root)
	if !readOnly {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, err
		}
	}
	for _, filename := range []string{"job_state.jsonl", "job_context.jsonl", "job_notifications.jsonl"} {
		if _, err := os.Lstat(filepath.Join(root, filename)); err == nil {
			return nil, fmt.Errorf("legacy task store file %s is not supported", filename)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	store := &JSONLStore{
		root:              root,
		statePath:         filepath.Join(root, stateFilename),
		runPath:           filepath.Join(root, runFilename),
		contextPath:       filepath.Join(root, contextFilename),
		notificationsPath: filepath.Join(root, notificationsFilename),
		lifecycle:         newTaskStoreLifecycle(),
		executionFence:    newTaskExecutionFence(),
	}
	store.readOnly = readOnly
	if readOnly {
		batch, err := jsonlbatch.OpenReader(root, taskBatchFilenames())
		if err != nil {
			return nil, err
		}
		store.batch = batch
		return store, nil
	}
	lock, err := acquireTaskWriter(root)
	if err != nil {
		return nil, err
	}
	store.writerLock = lock
	success := false
	defer func() {
		if !success {
			_ = store.Close()
		}
	}()
	batch, err := jsonlbatch.New(root, taskBatchFilenames())
	if err != nil {
		return nil, err
	}
	store.batch = batch
	if err := batch.Recover(context.Background()); err != nil {
		return nil, fmt.Errorf("recover task store batch: %w", err)
	}
	generation, err := advanceTaskWriterGeneration(lock)
	if err != nil {
		return nil, fmt.Errorf("advance task store writer generation: %w", err)
	}
	store.writerGeneration = generation
	// A missing/truncated counter must not reuse any generation already bound to a Run.
	runs, err := store.loadRuns(context.Background())
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		if run.WriterGeneration >= generation {
			return nil, fmt.Errorf("writer generation does not advance persisted Run ownership")
		}
	}
	success = true
	return store, nil
}

// WriterGeneration returns the monotonically increasing generation held by a
// live writable store. Readers and closed stores have no writer generation.
func (s *JSONLStore) WriterGeneration() (uint64, error) {
	if s == nil {
		return 0, fmt.Errorf("task store is nil")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return 0, os.ErrClosed
	}
	if s.readOnly || s.writerLock == nil || s.writerGeneration == 0 {
		return 0, fmt.Errorf("task store writer generation unavailable")
	}
	return s.writerGeneration, nil
}

func (s *JSONLStore) SaveTask(ctx context.Context, value domaintask.Task) error {
	return s.TaskTransaction(ctx, value.TaskID, func(store domaintask.Store) error {
		return store.SaveTask(ctx, value)
	})
}

func (s *JSONLStore) SaveRun(ctx context.Context, value domaintask.Run) error {
	return s.TaskTransaction(ctx, value.TaskID, func(store domaintask.Store) error {
		return store.SaveRun(ctx, value)
	})
}

func (s *JSONLStore) GetRun(ctx context.Context, runID modulecore.RunID) (domaintask.Run, error) {
	var result domaintask.Run
	err := s.ReadTransaction(ctx, func(store domaintask.Store) error {
		var err error
		result, err = store.GetRun(ctx, runID)
		return err
	})
	return result, err
}

func (s *JSONLStore) ListRuns(ctx context.Context, filter domaintask.RunFilter) ([]domaintask.Run, error) {
	var result []domaintask.Run
	err := s.ReadTransaction(ctx, func(store domaintask.Store) error {
		var err error
		result, err = store.ListRuns(ctx, filter)
		return err
	})
	return result, err
}

func (s *JSONLStore) GetTask(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	var result domaintask.Task
	err := s.ReadTransaction(ctx, func(store domaintask.Store) error {
		var err error
		result, err = store.GetTask(ctx, taskID)
		return err
	})
	return result, err
}

func (s *JSONLStore) ListTasks(ctx context.Context, filter domaintask.Filter) ([]domaintask.Task, error) {
	var result []domaintask.Task
	err := s.ReadTransaction(ctx, func(store domaintask.Store) error {
		var err error
		result, err = store.ListTasks(ctx, filter)
		return err
	})
	return result, err
}

func (s *JSONLStore) SaveContext(ctx context.Context, value domaintask.SharedRoleContext) error {
	return s.TaskTransaction(ctx, value.TaskID, func(store domaintask.Store) error {
		return store.SaveContext(ctx, value)
	})
}

func (s *JSONLStore) GetContext(ctx context.Context, taskID modulecore.TaskID) (domaintask.SharedRoleContext, error) {
	var result domaintask.SharedRoleContext
	err := s.ReadTransaction(ctx, func(store domaintask.Store) error {
		var err error
		result, err = store.GetContext(ctx, taskID)
		return err
	})
	return result, err
}

func (s *JSONLStore) SaveNotification(ctx context.Context, value domaintask.Notification) error {
	return s.TaskTransaction(ctx, value.TaskID, func(store domaintask.Store) error {
		return store.SaveNotification(ctx, value)
	})
}

func (s *JSONLStore) ListNotifications(ctx context.Context, limit int, interruptOnly bool) ([]domaintask.Notification, error) {
	var result []domaintask.Notification
	err := s.ReadTransaction(ctx, func(store domaintask.Store) error {
		var err error
		result, err = store.ListNotifications(ctx, limit, interruptOnly)
		return err
	})
	return result, err
}

func (s *JSONLStore) loadTasks(ctx context.Context) ([]domaintask.Task, error) {
	items, err := readJSONLLines[domaintask.Task](ctx, s.statePath)
	if err != nil {
		return nil, err
	}
	return foldTaskRecords(items)
}

func foldTaskRecords(items []domaintask.Task) ([]domaintask.Task, error) {
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
	return foldRunRecords(items, 0)
}

type runRecordFold struct {
	latest       map[modulecore.RunID]domaintask.Run
	activeByTask map[modulecore.TaskID]modulecore.RunID
}

func newRunRecordFold(capacity int) *runRecordFold {
	return &runRecordFold{
		latest:       make(map[modulecore.RunID]domaintask.Run, capacity),
		activeByTask: make(map[modulecore.TaskID]modulecore.RunID),
	}
}

func newRunRecordFoldFromLatest(items []domaintask.Run) *runRecordFold {
	fold := newRunRecordFold(len(items))
	for _, item := range items {
		fold.latest[item.RunID] = item
		if item.Status == domaintask.RunStatusRunning {
			fold.activeByTask[item.TaskID] = item.RunID
		}
	}
	return fold
}

func (f *runRecordFold) apply(recordIndex int, item domaintask.Run) error {
	if err := item.Validate(); err != nil {
		return fmt.Errorf("run record %d is invalid: %w", recordIndex, err)
	}
	if previous, ok := f.latest[item.RunID]; ok {
		if err := validateRunUpdate(previous, item); err != nil {
			return fmt.Errorf("run record %d update is invalid: %w", recordIndex, err)
		}
	}
	if item.Status == domaintask.RunStatusRunning {
		if activeID, ok := f.activeByTask[item.TaskID]; ok && activeID != item.RunID {
			return fmt.Errorf("task %s has multiple active runs", item.TaskID)
		}
		f.activeByTask[item.TaskID] = item.RunID
	} else if activeID, ok := f.activeByTask[item.TaskID]; ok && activeID == item.RunID {
		delete(f.activeByTask, item.TaskID)
	}
	f.latest[item.RunID] = item
	return nil
}

func (f *runRecordFold) records() []domaintask.Run {
	result := make([]domaintask.Run, 0, len(f.latest))
	for _, item := range f.latest {
		result = append(result, item)
	}
	return result
}

func foldRunRecords(items []domaintask.Run, recordOffset int) ([]domaintask.Run, error) {
	fold := newRunRecordFold(len(items))
	for index, item := range items {
		if err := fold.apply(recordOffset+index, item); err != nil {
			return nil, err
		}
	}
	return fold.records(), nil
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
	if existing.WriterGeneration != next.WriterGeneration || existing.TaskID != next.TaskID || existing.StartReason != next.StartReason || existing.Assignee != next.Assignee || !existing.StartedAt.Equal(next.StartedAt) || existing.StartCheckpointSHA256 != next.StartCheckpointSHA256 {
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

// Close relinquishes this writer's OS lease. The persistent lock file is never removed.
func (s *JSONLStore) Close() error {
	if s == nil {
		return nil
	}
	if s.lifecycle != nil && !s.lifecycle.beginClose() {
		return nil
	}
	if s.lifecycle != nil {
		s.lifecycle.wait()
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		if s.lifecycle != nil {
			s.lifecycle.finishClose()
		}
		return nil
	}
	s.closed = true
	var err error
	if s.writerLock != nil {
		err = releaseTaskWriter(s.writerLock)
		s.writerLock = nil
	}
	s.mu.Unlock()
	if s.lifecycle != nil {
		s.lifecycle.finishClose()
	}
	return err
}
