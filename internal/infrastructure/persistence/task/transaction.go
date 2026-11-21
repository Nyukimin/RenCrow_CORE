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
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	errTaskTransactionExpired                   = errors.New("task transaction is no longer active")
	errReadOnlyTransaction                      = errors.New("task transaction is read-only")
	ErrExpectedCriteriaRevisionImmutable        = errors.New("expected criteria revision is immutable after the first Task save")
	ErrNativeOPSResumeClaimImmutable            = errors.New("native OPS Resume claims are immutable and append-only")
	ErrTaskOriginPublicationProvenanceImmutable = errors.New("Task origin publication provenance is immutable after the first Task save")
)

func taskBatchFilenames() []string {
	return []string{stateFilename, runFilename, contextFilename, notificationsFilename, taskOperationReceiptFilename, taskExecutionFenceFilename, taskDelegationReportFilename}
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
	if err := s.activeTaskExecutionFenceError(taskID); err != nil {
		return err
	}
	return s.transactionWithScope(ctx, taskID, lease, fn)
}

func (s *JSONLStore) ActivityTransaction(ctx context.Context, taskIDs []modulecore.TaskID, fn func(domaintask.Store) error) error {
	if err := validateTransactionArgs(s, ctx, fn); err != nil {
		return err
	}
	if len(taskIDs) == 0 {
		return errors.New("activity transaction Task IDs are required")
	}
	if s.readOnly {
		return errors.New("task store is read-only")
	}
	ids := append([]modulecore.TaskID(nil), taskIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for i, taskID := range ids {
		if err := taskID.Validate(); err != nil {
			return fmt.Errorf("activity transaction task_id is invalid: %w", err)
		}
		if i > 0 && ids[i-1] == taskID {
			return fmt.Errorf("activity transaction task_id %s is duplicated", taskID)
		}
	}
	releaseAdmission, err := s.lifecycle.admit(ctx)
	if err != nil {
		return err
	}
	defer releaseAdmission()
	leases := make([]*taskExecutionLease, 0, len(ids))
	defer func() {
		for i := len(leases) - 1; i >= 0; i-- {
			leases[i].release()
		}
	}()
	for _, taskID := range ids {
		lease, acquireErr := s.executionFence.acquire(ctx, taskID)
		if acquireErr != nil {
			return acquireErr
		}
		leases = append(leases, lease)
		if err := s.activeTaskExecutionFenceError(taskID); err != nil {
			return err
		}
	}
	return s.transactionWithScope(ctx, "", nil, func(store domaintask.Store) error {
		if tx, ok := store.(*transactionStore); ok {
			tx.ownedLeases = append([]*taskExecutionLease(nil), leases...)
		}
		return fn(store)
	})
}

// WithTaskExecutionFence admits a synchronous external leaf while holding an
// exclusive Task gate. Only the short validation transaction runs under the
// global JSONL lock; the callback runs after that lock is released.
func (s *JSONLStore) WithTaskExecutionFence(ctx context.Context, taskID modulecore.TaskID, fn func() error) error {
	return s.withGenericTaskExecutionFence(ctx, taskID, fn)
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

func activityMetadataTransitionAllowed(previous, next *domaintask.ActivityMetadata) bool {
	if next == nil {
		return false
	}
	if previous == nil {
		return next.Revision == 1
	}
	if next.Revision <= previous.Revision {
		return false
	}
	if !reflect.DeepEqual(previous.Game, next.Game) {
		if previous.Game == nil || next.Game == nil || previous.Game.GameID != next.Game.GameID ||
			previous.Game.SessionID != "" || next.Game.SessionID == "" {
			return false
		}
	}
	prior := make(map[string]domaintask.ActivityParticipant, len(previous.Participants))
	for _, participant := range previous.Participants {
		prior[participant.ActorID] = participant
	}
	for _, participant := range next.Participants {
		old, exists := prior[participant.ActorID]
		if !exists {
			if participant.Revision == 0 {
				return false
			}
			continue
		}
		if participant.Revision < old.Revision || participant.IsCurrent != old.IsCurrent && participant.Revision <= old.Revision {
			return false
		}
	}
	return true
}

func (s *JSONLStore) transactionWithScope(ctx context.Context, scopeTaskID modulecore.TaskID, lease *taskExecutionLease, fn func(domaintask.Store) error) error {
	if admission, admitted := persistencequeue.AdmissionFromContext(ctx); admitted {
		return s.admittedTransactionWithScope(ctx, scopeTaskID, lease, fn, admission)
	}
	releaseSynchronousWrite, err := s.beginSynchronousWrite(ctx)
	if err != nil {
		return err
	}
	defer releaseSynchronousWrite()
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
	op := "Transaction"
	if scopeTaskID != "" {
		op = "TaskTransaction"
	}
	return s.writeBatchNow(withTxLabel(ctx, op, scopeTaskID), func() (map[string][]byte, error) {
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
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, admitted := persistencequeue.AdmissionFromContext(ctx); admitted {
		if !s.mu.TryRLock() {
			return taskAdmissionBusy(taskAdmissionBusyTransactionLock)
		}
	} else {
		s.mu.RLock()
	}
	defer s.mu.RUnlock()
	if s.closed {
		return os.ErrClosed
	}
	if s.batch == nil {
		return errors.New("task store batch is unavailable")
	}
	if s.idx != nil {
		return s.indexReadTransaction(ctx, fn)
	}
	if _, admitted := persistencequeue.AdmissionFromContext(ctx); admitted {
		return ErrTaskAdmissionRequiresIndex
	}
	return s.batch.Read(withTxLabel(ctx, "ReadTransaction", ""), func() error {
		tx := newTaskTransaction(s, true, "", nil)
		defer tx.end()
		return fn(tx)
	})
}

type transactionStore struct {
	parent      *JSONLStore
	readOnly    bool
	scopeTaskID modulecore.TaskID
	lease       *taskExecutionLease
	ownedLeases []*taskExecutionLease
	// idxLocked is true for the views of a read transaction on an indexed store,
	// which holds the index read lock for its whole callback.
	idxLocked       bool
	admittedPending map[string][]byte

	mu    sync.Mutex
	local bool
	state *transactionState
}

type transactionState struct {
	mu                sync.Mutex
	active            bool
	pending           map[string][]byte
	touched           map[modulecore.TaskID]struct{}
	ownerOnlyAccessed bool
	pendingVersion    uint64
	baseLoaded        bool
	base              *transactionTaskRunBase
	viewVersion       uint64
	view              *transactionTaskRunView
	// ov mirrors the pending appends as decoded records; it is set only for a
	// write transaction on an indexed store (see index_tx.go).
	ov *txOverlay
}

func newTaskTransaction(parent *JSONLStore, readOnly bool, scopeTaskID modulecore.TaskID, lease *taskExecutionLease) *transactionStore {
	state := &transactionState{active: true, pending: make(map[string][]byte), touched: make(map[modulecore.TaskID]struct{})}
	if parent != nil && parent.idx != nil && !readOnly {
		state.ov = newTxOverlay()
	}
	return &transactionStore{
		parent:      parent,
		readOnly:    readOnly,
		scopeTaskID: scopeTaskID,
		lease:       lease,
		local:       true,
		state:       state,
	}
}

type transactionSnapshot struct {
	payloads          map[string][]byte
	touched           map[modulecore.TaskID]struct{}
	owner             *taskExecutionLease
	owners            []*taskExecutionLease
	base              *transactionTaskRunBase
	admittedPending   map[string][]byte
	ownerOnlyAccessed bool
}

func (s *transactionStore) setAdmissionOverlay(payloads map[string][]byte) error {
	if s == nil || s.state == nil {
		return errTaskTransactionExpired
	}
	s.admittedPending = cloneTaskPayloads(payloads)
	if s.parent == nil || s.parent.idx == nil || len(payloads) == 0 {
		return nil
	}
	if s.state.ov == nil {
		s.state.ov = newTxOverlay()
	}
	for name, payload := range payloads {
		for _, line := range bytes.SplitAfter(payload, []byte{'\n'}) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			if err := s.state.ov.record(name, line); err != nil {
				return fmt.Errorf("decode accepted Task persistence overlay %s: %w", name, err)
			}
		}
	}
	return nil
}

func validateTaskTransaction(ctx context.Context, parent *JSONLStore, snapshot transactionSnapshot) error {
	if parent == nil {
		return errors.New("task store is unavailable")
	}
	settlingTask, settling := taskMeetingSettlementTask(ctx, parent, snapshot)
	if parent.executionFence != nil {
		if activeTaskID := parent.executionFence.hasActiveOwned(snapshot.touched, snapshot.owner, snapshot.owners); activeTaskID != "" {
			if !settling || activeTaskID != settlingTask {
				return fmt.Errorf("%w: task transaction conflicts with active execution fence for task %s", ErrTaskExecutionFenceActive, activeTaskID)
			}
		}
	}
	for taskID := range snapshot.touched {
		if err := parent.activeTaskExecutionFenceError(taskID); err != nil {
			if !settling || taskID != settlingTask {
				return err
			}
		}
	}
	if parent.idx != nil {
		// The records of an indexed store were checked as they were written and
		// are dry-run against the index right before commit (writeBatch), which
		// replaces the fold of every record in the log.
		return nil
	}
	view := &transactionStore{
		parent:          parent,
		readOnly:        true,
		scopeTaskID:     "",
		local:           true,
		admittedPending: cloneTaskPayloads(snapshot.admittedPending),
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
	view := &transactionStore{parent: s.parent, readOnly: true, scopeTaskID: s.scopeTaskID, lease: s.lease, idxLocked: s.idxLocked, local: true, state: s.state, admittedPending: s.admittedPending}
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
	if s.state.ov != nil {
		if err := s.state.ov.record(filename, encoded); err != nil {
			return err
		}
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
	snapshot := transactionSnapshot{owner: s.lease, owners: append([]*taskExecutionLease(nil), s.ownedLeases...)}
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
	snapshot.admittedPending = cloneTaskPayloads(s.admittedPending)
	snapshot.ownerOnlyAccessed = s.state.ownerOnlyAccessed
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
		return append([]byte(nil), s.admittedPending[filename]...)
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	combined := append([]byte(nil), s.admittedPending[filename]...)
	return append(combined, s.state.pending[filename]...)
}

func (s *transactionStore) SaveTask(ctx context.Context, value domaintask.Task) error {
	if domaintask.IsOwnerManagedTask(value) {
		return domaintask.ErrTypedTaskRequiresOwner
	}
	if value.PrivateAccess != nil {
		return domaintask.ErrPrivateTaskWriteAuthorizationRequired
	}
	return s.saveTask(ctx, value)
}

func (s *transactionStore) SaveTaskActivity(ctx context.Context, value domaintask.Task) error {
	if err := s.checkWrite(ctx); err != nil {
		return err
	}
	if value.Activity == nil {
		return domaintask.ErrCurrentActivityInvalid
	}
	if err := value.Validate(); err != nil {
		return err
	}
	existing, err := s.getTaskRaw(ctx, value.TaskID)
	if err != nil {
		return err
	}
	expected := cloneTransactionTask(value)
	comparable := cloneTransactionTask(existing)
	comparable.Activity = expected.Activity
	comparable.UpdatedAt = expected.UpdatedAt
	if !reflect.DeepEqual(comparable, expected) {
		return domaintask.ErrActivityTaskRequiresOwner
	}
	if !activityMetadataTransitionAllowed(existing.Activity, value.Activity) {
		return domaintask.ErrCurrentActivityConflict
	}
	return s.appendActivityTask(ctx, value)
}

func (s *transactionStore) SaveNewPassiveActivityTask(ctx context.Context, value domaintask.Task) error {
	if ctx == nil || value.Activity == nil {
		return errors.New("new passive activity Task does not meet the restricted fallback contract")
	}
	if value.Status != domaintask.StatusRunning || value.StartedAt == nil || value.FinishedAt != nil || value.Route != domaintask.RouteGeneral ||
		!value.Activity.Passive || value.Activity.Revision != 1 {
		return errors.New("new passive activity Task has invalid lifecycle state")
	}
	switch value.Activity.Kind {
	case domaintask.ActivityKindEvaluating:
		if len(value.Activity.Participants) != 1 || value.Activity.Game != nil || value.Title != "Evaluating next activity" ||
			value.Activity.Label != "Evaluating next activity" || value.Activity.Phase != "evaluating" {
			return errors.New("new evaluating Task does not meet the restricted fallback contract")
		}
		participant := value.Activity.Participants[0]
		if !participant.IsCurrent || participant.ActorID != value.Assignee {
			return errors.New("new evaluating Task must bind its assignee as the sole current participant")
		}
	case domaintask.ActivityKindGame:
		if value.Activity.Game == nil || value.Activity.Game.GameID == "" || value.Activity.Game.SessionID == "" ||
			value.Title != "Playing "+value.Activity.Game.GameID || value.Activity.Label != value.Title ||
			value.Activity.Phase == "" || len(value.Activity.Participants) == 0 || value.Activity.Participants[0].ActorID != value.Assignee {
			return errors.New("new observed game Task does not meet the restricted adoption contract")
		}
		for _, participant := range value.Activity.Participants {
			if participant.IsCurrent || participant.Revision != 1 {
				return errors.New("new observed game Task participants must begin unassigned")
			}
		}
	default:
		return errors.New("new passive activity Task kind is outside the restricted owner contract")
	}
	expected := domaintask.Task{
		TaskID: value.TaskID, Title: value.Title, Assignee: value.Assignee, Route: domaintask.RouteGeneral,
		Status: domaintask.StatusRunning, Priority: domaintask.PriorityNormal,
		InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true, CreatedAt: value.CreatedAt,
		UpdatedAt: value.UpdatedAt, StartedAt: value.StartedAt, Activity: value.Activity,
	}
	if !reflect.DeepEqual(value, expected) {
		return errors.New("new passive activity Task contains fields outside the restricted owner contract")
	}
	if err := value.Validate(); err != nil {
		return err
	}
	if err := s.checkWrite(ctx); err != nil {
		return err
	}
	if _, err := s.getTaskRaw(ctx, value.TaskID); err == nil {
		return fmt.Errorf("passive activity Task %s already exists: %w", value.TaskID, domaintask.ErrCurrentActivityConflict)
	} else if !errors.Is(err, domaintask.ErrNotFound) {
		return err
	}
	return s.appendActivityTask(ctx, value)
}

func (s *transactionStore) GetTaskActivity(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	if err := s.check(ctx); err != nil {
		return domaintask.Task{}, err
	}
	return s.getTaskRaw(ctx, taskID)
}

func (s *transactionStore) SaveTaskAuthorized(ctx context.Context, value domaintask.Task, authorize domaintask.TaskWriteAuthorizationCheck) error {
	if domaintask.IsOwnerManagedTask(value) {
		return domaintask.ErrTypedTaskRequiresOwner
	}
	if ctx == nil {
		return errors.New("private Task write context is nil")
	}
	if authorize == nil {
		return domaintask.ErrPrivateTaskWriteAuthorizationRequired
	}
	if err := value.Validate(); err != nil {
		return err
	}
	input, err := domaintask.PrivateTaskWriteInput(value)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := authorize(ctx, input); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return domaintask.ErrPrivateTaskAccessForbidden
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.saveTask(ctx, value)
}

func (s *transactionStore) saveTask(ctx context.Context, value domaintask.Task) error {
	if err := s.checkWrite(ctx); err != nil {
		return err
	}
	if err := value.Validate(); err != nil {
		return err
	}
	existing, existingErr := s.getTaskRaw(ctx, value.TaskID)
	if existingErr != nil && !errors.Is(existingErr, domaintask.ErrNotFound) {
		return existingErr
	}
	if existingErr == nil && !reflect.DeepEqual(existing.Activity, value.Activity) {
		return domaintask.ErrActivityTaskRequiresOwner
	}
	if errors.Is(existingErr, domaintask.ErrNotFound) && value.Activity != nil {
		return domaintask.ErrActivityTaskRequiresOwner
	}
	// Prime the committed Task history before entering the atomic compare+append
	// below. The committed fold validates every prior pin transition, while the
	// locked append also checks Task records appended concurrently in this
	// transaction. An indexed store checked the committed history as it absorbed
	// it, so there is nothing to prime.
	if s.parent.idx == nil {
		if _, err := s.loadTasks(ctx); err != nil {
			return err
		}
	}
	return s.appendTask(ctx, value)
}

func (s *transactionStore) appendTask(ctx context.Context, value domaintask.Task) error {
	return s.appendTaskWithScope(ctx, value, false)
}

func (s *transactionStore) appendActivityTask(ctx context.Context, value domaintask.Task) error {
	return s.appendTaskWithScope(ctx, value, true)
}

func (s *transactionStore) appendTaskWithScope(ctx context.Context, value domaintask.Task, activityOnly bool) error {
	if err := s.checkWrite(ctx); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if s.state == nil {
		return errTaskTransactionExpired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.local {
		return errTaskTransactionExpired
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if !s.state.active {
		return errTaskTransactionExpired
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !activityOnly && s.scopeTaskID != "" && s.scopeTaskID != value.TaskID {
		return errTaskExecutionFenceScope
	}
	if s.parent.idx != nil {
		if err := s.idxCheckSaveTask(value); err != nil {
			return err
		}
	} else {
		if !s.state.baseLoaded || s.state.base == nil {
			return errors.New("task transaction committed snapshot is unavailable")
		}
		if s.state.base.tasksErr != nil {
			return s.state.base.tasksErr
		}
		records := append([]domaintask.Task(nil), s.state.base.tasks...)
		pending, err := readJSONLBytes[domaintask.Task](ctx, s.state.pending[stateFilename])
		if err != nil {
			return err
		}
		records = append(records, pending...)
		latest, err := foldTaskRecords(records)
		if err != nil {
			return err
		}
		foundSavedTask := false
		for _, saved := range latest {
			if saved.TaskID == value.TaskID {
				foundSavedTask = true
				if err := checkTaskPinTransition(pinOfTask(saved), value); err != nil {
					return err
				}
			}
		}
		if !foundSavedTask && len(value.NativeResumeClaims) != 0 {
			return ErrNativeOPSResumeClaimImmutable
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.state.ov != nil {
		if err := s.state.ov.record(stateFilename, encoded); err != nil {
			return err
		}
	}
	s.state.touched[value.TaskID] = struct{}{}
	s.state.pending[stateFilename] = append(s.state.pending[stateFilename], encoded...)
	s.state.pendingVersion++
	s.state.view = nil
	return nil
}

func (s *transactionStore) SaveRun(ctx context.Context, value domaintask.Run) error {
	if err := value.Validate(); err != nil {
		return err
	}
	parent, err := s.getTaskRaw(ctx, value.TaskID)
	if err != nil {
		if !errors.Is(err, domaintask.ErrNotFound) {
			return err
		}
		return fmt.Errorf("run task is unavailable: %w", domaintask.ErrNotFound)
	}
	if parent.PrivateAccess != nil {
		return domaintask.ErrPrivateTaskWriteAuthorizationRequired
	}
	if domaintask.IsOwnerManagedTask(parent) {
		return domaintask.ErrTypedTaskRequiresOwner
	}
	return s.saveRun(ctx, value)
}

func (s *transactionStore) SaveRunAuthorized(ctx context.Context, value domaintask.Run, authorize domaintask.TaskWriteAuthorizationCheck) error {
	if ctx == nil {
		return errors.New("private Run write context is nil")
	}
	if authorize == nil {
		return domaintask.ErrPrivateTaskWriteAuthorizationRequired
	}
	if err := value.Validate(); err != nil {
		return err
	}
	parent, err := s.getTaskRaw(ctx, value.TaskID)
	if err != nil {
		return domaintask.ErrNotFound
	}
	if parent.PrivateAccess == nil {
		return errors.New("authorized private Run write requires a private Task")
	}
	if domaintask.IsOwnerManagedTask(parent) {
		return domaintask.ErrTypedTaskRequiresOwner
	}
	input, err := domaintask.TaskWriteInput(parent, domaintask.TaskWriteResourceRun, value.RunID)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := authorize(ctx, input); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return domaintask.ErrPrivateTaskAccessForbidden
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	currentParent, err := s.getTaskRaw(ctx, value.TaskID)
	if err != nil || !samePrivateTaskAccess(currentParent, parent) {
		return domaintask.ErrPrivateTaskAccessForbidden
	}
	return s.saveRun(ctx, value)
}

func (s *transactionStore) saveRun(ctx context.Context, value domaintask.Run) error {
	if err := s.checkWrite(ctx); err != nil {
		return err
	}
	if err := value.Validate(); err != nil {
		return err
	}
	if err := s.touchTask(ctx, value.TaskID); err != nil {
		return err
	}
	if s.parent.idx != nil {
		if err := s.idxSaveRunChecks(value); err != nil {
			return err
		}
		return s.appendValue(ctx, runFilename, value)
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
	run, err := s.getRunRaw(ctx, runID)
	if err != nil {
		return domaintask.Run{}, err
	}
	task, err := s.getTaskRaw(ctx, run.TaskID)
	if errors.Is(err, domaintask.ErrNotFound) || err == nil && task.PrivateAccess != nil {
		return domaintask.Run{}, domaintask.ErrNotFound
	}
	if err != nil {
		return domaintask.Run{}, err
	}
	if domaintask.IsOwnerManagedTask(task) {
		return domaintask.Run{}, domaintask.ErrTypedTaskRequiresOwner
	}
	if domaintask.IsOwnerManagedTask(task) {
		return domaintask.Run{}, domaintask.ErrTypedTaskRequiresOwner
	}
	return run, nil
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
	if s.parent.idx != nil {
		return s.idxListRuns(ctx, filter)
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
	privateTasks := privateTaskIDs(tasks)
	ownerManagedTasks := make(map[modulecore.TaskID]struct{})
	for _, task := range tasks {
		if domaintask.IsOwnerManagedTask(task) {
			ownerManagedTasks[task.TaskID] = struct{}{}
		}
	}
	filtered := make([]domaintask.Run, 0, len(items))
	for _, item := range items {
		if _, private := privateTasks[item.TaskID]; private {
			continue
		}
		if _, owned := ownerManagedTasks[item.TaskID]; owned {
			continue
		}
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
	task, err := s.getTaskRaw(ctx, taskID)
	if err != nil {
		return domaintask.Task{}, err
	}
	if task.PrivateAccess != nil {
		return domaintask.Task{}, domaintask.ErrNotFound
	}
	if domaintask.IsOwnerManagedTask(task) {
		return domaintask.Task{}, domaintask.ErrTypedTaskRequiresOwner
	}
	return task, nil
}

func (s *transactionStore) CurrentActivityTasks(ctx context.Context, actorIDs []string) (map[string]domaintask.Task, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	if err := validateActivityActorIDs(actorIDs); err != nil {
		return nil, err
	}
	wanted := make(map[string]struct{}, len(actorIDs))
	for _, actorID := range actorIDs {
		wanted[actorID] = struct{}{}
	}
	var current map[string]domaintask.Task
	if s.parent.idx != nil {
		var err error
		if s.idxLocked {
			current, err = s.parent.idx.currentActivityTasksLocked(ctx, actorIDs)
		} else {
			current, err = s.parent.idx.currentActivityTasks(ctx, actorIDs)
		}
		if err != nil {
			return nil, err
		}
	} else {
		all, err := s.loadTasks(ctx)
		if err != nil {
			return nil, err
		}
		current = make(map[string]domaintask.Task)
		for _, task := range all {
			if task.Activity == nil {
				continue
			}
			for _, participant := range task.Activity.Participants {
				if !participant.IsCurrent {
					continue
				}
				if _, ok := wanted[participant.ActorID]; !ok {
					continue
				}
				if prior, duplicate := current[participant.ActorID]; duplicate && prior.TaskID != task.TaskID {
					return nil, fmt.Errorf("actor %s has multiple current activity Tasks: %w", participant.ActorID, domaintask.ErrCurrentActivityConflict)
				}
				current[participant.ActorID] = task
			}
		}
	}
	if s.state == nil || s.state.ov == nil {
		return current, nil
	}
	s.state.mu.Lock()
	overlay := make([]domaintask.Task, 0, len(s.state.ov.tasks))
	for _, task := range s.state.ov.tasks {
		overlay = append(overlay, cloneTransactionTask(task))
	}
	s.state.mu.Unlock()
	for _, task := range overlay {
		for actorID, currentTask := range current {
			if currentTask.TaskID == task.TaskID {
				delete(current, actorID)
			}
		}
	}
	for _, task := range overlay {
		if task.Activity == nil {
			continue
		}
		for _, participant := range task.Activity.Participants {
			if !participant.IsCurrent {
				continue
			}
			if _, ok := wanted[participant.ActorID]; !ok {
				continue
			}
			if prior, duplicate := current[participant.ActorID]; duplicate && prior.TaskID != task.TaskID {
				return nil, fmt.Errorf("actor %s has multiple pending current activity Tasks: %w", participant.ActorID, domaintask.ErrCurrentActivityConflict)
			}
			current[participant.ActorID] = task
		}
	}
	return current, nil
}

func (s *transactionStore) ListTasks(ctx context.Context, filter domaintask.Filter) ([]domaintask.Task, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	if err := validateTaskListCursorFilter(filter); err != nil {
		return nil, err
	}
	if filter.TaskIDAfter != nil && s.parent.idx == nil {
		view, err := s.taskRunView(ctx)
		if err != nil {
			return nil, err
		}
		if view.tasksErr != nil {
			return nil, view.tasksErr
		}
		cursor := *filter.TaskIDAfter
		page := newBestN(filter.Limit, func(a, b domaintask.Task) bool { return a.TaskID < b.TaskID })
		for _, item := range view.tasks {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if item.TaskID <= cursor || !taskMatchesFilter(item, filter) {
				continue
			}
			page.add(item)
		}
		return cloneTransactionTasks(page.result()), nil
	}
	if s.parent.idx != nil {
		items, err := s.idxListTasks(ctx, filter)
		if err != nil {
			return nil, err
		}
		filtered := items[:0]
		for _, item := range items {
			if item.PrivateAccess == nil && !domaintask.IsOwnerManagedTask(item) {
				filtered = append(filtered, item)
			}
		}
		return filtered, nil
	}
	items, err := s.loadTasks(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]domaintask.Task, 0, len(items))
	for _, item := range items {
		if item.PrivateAccess != nil || domaintask.IsOwnerManagedTask(item) {
			continue
		}
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

func validateTaskListCursorFilter(filter domaintask.Filter) error {
	if filter.TaskIDAfter == nil {
		return nil
	}
	if filter.Limit < 1 || filter.Limit > domaintask.MaxTaskListPageSize {
		return fmt.Errorf("task ID cursor requires a limit between 1 and %d", domaintask.MaxTaskListPageSize)
	}
	if *filter.TaskIDAfter != "" {
		if err := filter.TaskIDAfter.Validate(); err != nil {
			return fmt.Errorf("task ID cursor is invalid: %w", err)
		}
	}
	return nil
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
	parent, err := s.GetTask(ctx, value.TaskID)
	if err != nil {
		return err
	}
	if domaintask.IsOwnerManagedTask(parent) {
		return domaintask.ErrTypedTaskRequiresOwner
	}
	return s.appendValue(ctx, contextFilename, value)
}

func (s *transactionStore) SaveContextAuthorized(ctx context.Context, value domaintask.SharedRoleContext, authorize domaintask.TaskWriteAuthorizationCheck) error {
	if ctx == nil {
		return errors.New("private context write context is nil")
	}
	if authorize == nil {
		return domaintask.ErrPrivateTaskWriteAuthorizationRequired
	}
	if err := value.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	parent, err := s.getTaskRaw(ctx, value.TaskID)
	if err != nil || parent.PrivateAccess == nil {
		return domaintask.ErrNotFound
	}
	input, err := domaintask.TaskWriteInput(parent, domaintask.TaskWriteResourceContext, "")
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := authorize(ctx, input); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return domaintask.ErrPrivateTaskAccessForbidden
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	currentParent, err := s.getTaskRaw(ctx, value.TaskID)
	if err != nil || !samePrivateTaskAccess(currentParent, parent) {
		return domaintask.ErrPrivateTaskAccessForbidden
	}
	if err := s.checkWrite(ctx); err != nil {
		return err
	}
	if err := s.touchTask(ctx, value.TaskID); err != nil {
		return err
	}
	return s.appendValue(ctx, contextFilename, value)
}

func (s *transactionStore) GetContext(ctx context.Context, taskID modulecore.TaskID) (domaintask.SharedRoleContext, error) {
	if _, err := s.GetTask(ctx, taskID); err != nil {
		if errors.Is(err, domaintask.ErrNotFound) {
			return domaintask.SharedRoleContext{}, domaintask.ErrNotFound
		}
		return domaintask.SharedRoleContext{}, err
	}
	return s.getContextRaw(ctx, taskID)
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
	if parent, err := s.getTaskRaw(ctx, value.TaskID); err == nil && parent.PrivateAccess != nil {
		return domaintask.ErrPrivateTaskWriteAuthorizationRequired
	} else if err == nil && domaintask.IsOwnerManagedTask(parent) {
		return domaintask.ErrTypedTaskRequiresOwner
	} else if err != nil && !errors.Is(err, domaintask.ErrNotFound) {
		return err
	}
	return s.appendValue(ctx, notificationsFilename, value)
}

func (s *transactionStore) ListNotifications(ctx context.Context, limit int, interruptOnly bool) ([]domaintask.Notification, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	if s.parent.idx != nil {
		return s.idxListNotifications(ctx, limit, interruptOnly)
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
	tasks, err := s.loadTasks(ctx)
	if err != nil {
		return nil, err
	}
	publicTasks := make(map[modulecore.TaskID]struct{}, len(tasks))
	for _, task := range tasks {
		if task.PrivateAccess == nil && !domaintask.IsOwnerManagedTask(task) {
			publicTasks[task.TaskID] = struct{}{}
		}
	}
	for _, item := range items {
		if interruptOnly && !item.Interrupt {
			continue
		}
		if _, visible := publicTasks[item.TaskID]; !visible {
			continue
		}
		filtered = append(filtered, item)
	}
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func privateTaskIDs(tasks []domaintask.Task) map[modulecore.TaskID]struct{} {
	private := make(map[modulecore.TaskID]struct{})
	for _, task := range tasks {
		if task.PrivateAccess != nil {
			private[task.TaskID] = struct{}{}
		}
	}
	return private
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
