package task

import (
	"context"
	"errors"
	"math"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	// ErrTaskAdmissionRequiresIndex keeps explicit CHAT admission from falling
	// back to a disk-locked transaction on a legacy non-indexed store.
	ErrTaskAdmissionRequiresIndex = errors.New("explicit Task persistence admission requires an indexed Task store")
	ErrTaskAdmissionBusy          = errors.New("explicit Task persistence admission is busy")
	errTaskAdmissionIndexDirty    = errors.New("explicit Task persistence admission requires a clean in-memory Task index")
)

type taskAdmissionWrite struct {
	payloads  map[string][]byte
	admission *persistencequeue.Admission
	applyErr  error
}

type taskAdmissionWriteContextKey struct{}

// taskPersistenceQueueState tracks volatile commands accepted by the shared
// persistence queue. The JSONL files remain the only durable source of truth.
type taskPersistenceQueueState struct {
	mu                sync.Mutex
	changed           chan struct{}
	synchronousWriter bool
	admittedTxs       int
	pending           []*taskAdmissionWrite
}

func newTaskPersistenceQueueState() *taskPersistenceQueueState {
	return &taskPersistenceQueueState{changed: make(chan struct{})}
}

func (s *taskPersistenceQueueState) signalLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *taskPersistenceQueueState) beginAdmitted(ctx context.Context) (func(), error) {
	if s == nil {
		return func() {}, nil
	}
	if ctx == nil {
		return nil, persistencequeue.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !s.mu.TryLock() {
		return nil, ErrTaskAdmissionBusy
	}
	if s.synchronousWriter || s.admittedTxs != 0 {
		s.mu.Unlock()
		return nil, ErrTaskAdmissionBusy
	}
	s.admittedTxs++
	// Keep the state lock through the indexed snapshot and callback. This makes
	// the pending overlay and committed index one view, while ensuring cleanup
	// never waits behind a post-commit hook that is holding the JSONL OS lock.
	return func() {
		if s.admittedTxs > 0 {
			s.admittedTxs--
		}
		s.signalLocked()
		s.mu.Unlock()
	}, nil
}

func (s *taskPersistenceQueueState) beginSynchronous(ctx context.Context) (func(), error) {
	if s == nil {
		return func() {}, nil
	}
	if ctx == nil {
		return nil, persistencequeue.ErrNilContext
	}
	for {
		s.mu.Lock()
		if !s.synchronousWriter && s.admittedTxs == 0 && len(s.pending) == 0 {
			s.synchronousWriter = true
			s.mu.Unlock()
			return func() {
				s.mu.Lock()
				s.synchronousWriter = false
				s.signalLocked()
				s.mu.Unlock()
			}, nil
		}
		changed := s.changed
		pending := append([]*taskAdmissionWrite(nil), s.pending...)
		s.mu.Unlock()
		if len(pending) > 0 {
			seen := make(map[*persistencequeue.Admission]struct{}, len(pending))
			for _, write := range pending {
				if write == nil || write.admission == nil {
					return nil, errors.New("accepted Task persistence command has no admission")
				}
				if _, ok := seen[write.admission]; ok {
					continue
				}
				seen[write.admission] = struct{}{}
				if err := write.admission.Barrier(ctx); err != nil {
					return nil, err
				}
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (s *taskPersistenceQueueState) acceptPendingLocked(write *taskAdmissionWrite, enqueue func() error) error {
	if s == nil || write == nil || enqueue == nil {
		return errors.New("Task persistence command is unavailable")
	}
	// The caller holds s.mu across its indexed snapshot. Publish the RAM overlay
	// in the same critical section that accepts the command: the worker can
	// start immediately after Enqueue returns.
	if err := enqueue(); err != nil {
		return err
	}
	s.pending = append(s.pending, write)
	s.signalLocked()
	return nil
}

func (s *taskPersistenceQueueState) failPending(write *taskAdmissionWrite, err error) {
	if s == nil || write == nil || err == nil {
		return
	}
	s.mu.Lock()
	for _, pending := range s.pending {
		if pending == write {
			pending.applyErr = err
			s.signalLocked()
			break
		}
	}
	s.mu.Unlock()
}

// pendingPayloadsLocked requires s.mu. It also probes queue health without
// waiting, so a failed queue cannot lend later reads a stale RAM-only view.
func (s *taskPersistenceQueueState) pendingPayloadsLocked() (map[string][]byte, error) {
	if s == nil {
		return nil, nil
	}
	result := make(map[string][]byte)
	admissions := make(map[*persistencequeue.Admission]struct{})
	for _, write := range s.pending {
		if write == nil || write.admission == nil {
			return nil, errors.New("accepted Task persistence command has no admission")
		}
		if write.applyErr != nil {
			return nil, write.applyErr
		}
		admissions[write.admission] = struct{}{}
		appendTaskPayloads(result, write.payloads)
	}
	// Probe queue health without waiting for in-flight work. A cancelled wait
	// means the accepted command is still pending; a queue failure means its
	// RAM-only view can no longer be trusted for later decisions.
	for admission := range admissions {
		probeCtx, cancel := context.WithCancel(context.Background())
		cancel()
		err := admission.Barrier(probeCtx)
		if err != nil && (!errors.Is(err, context.Canceled) || errors.Is(err, persistencequeue.ErrFailed)) {
			return nil, err
		}
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

// lockAdmissionIndexView atomically pairs the committed index with its pending
// RAM overlay. The caller holds the queue-state mutex from beginAdmitted; this
// function takes the index read lock without waiting behind a writer.
func (s *taskPersistenceQueueState) lockAdmissionIndexView(ix *taskIndex) (map[string][]byte, func(), error) {
	if s == nil || ix == nil {
		return nil, nil, ErrTaskAdmissionRequiresIndex
	}
	payloads, err := s.pendingPayloadsLocked()
	if err != nil {
		return nil, nil, err
	}
	if !ix.mu.TryRLock() {
		return nil, nil, ErrTaskAdmissionBusy
	}
	if ix.dirty.Load() {
		ix.mu.RUnlock()
		return nil, nil, errTaskAdmissionIndexDirty
	}
	return payloads, func() {
		ix.mu.RUnlock()
	}, nil
}

func (s *taskPersistenceQueueState) drain(ctx context.Context) error {
	if s == nil {
		return nil
	}
	for {
		s.mu.Lock()
		pending := append([]*taskAdmissionWrite(nil), s.pending...)
		s.mu.Unlock()
		if len(pending) == 0 {
			return nil
		}
		seen := make(map[*persistencequeue.Admission]struct{}, len(pending))
		for _, write := range pending {
			if write == nil || write.admission == nil {
				return errors.New("accepted Task persistence command has no admission")
			}
			if _, ok := seen[write.admission]; ok {
				continue
			}
			seen[write.admission] = struct{}{}
			if err := write.admission.Barrier(ctx); err != nil {
				return err
			}
		}
	}
}

func appendTaskPayloads(target, source map[string][]byte) {
	for name, payload := range source {
		target[name] = append(target[name], payload...)
	}
}

func cloneTaskPayloads(payloads map[string][]byte) map[string][]byte {
	if len(payloads) == 0 {
		return nil
	}
	cloned := make(map[string][]byte, len(payloads))
	for name, payload := range payloads {
		cloned[name] = append([]byte(nil), payload...)
	}
	return cloned
}

func taskPayloadSize(payloads map[string][]byte) (int64, error) {
	var size int64
	for _, payload := range payloads {
		if int64(len(payload)) > math.MaxInt64-size {
			return 0, errors.New("Task persistence command size overflows int64")
		}
		size += int64(len(payload))
	}
	if size < 1 {
		return 0, errors.New("Task persistence command is empty")
	}
	return size, nil
}

func (s *taskPersistenceQueueState) completePendingLocked(write *taskAdmissionWrite) {
	if s == nil || write == nil {
		return
	}
	for index, pending := range s.pending {
		if pending != write {
			continue
		}
		copy(s.pending[index:], s.pending[index+1:])
		s.pending[len(s.pending)-1] = nil
		s.pending = s.pending[:len(s.pending)-1]
		s.signalLocked()
		break
	}
}

func (s *JSONLStore) enqueueTaskTransaction(
	ctx context.Context,
	admission *persistencequeue.Admission,
	payloads map[string][]byte,
) error {
	immutable := cloneTaskPayloads(payloads)
	if len(immutable) == 0 {
		return nil
	}
	sizeBytes, err := taskPayloadSize(immutable)
	if err != nil {
		return err
	}
	write := &taskAdmissionWrite{payloads: immutable, admission: admission}
	command := persistencequeue.PersistenceCommand{
		Owner:     persistencequeue.OwnerTask,
		Kind:      "task.jsonl.transaction",
		SizeBytes: sizeBytes,
		Apply: func(workerCtx context.Context) error {
			return s.applyTaskTransaction(workerCtx, write)
		},
	}
	return s.taskPersistence.acceptPendingLocked(write, func() error {
		_, err := admission.Enqueue(command)
		return err
	})
}

func (s *JSONLStore) applyTaskTransaction(ctx context.Context, write *taskAdmissionWrite) error {
	if s == nil || write == nil {
		return errors.New("Task persistence command is unavailable")
	}
	fail := func(err error) error {
		s.taskPersistence.failPending(write, err)
		return err
	}
	applyCtx := context.WithValue(ctx, taskAdmissionWriteContextKey{}, write)
	s.rotateIfDue(applyCtx)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return fail(errors.New("Task store is closed"))
	}
	if s.readOnly || s.writerLock == nil || s.batch == nil {
		return fail(errors.New("Task store writer is unavailable"))
	}
	err := s.writeBatchNow(applyCtx, func() (map[string][]byte, error) {
		return cloneTaskPayloads(write.payloads), nil
	})
	if err != nil {
		return fail(err)
	}
	return nil
}

func (s *JSONLStore) taskPersistencePostCommitHook(ord uint16) jsonlbatch.PostCommitHook {
	var indexHook jsonlbatch.PostCommitHook
	if s.idx != nil {
		indexHook = s.idx.onCommitFor(ord)
	}
	return func(ctx context.Context, appends []jsonlbatch.CommittedAppend) {
		write, accepted := ctx.Value(taskAdmissionWriteContextKey{}).(*taskAdmissionWrite)
		if accepted {
			s.taskPersistence.mu.Lock()
			if indexHook != nil {
				indexHook(ctx, appends)
			}
			s.taskPersistence.completePendingLocked(write)
			s.taskPersistence.mu.Unlock()
			return
		}
		if indexHook != nil {
			indexHook(ctx, appends)
		}
	}
}

func (s *JSONLStore) setTaskPersistencePostCommitHook(ord uint16) {
	if s != nil && s.batch != nil {
		s.batch.SetPostCommitHook(s.taskPersistencePostCommitHook(ord))
	}
}

func taskAdmissionValidationPayloads(pending, current map[string][]byte) map[string][]byte {
	combined := cloneTaskPayloads(pending)
	if combined == nil {
		combined = make(map[string][]byte)
	}
	appendTaskPayloads(combined, current)
	return combined
}

func (s *JSONLStore) admittedTransactionWithScope(
	ctx context.Context,
	scopeTaskID modulecore.TaskID,
	lease *taskExecutionLease,
	fn func(domaintask.Store) error,
	admission *persistencequeue.Admission,
) error {
	if s == nil || s.batch == nil {
		return errors.New("Task store batch is unavailable")
	}
	if s.idx == nil {
		return ErrTaskAdmissionRequiresIndex
	}
	if s.readOnly || s.writerLock == nil {
		return errors.New("Task store is read-only")
	}
	releaseAdmission, err := s.taskPersistence.beginAdmitted(ctx)
	if err != nil {
		return err
	}
	defer releaseAdmission()
	if !s.mu.TryRLock() {
		return ErrTaskAdmissionBusy
	}
	defer s.mu.RUnlock()
	if s.closed {
		return errors.New("Task store is closed")
	}
	admittedPending, releaseView, err := s.taskPersistence.lockAdmissionIndexView(s.idx)
	if err != nil {
		return err
	}
	defer releaseView()
	tx := newTaskTransaction(s, false, scopeTaskID, lease)
	tx.idxLocked = true
	defer tx.end()
	if err := tx.setAdmissionOverlay(admittedPending); err != nil {
		return err
	}
	err = fn(tx)
	snapshot := tx.freezeAndSnapshot()
	if err != nil {
		return err
	}
	if err := validateTaskTransaction(ctx, s, snapshot); err != nil {
		return err
	}
	if len(snapshot.payloads) == 0 {
		return nil
	}
	if err := s.idx.validateAppend(taskAdmissionValidationPayloads(admittedPending, snapshot.payloads)); err != nil {
		return err
	}
	return s.enqueueTaskTransaction(ctx, admission, snapshot.payloads)
}
