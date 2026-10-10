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
	fenceMu           sync.RWMutex
	fenceByID         map[string]persistedTaskFence
	activeFences      map[modulecore.TaskID]string
	fenceRecovery     bool
	lifecycle         *taskStoreLifecycle
	executionFence    *taskExecutionFence
	batch             *jsonlbatch.Store
	txObserver        *txObserver
	idx               *taskIndex // optional in-memory index (OpenOptions.Index); nil keeps the original path
	root              string
	statePath         string
	runPath           string
	contextPath       string
	notificationsPath string
}

func NewJSONLStore(root string) (*JSONLStore, error) {
	return openJSONLStore(root, false, OpenOptions{Index: defaultIndexOption, Persist: defaultPersistOption})
}

// NewJSONLReader opens the canonical files without acquiring write ownership.
//
// If the writer keeps an index sidecar, the reader restores it (and replays the
// log written after it) instead of folding the whole log on every read; it never
// writes a sidecar or anything else. Such a reader answers from the log as it was
// when it was opened: commits that follow are not seen, which suits a command
// that opens, reads and exits. Without a sidecar the reader works as it always
// has (every read folds the log, and sees every commit).
func NewJSONLReader(root string) (*JSONLStore, error) {
	return openJSONLStore(root, true, OpenOptions{})
}

// NewIndexedJSONLReader is NewJSONLReader for callers that need the index
// itself (a Task's whole history, the verification of the index): the index is
// restored from the sidecar when it is usable and otherwise built in memory from
// the log, and failing to get one is an error.
func NewIndexedJSONLReader(root string) (*JSONLStore, error) {
	return openJSONLStore(root, true, OpenOptions{Index: true})
}

func openJSONLStore(root string, readOnly bool, opts OpenOptions) (*JSONLStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("task store root is required")
	}
	if opts.Persist && !opts.Index {
		return nil, errors.New("task store option Persist requires Index")
	}
	root = filepath.Clean(root)
	if !readOnly {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, err
		}
	}
	if err := rejectLegacyTaskStoreFiles(root); err != nil {
		return nil, err
	}
	store := &JSONLStore{
		root:              root,
		statePath:         filepath.Join(root, stateFilename),
		runPath:           filepath.Join(root, runFilename),
		contextPath:       filepath.Join(root, contextFilename),
		notificationsPath: filepath.Join(root, notificationsFilename),
		lifecycle:         newTaskStoreLifecycle(),
		executionFence:    newTaskExecutionFence(),
		fenceByID:         make(map[string]persistedTaskFence),
		activeFences:      make(map[modulecore.TaskID]string),
	}
	store.readOnly = readOnly
	if readOnly {
		filenames, err := taskBatchReaderFilenames(root)
		if err != nil {
			return nil, err
		}
		batch, err := jsonlbatch.OpenReader(root, filenames)
		if err != nil {
			return nil, err
		}
		store.batch = batch
		if err := store.openReaderIndex(opts.Index); err != nil {
			return nil, err
		}
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
	// Only the writer observes transaction timing; short-lived read-only handles
	// (CLI) stay silent.
	store.txObserver = newTxObserver(store.statePath, store.runPath)
	batch.SetTxObserver(store.txObserver.observe)
	if err := batch.Recover(context.Background()); err != nil {
		return nil, fmt.Errorf("recover task store batch: %w", err)
	}
	var receipts map[string]TaskOperationReceipt
	if opts.Index {
		idx, err := openTaskIndex(root, opts.Persist, opts.failpoint)
		if err != nil {
			return nil, fmt.Errorf("build task index: %w", err)
		}
		store.idx = idx
		if opts.Persist {
			policy := defaultCheckpointPolicy()
			if opts.checkpoint != nil {
				policy = *opts.checkpoint
			}
			idx.ck = newCheckpointer(idx, root, policy)
			idx.ck.fp = opts.failpoint
			if idx.source == sourceSidecar && idx.replayedLines == 0 {
				// The sidecar on disk already says everything.
				idx.ck.markWritten(idx.applied, totalLines(idx.lines))
			} else {
				// After a rebuild, or a restore that needed the log beyond the
				// sidecar, write a fresh one now: a crash before the first interval
				// must not send the next start through the same work. A failure is
				// logged and retried later; it does not stop the open.
				_ = idx.ck.checkpointNow("open")
			}
		}
		batch.SetPostCommitHook(idx.onCommit)
	} else {
		receipts, err = readTaskOperationReceipts(context.Background(), filepath.Join(root, taskOperationReceiptFilename))
		if err != nil {
			return nil, fmt.Errorf("load task operation receipts: %w", err)
		}
	}
	fenceSnapshot, err := readTaskExecutionFences(context.Background(), filepath.Join(root, taskExecutionFenceFilename))
	if err != nil {
		return nil, fmt.Errorf("load task execution fences: %w", err)
	}
	store.installTaskFenceSnapshot(fenceSnapshot)
	generation, err := advanceTaskWriterGeneration(lock)
	if err != nil {
		return nil, fmt.Errorf("advance task store writer generation: %w", err)
	}
	store.writerGeneration = generation
	// A missing/truncated counter must not reuse any generation already bound to a Run.
	if store.idx != nil {
		if err := checkIndexGenerations(store.idx, generation); err != nil {
			return nil, err
		}
	} else {
		runs, err := store.loadRuns(context.Background())
		if err != nil {
			return nil, err
		}
		for _, run := range runs {
			if run.WriterGeneration >= generation {
				return nil, fmt.Errorf("writer generation does not advance persisted Run ownership")
			}
		}
		for _, receipt := range receipts {
			if receipt.WriterGeneration >= generation {
				return nil, fmt.Errorf("writer generation does not advance persisted task operation receipt ownership")
			}
		}
	}
	for _, fence := range fenceSnapshot.byID {
		if fence.AcquireGeneration >= generation || fence.ReleaseGeneration >= generation {
			return nil, fmt.Errorf("writer generation does not advance persisted task execution fence ownership")
		}
	}
	if store.idx != nil && store.idx.ck != nil {
		store.idx.ck.start()
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
	if s != nil && s.idx != nil {
		return s.indexGetRun(ctx, runID)
	}
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
	if s != nil && s.idx != nil {
		return s.indexGetTask(ctx, taskID)
	}
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
	if s != nil && s.idx != nil {
		return s.indexGetContext(ctx, taskID)
	}
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
	for index, item := range items {
		if err := item.Validate(); err != nil {
			return nil, fmt.Errorf("task record %d is invalid: %w", index, err)
		}
		if previous, ok := latest[item.TaskID]; ok {
			if previous.ExpectedCriteriaRevision != item.ExpectedCriteriaRevision {
				return nil, fmt.Errorf("task record %d changes its first-save criteria revision: %w", index, ErrExpectedCriteriaRevisionImmutable)
			}
			if !domaintask.NativeOPSResumeClaimsExtend(previous.NativeResumeClaims, item.NativeResumeClaims) {
				return nil, fmt.Errorf("task record %d rewrites native OPS Resume claims: %w", index, ErrNativeOPSResumeClaimImmutable)
			}
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
func (s *JSONLStore) Close() error { return s.closeStore(true) }

// closeStore is Close; flushCheckpoint false skips the final sidecar write, which
// leaves the store as a crash after the last commit would (tests only).
func (s *JSONLStore) closeStore(flushCheckpoint bool) error {
	if s == nil {
		return nil
	}
	if s.lifecycle != nil && !s.lifecycle.beginClose() {
		return nil
	}
	if s.lifecycle != nil {
		s.lifecycle.wait()
	}
	// No transaction is in flight and none can start. Stop the checkpoint loop
	// first and only then write the final checkpoint, so two writers of the
	// sidecar never overlap; it runs before the writer lock is released, so no
	// other process can be replacing the sidecar at the same time.
	if s.idx != nil && s.idx.ck != nil {
		if flushCheckpoint {
			s.idx.ck.stopAndFlush()
		} else {
			s.idx.ck.stop()
		}
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
	err = errors.Join(err, s.idx.close())
	s.mu.Unlock()
	if s.lifecycle != nil {
		s.lifecycle.finishClose()
	}
	return err
}
