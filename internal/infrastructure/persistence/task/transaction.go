package task

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	errTaskTransactionExpired = errors.New("task transaction is no longer active")
	errReadOnlyTransaction    = errors.New("task transaction is read-only")
)

func taskBatchFilenames() []string {
	return []string{stateFilename, runFilename, contextFilename, notificationsFilename}
}

// Transaction executes one Manager mutation against a pending append view.
// The batch helper owns the OS lock and commits every pending file only after
// the callback returns nil.
func (s *JSONLStore) Transaction(ctx context.Context, fn func(domaintask.Store) error) error {
	if err := validateTransactionArgs(s, ctx, fn); err != nil {
		return err
	}
	releaseAdmission, err := s.lifecycle.admit(ctx)
	if err != nil {
		return err
	}
	defer releaseAdmission()
	return s.transactionWithScope(ctx, "", nil, fn)
}

// TaskTransaction serializes writes for one Task before entering the global
// append batch. The transaction callback is intentionally bounded to the same
// Task so an active external effect cannot be bypassed through this view.
func (s *JSONLStore) TaskTransaction(ctx context.Context, taskID modulecore.TaskID, fn func(domaintask.Store) error) error {
	if err := validateTransactionArgs(s, ctx, fn); err != nil {
		return err
	}
	if err := taskID.Validate(); err != nil {
		return err
	}
	if s.readOnly {
		return fmt.Errorf("task store is read-only")
	}
	releaseAdmission, err := s.lifecycle.admit(ctx)
	if err != nil {
		return err
	}
	defer releaseAdmission()
	lease, err := s.executionFence.acquire(ctx, taskID)
	if err != nil {
		return err
	}
	defer lease.release()
	return s.transactionWithScope(ctx, taskID, lease, fn)
}

// WithTaskExecutionFence admits a synchronous external leaf while holding an
// exclusive Task gate. Only the short validation transaction runs under the
// global JSONL lock; the callback runs after that lock is released.
func (s *JSONLStore) WithTaskExecutionFence(ctx context.Context, taskID modulecore.TaskID, fn func() error) error {
	if s == nil {
		return errors.New("task store is nil")
	}
	if fn == nil {
		return errors.New("task execution fence callback is nil")
	}
	if ctx == nil {
		return errors.New("task execution fence context is nil")
	}
	if err := taskID.Validate(); err != nil {
		return err
	}
	if s.readOnly {
		return fmt.Errorf("task store is read-only")
	}
	releaseAdmission, err := s.lifecycle.admit(ctx)
	if err != nil {
		return err
	}
	defer releaseAdmission()
	lease, err := s.executionFence.acquire(ctx, taskID)
	if err != nil {
		return err
	}
	defer lease.release()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

func validateTransactionArgs(s *JSONLStore, ctx context.Context, fn func(domaintask.Store) error) error {
	if s == nil {
		return errors.New("task store is nil")
	}
	if fn == nil {
		return errors.New("task transaction callback is nil")
	}
	if ctx == nil {
		return errors.New("task transaction context is nil")
	}
	return nil
}

func (s *JSONLStore) transactionWithScope(ctx context.Context, scopeTaskID modulecore.TaskID, lease *taskExecutionLease, fn func(domaintask.Store) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return os.ErrClosed
	}
	if s.readOnly || s.writerLock == nil {
		return fmt.Errorf("task store is read-only")
	}
	if s.batch == nil {
		return errors.New("task store batch is unavailable")
	}
	return s.batch.Write(ctx, func() (map[string][]byte, error) {
		tx := newTaskTransaction(s, false, scopeTaskID, lease)
		defer tx.end()
		err := fn(tx)
		snapshot := tx.freezeAndSnapshot()
		if err != nil {
			return nil, err
		}
		if err := validateTaskTransaction(ctx, s, snapshot); err != nil {
			return nil, err
		}
		return snapshot.payloads, nil
	})
}

// ReadTransaction executes one read decision against a committed snapshot.
// The batch helper rejects pending or torn WAL state without repairing it.
func (s *JSONLStore) ReadTransaction(ctx context.Context, fn func(domaintask.Store) error) error {
	if s == nil {
		return errors.New("task store is nil")
	}
	if fn == nil {
		return errors.New("task read transaction callback is nil")
	}
	if ctx == nil {
		return errors.New("task read transaction context is nil")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return os.ErrClosed
	}
	if s.batch == nil {
		return errors.New("task store batch is unavailable")
	}
	return s.batch.Read(ctx, func() error {
		tx := newTaskTransaction(s, true, "", nil)
		defer tx.end()
		err := fn(tx)
		return err
	})
}

type transactionStore struct {
	parent      *JSONLStore
	readOnly    bool
	scopeTaskID modulecore.TaskID
	lease       *taskExecutionLease

	mu    sync.Mutex
	local bool
	state *transactionState
}

type transactionState struct {
	mu             sync.Mutex
	active         bool
	pending        map[string][]byte
	touched        map[modulecore.TaskID]struct{}
	pendingVersion uint64
	baseLoaded     bool
	base           *transactionTaskRunBase
	viewVersion    uint64
	view           *transactionTaskRunView
}

func newTaskTransaction(parent *JSONLStore, readOnly bool, scopeTaskID modulecore.TaskID, lease *taskExecutionLease) *transactionStore {
	return &transactionStore{
		parent:      parent,
		readOnly:    readOnly,
		scopeTaskID: scopeTaskID,
		lease:       lease,
		local:       true,
		state:       &transactionState{active: true, pending: make(map[string][]byte), touched: make(map[modulecore.TaskID]struct{})},
	}
}

type transactionSnapshot struct {
	payloads map[string][]byte
	touched  map[modulecore.TaskID]struct{}
	owner    *taskExecutionLease
	base     *transactionTaskRunBase
}

func validateTaskTransaction(ctx context.Context, parent *JSONLStore, snapshot transactionSnapshot) error {
	if parent == nil {
		return errors.New("task store is unavailable")
	}
	if parent.executionFence != nil {
		if activeTaskID := parent.executionFence.hasActive(snapshot.touched, snapshot.owner); activeTaskID != "" {
			return fmt.Errorf("task transaction conflicts with active execution fence for task %s", activeTaskID)
		}
	}
	view := &transactionStore{
		parent:      parent,
		readOnly:    true,
		scopeTaskID: "",
		local:       true,
		state: &transactionState{
			active:     true,
			pending:    snapshot.payloads,
			touched:    snapshot.touched,
			baseLoaded: snapshot.base != nil,
			base:       snapshot.base,
		},
	}
	defer view.end()
	tasks, err := view.loadTasks(ctx)
	if err != nil {
		return err
	}
	runs, err := view.loadRuns(ctx)
	if err != nil {
		return err
	}
	return validateRunOwners(runs, tasks)
}

func (s *transactionStore) end() {
	s.mu.Lock()
	if !s.local {
		s.mu.Unlock()
		return
	}
	s.local = false
	if !s.readOnly && s.state != nil {
		s.state.mu.Lock()
		s.state.active = false
		s.state.mu.Unlock()
	}
	s.mu.Unlock()
}

func (s *transactionStore) check(ctx context.Context) error {
	s.mu.Lock()
	local := s.local
	s.mu.Unlock()
	if !local {
		return errTaskTransactionExpired
	}
	if s.state == nil {
		return errTaskTransactionExpired
	}
	s.state.mu.Lock()
	active := s.state.active
	s.state.mu.Unlock()
	if !active {
		return errTaskTransactionExpired
	}
	if ctx == nil {
		return errors.New("task transaction context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (s *transactionStore) checkWrite(ctx context.Context) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	if s.readOnly {
		return errReadOnlyTransaction
	}
	return nil
}

func (s *transactionStore) WriterGeneration() (uint64, error) {
	if err := s.check(context.Background()); err != nil {
		return 0, err
	}
	if s.parent == nil {
		return 0, errors.New("task transaction parent is unavailable")
	}
	if s.parent.readOnly || s.parent.writerLock == nil || s.parent.writerGeneration == 0 {
		return 0, fmt.Errorf("task store writer generation unavailable")
	}
	return s.parent.writerGeneration, nil
}

func (s *transactionStore) Transaction(ctx context.Context, fn func(domaintask.Store) error) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	if s.readOnly {
		return errReadOnlyTransaction
	}
	if fn == nil {
		return errors.New("task transaction callback is nil")
	}
	return fn(s)
}

func (s *transactionStore) TaskTransaction(ctx context.Context, taskID modulecore.TaskID, fn func(domaintask.Store) error) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	if s.readOnly {
		return errReadOnlyTransaction
	}
	if fn == nil {
		return errors.New("task transaction callback is nil")
	}
	if err := taskID.Validate(); err != nil {
		return err
	}
	if s.scopeTaskID == "" || s.scopeTaskID != taskID {
		return errTaskExecutionFenceScope
	}
	return fn(s)
}

func (s *transactionStore) WithTaskExecutionFence(context.Context, modulecore.TaskID, func() error) error {
	return errors.New("task execution fence cannot be entered from a transaction")
}

func (s *transactionStore) ReadTransaction(ctx context.Context, fn func(domaintask.Store) error) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	if fn == nil {
		return errors.New("task read transaction callback is nil")
	}
	if s.readOnly {
		return fn(s)
	}
	view := &transactionStore{parent: s.parent, readOnly: true, scopeTaskID: s.scopeTaskID, lease: s.lease, local: true, state: s.state}
	defer view.end()
	return fn(view)
}

func (s *transactionStore) appendValue(ctx context.Context, filename string, value any) error {
	if err := s.checkWrite(ctx); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.local || s.state == nil {
		return errTaskTransactionExpired
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if !s.state.active {
		return errTaskTransactionExpired
	}
	s.state.pending[filename] = append(s.state.pending[filename], encoded...)
	if filename == stateFilename || filename == runFilename {
		s.state.pendingVersion++
		s.state.view = nil
	}
	return nil
}

func (s *transactionStore) freezeAndSnapshot() transactionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := transactionSnapshot{owner: s.lease}
	if s.state == nil {
		return snapshot
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	s.state.active = false
	snapshot.payloads = make(map[string][]byte, len(s.state.pending))
	for name, payload := range s.state.pending {
		snapshot.payloads[name] = append([]byte(nil), payload...)
	}
	snapshot.touched = make(map[modulecore.TaskID]struct{}, len(s.state.touched))
	for taskID := range s.state.touched {
		snapshot.touched[taskID] = struct{}{}
	}
	snapshot.base = s.state.base
	return snapshot
}

func (s *transactionStore) touchTask(ctx context.Context, taskID modulecore.TaskID) error {
	if err := s.checkWrite(ctx); err != nil {
		return err
	}
	if err := taskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	if s.scopeTaskID != "" && s.scopeTaskID != taskID {
		return errTaskExecutionFenceScope
	}
	if s.state == nil {
		return errTaskTransactionExpired
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if !s.state.active {
		return errTaskTransactionExpired
	}
	s.state.touched[taskID] = struct{}{}
	return nil
}

func (s *transactionStore) pendingBytes(filename string) []byte {
	if s.state == nil {
		return nil
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	return append([]byte(nil), s.state.pending[filename]...)
}

func (s *transactionStore) SaveTask(ctx context.Context, value domaintask.Task) error {
	if err := s.checkWrite(ctx); err != nil {
		return err
	}
	if err := value.Validate(); err != nil {
		return err
	}
	if err := s.touchTask(ctx, value.TaskID); err != nil {
		return err
	}
	return s.appendValue(ctx, stateFilename, value)
}

func (s *transactionStore) SaveRun(ctx context.Context, value domaintask.Run) error {
	if err := s.checkWrite(ctx); err != nil {
		return err
	}
	if err := value.Validate(); err != nil {
		return err
	}
	if err := s.touchTask(ctx, value.TaskID); err != nil {
		return err
	}
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
	existingRun := false
	for _, item := range runs {
		if item.RunID == value.RunID {
			existingRun = true
			if err := validateRunUpdate(item, value); err != nil {
				return err
			}
			break
		}
	}
	generation, err := s.WriterGeneration()
	if err != nil {
		return err
	}
	if !existingRun && (value.WriterGeneration == 0 || value.WriterGeneration != generation) {
		return fmt.Errorf("new run requires current writer generation")
	}
	if value.Status == domaintask.RunStatusRunning {
		for _, item := range runs {
			if item.TaskID == value.TaskID && item.Status == domaintask.RunStatusRunning && item.RunID != value.RunID {
				return fmt.Errorf("task already has active run %s", item.RunID)
			}
		}
	}
	return s.appendValue(ctx, runFilename, value)
}

func (s *transactionStore) GetRun(ctx context.Context, runID modulecore.RunID) (domaintask.Run, error) {
	if err := s.check(ctx); err != nil {
		return domaintask.Run{}, err
	}
	if err := runID.Validate(); err != nil {
		return domaintask.Run{}, err
	}
	items, err := s.loadRuns(ctx)
	if err != nil {
		return domaintask.Run{}, err
	}
	tasks, err := s.loadTasks(ctx)
	if err != nil {
		return domaintask.Run{}, err
	}
	if err := validateRunOwners(items, tasks); err != nil {
		return domaintask.Run{}, err
	}
	for _, item := range items {
		if item.RunID == runID {
			return item, nil
		}
	}
	return domaintask.Run{}, domaintask.ErrNotFound
}

func (s *transactionStore) ListRuns(ctx context.Context, filter domaintask.RunFilter) ([]domaintask.Run, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	if filter.TaskID != "" {
		if err := filter.TaskID.Validate(); err != nil {
			return nil, fmt.Errorf("task_id is invalid: %w", err)
		}
	}
	if filter.Status != "" && !domaintask.ValidRunStatus(filter.Status) {
		return nil, fmt.Errorf("invalid run status: %s", filter.Status)
	}
	items, err := s.loadRuns(ctx)
	if err != nil {
		return nil, err
	}
	tasks, err := s.loadTasks(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateRunOwners(items, tasks); err != nil {
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

func (s *transactionStore) GetTask(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	if err := s.check(ctx); err != nil {
		return domaintask.Task{}, err
	}
	if err := taskID.Validate(); err != nil {
		return domaintask.Task{}, err
	}
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

func (s *transactionStore) ListTasks(ctx context.Context, filter domaintask.Filter) ([]domaintask.Task, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
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

func (s *transactionStore) SaveContext(ctx context.Context, value domaintask.SharedRoleContext) error {
	if err := s.checkWrite(ctx); err != nil {
		return err
	}
	if err := value.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := s.touchTask(ctx, value.TaskID); err != nil {
		return err
	}
	if _, err := s.GetTask(ctx, value.TaskID); err != nil {
		return err
	}
	return s.appendValue(ctx, contextFilename, value)
}

func (s *transactionStore) GetContext(ctx context.Context, taskID modulecore.TaskID) (domaintask.SharedRoleContext, error) {
	if err := s.check(ctx); err != nil {
		return domaintask.SharedRoleContext{}, err
	}
	if err := taskID.Validate(); err != nil {
		return domaintask.SharedRoleContext{}, err
	}
	items, err := readJSONLLinesWithPending[domaintask.SharedRoleContext](ctx, s.parent.contextPath, s.pendingBytes(contextFilename))
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

func (s *transactionStore) SaveNotification(ctx context.Context, value domaintask.Notification) error {
	if err := s.checkWrite(ctx); err != nil {
		return err
	}
	if err := value.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := s.touchTask(ctx, value.TaskID); err != nil {
		return err
	}
	if strings.TrimSpace(value.Type) == "" {
		return fmt.Errorf("notification type is required")
	}
	return s.appendValue(ctx, notificationsFilename, value)
}

func (s *transactionStore) ListNotifications(ctx context.Context, limit int, interruptOnly bool) ([]domaintask.Notification, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	items, err := readJSONLLinesWithPending[domaintask.Notification](ctx, s.parent.notificationsPath, s.pendingBytes(notificationsFilename))
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

func (s *transactionStore) loadTasks(ctx context.Context) ([]domaintask.Task, error) {
	view, err := s.taskRunView(ctx)
	if err != nil {
		return nil, err
	}
	if view.tasksErr != nil {
		return nil, view.tasksErr
	}
	result := cloneTransactionTasks(view.tasks)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *transactionStore) loadRuns(ctx context.Context) ([]domaintask.Run, error) {
	view, err := s.taskRunView(ctx)
	if err != nil {
		return nil, err
	}
	if view.runsErr != nil {
		return nil, view.runsErr
	}
	result := cloneTransactionRuns(view.runs)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func readJSONLLinesWithPending[T any](ctx context.Context, path string, pending []byte) ([]T, error) {
	items, err := readJSONLLines[T](ctx, path)
	if err != nil || len(pending) == 0 {
		return items, err
	}
	additional, err := readJSONLBytes[T](ctx, pending)
	if err != nil {
		return nil, err
	}
	return append(items, additional...), nil
}

func readJSONLBytes[T any](ctx context.Context, data []byte) ([]T, error) {
	if ctx == nil {
		return nil, errors.New("task transaction context is nil")
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
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
