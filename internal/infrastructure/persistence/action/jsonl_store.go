package action

import (
	"bufio"
	"bytes"
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

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/txobserve"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	actionFilename  = "action_state.jsonl"
	attemptFilename = "action_attempt.jsonl"
)

var (
	errReadOnlyTransaction = errors.New("action transaction is read-only")
	errExpiredTransaction  = errors.New("action transaction store expired")
)

// JSONLStore persists Action and Attempt records under one owner-local WAL.
// The in-process mutex protects one instance; jsonlbatch supplies the OS lock
// shared by every instance using the same root.
type JSONLStore struct {
	mu          sync.RWMutex
	root        string
	actionPath  string
	attemptPath string
	batch       *jsonlbatch.Store
	txObserver  *txobserve.Observer
	// idx is the in-memory snapshot of both logs; nil when the store was opened
	// without OpenOptions.Index, in which case every transaction reads the logs
	// in full.
	idx *snapshot
}

// OpenOptions selects optional behavior of the Action store. The zero value is
// the behavior this package has always had.
type OpenOptions struct {
	// Index keeps a snapshot of both logs in memory. The log stays the only source
	// of truth: the snapshot is built from it at open and followed after every
	// commit, and a store opened without Index reads the same files.
	Index bool
}

// defaultIndexOption is what NewJSONLStore uses. It is false in every build
// except one made with the actionindexdefault tag, which exists so the consumers'
// tests can run against the snapshot (see snapshot_default_on.go).
var defaultIndexOption = false

// NewJSONLStore opens the Action files, repairs a pending WAL as a writer, and
// rejects malformed persisted state before returning a usable store.
func NewJSONLStore(root string) (*JSONLStore, error) {
	return newJSONLStore(root, OpenOptions{Index: defaultIndexOption}, nil)
}

// NewJSONLStoreWithOptions is NewJSONLStore with optional behavior selected.
func NewJSONLStoreWithOptions(root string, opts OpenOptions) (*JSONLStore, error) {
	return newJSONLStore(root, opts, nil)
}

// newJSONLStore is NewJSONLStoreWithOptions with a seam for tests: tune may
// change the observer options (to capture the log or force the slow path) before
// the observer is built.
func newJSONLStore(root string, options OpenOptions, tune func(*txobserve.Options)) (*JSONLStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("action store root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve action store root: %w", err)
	}
	batch, err := jsonlbatch.New(absRoot, []string{actionFilename, attemptFilename})
	if err != nil {
		return nil, err
	}
	if err := batch.Recover(context.Background()); err != nil {
		return nil, fmt.Errorf("recover action store: %w", err)
	}
	store := &JSONLStore{
		root:        absRoot,
		actionPath:  filepath.Join(absRoot, actionFilename),
		attemptPath: filepath.Join(absRoot, attemptFilename),
		batch:       batch,
	}
	// Observing starts with the open's own validation read, so a slow start shows
	// up as op=Open instead of being invisible until the first request.
	opts := txObserverOptions(store.actionPath, store.attemptPath)
	if tune != nil {
		tune(&opts)
	}
	store.txObserver = txobserve.New(opts)
	batch.SetTxObserver(store.txObserver.Observe)
	if options.Index {
		ix := newSnapshot(store.actionPath, store.attemptPath)
		if err := batch.Read(withTxLabel(context.Background(), txOpOpen), func() error {
			return ix.build(context.Background())
		}); err != nil {
			return nil, fmt.Errorf("validate action store: %w", err)
		}
		store.idx = ix
		// From here on the snapshot follows every commit of this Store, under the
		// same OS lock as the writer.
		batch.SetPostCommitHook(ix.onCommit)
		return store, nil
	}
	if err := batch.Read(withTxLabel(context.Background(), txOpOpen), func() error {
		actions, err := store.loadActions(context.Background())
		if err != nil {
			return fmt.Errorf("validate action state: %w", err)
		}
		attempts, err := store.loadAttempts(context.Background())
		if err != nil {
			return fmt.Errorf("validate attempt state: %w", err)
		}
		if err := validateSnapshot(actions, attempts); err != nil {
			return fmt.Errorf("validate action snapshot: %w", err)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("validate action store: %w", err)
	}
	return store, nil
}

// Transaction executes one callback against a disk-plus-pending overlay. The
// overlay is encoded and committed only when the callback returns nil.
func (s *JSONLStore) Transaction(ctx context.Context, callback func(domainaction.Store) error) error {
	return s.transaction(ctx, txOpTransaction, callback)
}

// transaction is Transaction labelled with the store method that called it, so a
// slow-transaction line names the entry point.
func (s *JSONLStore) transaction(ctx context.Context, op string, callback func(domainaction.Store) error) error {
	if err := validateStoreContext(ctx); err != nil {
		return err
	}
	if callback == nil {
		return fmt.Errorf("action transaction callback is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idx != nil {
		return s.snapshotTransaction(ctx, op, callback)
	}
	return s.batch.Write(withTxLabel(ctx, op), func() (map[string][]byte, error) {
		actions, err := s.loadActions(ctx)
		if err != nil {
			return nil, err
		}
		attempts, err := s.loadAttempts(ctx)
		if err != nil {
			return nil, err
		}
		if err := validateSnapshot(actions, attempts); err != nil {
			return nil, err
		}
		tx := newTransactionStore(actions, attempts, false)
		defer tx.invalidate()
		if err := callback(tx); err != nil {
			return nil, err
		}
		return tx.finalize()
	})
}

// snapshotTransaction is transaction for a store with a snapshot: the callback
// runs on the snapshot with only its own writes on top, and the transaction is
// held to the group rule for what it touched. s.mu is held. Once the commit is
// durable, the post-commit hook applies the bytes to the snapshot under the same
// OS lock; a write that ends without a known commit boundary marks it dirty.
func (s *JSONLStore) snapshotTransaction(ctx context.Context, op string, callback func(domainaction.Store) error) error {
	err := s.batch.Write(withTxLabel(ctx, op), func() (map[string][]byte, error) {
		if err := s.idx.ensureSynced(ctx); err != nil {
			return nil, err
		}
		// Held while the callback runs and released when it returns, before the
		// commit: the hook that follows needs the write lock.
		s.idx.mu.RLock()
		defer s.idx.mu.RUnlock()
		tx := newSnapshotTransaction(s.idx.st, false)
		defer tx.invalidate()
		if err := callback(tx); err != nil {
			return nil, err
		}
		return tx.finalize()
	})
	s.idx.noteWriteResult(err)
	return err
}

// ReadTransaction runs against one OS-locked, committed snapshot. Its
// callback receives a read-only transaction store and cannot write state.
func (s *JSONLStore) ReadTransaction(ctx context.Context, callback func(domainaction.Store) error) error {
	return s.readTransaction(ctx, txOpReadTransaction, callback)
}

// readTransaction is ReadTransaction labelled with the store method that called it.
func (s *JSONLStore) readTransaction(ctx context.Context, op string, callback func(domainaction.Store) error) error {
	if err := validateStoreContext(ctx); err != nil {
		return err
	}
	if callback == nil {
		return fmt.Errorf("action read transaction callback is nil")
	}
	if s.idx != nil {
		return s.snapshotReadTransaction(ctx, op, callback)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.batch.Read(withTxLabel(ctx, op), func() error {
		actions, err := s.loadActions(ctx)
		if err != nil {
			return err
		}
		attempts, err := s.loadAttempts(ctx)
		if err != nil {
			return err
		}
		if err := validateSnapshot(actions, attempts); err != nil {
			return err
		}
		tx := newTransactionStore(actions, attempts, true)
		defer tx.invalidate()
		return callback(tx)
	})
}

// snapshotReadTransaction is readTransaction for a store with a snapshot. When
// the snapshot is current (no mark is set and both logs are as long as it
// covers) the read takes no OS lock and touches no file but for two stat calls;
// otherwise it first brings the snapshot up to the logs under the OS lock, which
// also fails closed while the WAL is not settled.
//
// It does not take s.mu: that lock serializes the writers of one store. A reader
// is excluded from a writer's change to the snapshot by snapshot.mu, which the
// writer's post-commit hook takes for writing, and from a writer in another
// handle or process by the length check and the OS lock behind it.
func (s *JSONLStore) snapshotReadTransaction(ctx context.Context, op string, callback func(domainaction.Store) error) error {
	started := time.Now()
	stale, err := s.idx.stale()
	if err != nil {
		return err
	}
	if stale {
		if err := s.batch.Read(withTxLabel(ctx, op), func() error { return s.idx.ensureSynced(ctx) }); err != nil {
			return err
		}
		started = time.Now()
	}
	err = func() error {
		s.idx.mu.RLock()
		defer s.idx.mu.RUnlock()
		tx := newSnapshotTransaction(s.idx.st, true)
		defer tx.invalidate()
		return callback(tx)
	}()
	if !stale {
		// jsonlbatch saw none of this read; the observer is told, so the summary
		// line still counts reads and their time.
		s.txObserver.Observe(withTxLabel(ctx, op), jsonlbatch.TxObservation{
			Kind: jsonlbatch.TxKindRead, Acquired: true, Exec: time.Since(started), Err: err,
		})
	}
	return err
}

func (s *JSONLStore) SaveAction(ctx context.Context, value domainaction.Action) error {
	return s.transaction(ctx, txOpSaveAction, func(tx domainaction.Store) error {
		return tx.SaveAction(ctx, value)
	})
}

func (s *JSONLStore) GetAction(ctx context.Context, actionID modulecore.ActionID) (domainaction.Action, error) {
	if err := actionID.Validate(); err != nil {
		return domainaction.Action{}, err
	}
	var result domainaction.Action
	err := s.readTransaction(ctx, txOpGetAction, func(tx domainaction.Store) error {
		var err error
		result, err = tx.GetAction(ctx, actionID)
		return err
	})
	if err != nil {
		return domainaction.Action{}, err
	}
	return result, nil
}

func (s *JSONLStore) ListActions(ctx context.Context, filter domainaction.Filter) ([]domainaction.Action, error) {
	var result []domainaction.Action
	err := s.readTransaction(ctx, txOpListActions, func(tx domainaction.Store) error {
		var err error
		result, err = tx.ListActions(ctx, filter)
		return err
	})
	return result, err
}

func (s *JSONLStore) SaveAttempt(ctx context.Context, value domainaction.Attempt) error {
	return s.transaction(ctx, txOpSaveAttempt, func(tx domainaction.Store) error {
		return tx.SaveAttempt(ctx, value)
	})
}

func (s *JSONLStore) GetAttempt(ctx context.Context, attemptID modulecore.AttemptID) (domainaction.Attempt, error) {
	if err := attemptID.Validate(); err != nil {
		return domainaction.Attempt{}, err
	}
	var result domainaction.Attempt
	err := s.readTransaction(ctx, txOpGetAttempt, func(tx domainaction.Store) error {
		var err error
		result, err = tx.GetAttempt(ctx, attemptID)
		return err
	})
	if err != nil {
		return domainaction.Attempt{}, err
	}
	return result, nil
}

func (s *JSONLStore) ListAttempts(ctx context.Context, filter domainaction.AttemptFilter) ([]domainaction.Attempt, error) {
	var result []domainaction.Attempt
	err := s.readTransaction(ctx, txOpListAttempts, func(tx domainaction.Store) error {
		var err error
		result, err = tx.ListAttempts(ctx, filter)
		return err
	})
	return result, err
}

// transactionStore is the Store a transaction callback works on. It has two
// shapes. Built from a full read (base is nil), actions and attempts are the whole
// store. Built on a snapshot (base is set), they hold only the records this
// transaction saved, and every other record is read from base, which nothing
// changes while the transaction runs. Both shapes answer the same questions the
// same way; the second never copies the store.
type transactionStore struct {
	lifecycle       sync.RWMutex
	dataMu          sync.Mutex
	active          bool
	readOnly        bool
	base            *snapState
	actions         map[modulecore.ActionID]domainaction.Action
	attempts        map[modulecore.AttemptID]domainaction.Attempt
	pendingActions  []domainaction.Action
	pendingAttempts []domainaction.Attempt
}

func newTransactionStore(actions []domainaction.Action, attempts []domainaction.Attempt, readOnly bool) *transactionStore {
	tx := &transactionStore{
		active:   true,
		readOnly: readOnly,
		actions:  make(map[modulecore.ActionID]domainaction.Action, len(actions)),
		attempts: make(map[modulecore.AttemptID]domainaction.Attempt, len(attempts)),
	}
	for _, item := range actions {
		tx.actions[item.ActionID] = cloneAction(item)
	}
	for _, item := range attempts {
		tx.attempts[item.AttemptID] = cloneAttempt(item)
	}
	return tx
}

// newSnapshotTransaction is a transaction on the snapshot content base. The
// caller keeps the snapshot's read lock (and, for a write, the OS lock) for the
// life of the transaction.
func newSnapshotTransaction(base *snapState, readOnly bool) *transactionStore {
	return &transactionStore{
		active:   true,
		readOnly: readOnly,
		base:     base,
		actions:  make(map[modulecore.ActionID]domainaction.Action),
		attempts: make(map[modulecore.AttemptID]domainaction.Attempt),
	}
}

// lookupAction finds an Action as this transaction sees it: its own write first,
// then the snapshot. dataMu must be held.
func (tx *transactionStore) lookupAction(id modulecore.ActionID) (domainaction.Action, bool) {
	if item, ok := tx.actions[id]; ok {
		return item, true
	}
	if tx.base != nil {
		return tx.base.action(id)
	}
	return domainaction.Action{}, false
}

// lookupAttempt is lookupAction for Attempts.
func (tx *transactionStore) lookupAttempt(id modulecore.AttemptID) (domainaction.Attempt, bool) {
	if item, ok := tx.attempts[id]; ok {
		return item, true
	}
	if tx.base != nil {
		return tx.base.attempt(id)
	}
	return domainaction.Attempt{}, false
}

func (tx *transactionStore) validateSnapshot() error {
	return tx.withActive(context.Background(), false, func() error {
		return tx.validateSnapshotLocked()
	})
}

// validateSnapshotLocked validates the overlay while dataMu is held. The
// finalizer calls this under the exclusive lifecycle lock so no operation can
// append to the pending overlay after validation or payload extraction.
func (tx *transactionStore) validateSnapshotLocked() error {
	if tx.base != nil {
		return tx.validateTouchedLocked()
	}
	actions := make([]domainaction.Action, 0, len(tx.actions))
	for _, item := range tx.actions {
		actions = append(actions, item)
	}
	attempts := make([]domainaction.Attempt, 0, len(tx.attempts))
	for _, item := range tx.attempts {
		attempts = append(attempts, item)
	}
	return validateSnapshot(actions, attempts)
}

// finalize closes the transaction and takes its final snapshot as one
// lifecycle transition. Operations that already hold the lifecycle read lock
// finish first and are included; operations starting afterward observe expiry.
func (tx *transactionStore) finalize() (map[string][]byte, error) {
	tx.lifecycle.Lock()
	defer tx.lifecycle.Unlock()
	if !tx.active {
		return nil, errExpiredTransaction
	}
	tx.active = false
	tx.dataMu.Lock()
	defer tx.dataMu.Unlock()
	if err := tx.validateSnapshotLocked(); err != nil {
		return nil, err
	}
	return tx.pendingPayloadsLocked()
}

func (tx *transactionStore) Transaction(ctx context.Context, callback func(domainaction.Store) error) error {
	if callback == nil {
		return fmt.Errorf("action transaction callback is nil")
	}
	return tx.withCallback(ctx, true, func() error { return callback(tx) })
}

func (tx *transactionStore) ReadTransaction(ctx context.Context, callback func(domainaction.Store) error) error {
	if callback == nil {
		return fmt.Errorf("action read transaction callback is nil")
	}
	return tx.withCallback(ctx, false, func() error {
		return callback(&readOnlyTransactionStore{tx: tx})
	})
}

func (tx *transactionStore) SaveAction(ctx context.Context, value domainaction.Action) error {
	return tx.withActive(ctx, true, func() error {
		if err := value.Validate(); err != nil {
			return err
		}
		if existing, ok := tx.lookupAction(value.ActionID); ok {
			if existing.TaskID != value.TaskID || existing.RunID != value.RunID || existing.Kind != value.Kind || !existing.CreatedAt.Equal(value.CreatedAt) {
				return fmt.Errorf("action identity fields are immutable")
			}
			if isNativeDelegationAction(existing) {
				if value.Name != existing.Name || value.CurrentAttemptID != existing.CurrentAttemptID {
					return fmt.Errorf("native delegation name and current attempt are immutable")
				}
				if !existing.IsOpen() && value.Status != existing.Status {
					return fmt.Errorf("terminal native delegation status is immutable")
				}
			}
		}
		value = cloneAction(value)
		tx.actions[value.ActionID] = value
		tx.pendingActions = append(tx.pendingActions, value)
		return nil
	})
}

func (tx *transactionStore) GetAction(ctx context.Context, actionID modulecore.ActionID) (domainaction.Action, error) {
	var result domainaction.Action
	err := tx.withActive(ctx, false, func() error {
		if err := actionID.Validate(); err != nil {
			return err
		}
		item, ok := tx.lookupAction(actionID)
		if !ok {
			return domainaction.ErrNotFound
		}
		result = cloneAction(item)
		return nil
	})
	return result, err
}

func (tx *transactionStore) ListActions(ctx context.Context, filter domainaction.Filter) ([]domainaction.Action, error) {
	var result []domainaction.Action
	err := tx.withActive(ctx, false, func() error {
		if err := validateActionFilter(filter); err != nil {
			return err
		}
		result = tx.selectActions(filter)
		sort.SliceStable(result, func(i, j int) bool {
			if result[i].CreatedAt.Equal(result[j].CreatedAt) {
				return string(result[i].ActionID) < string(result[j].ActionID)
			}
			return result[i].CreatedAt.Before(result[j].CreatedAt)
		})
		if filter.Limit > 0 && len(result) > filter.Limit {
			result = result[:filter.Limit]
		}
		return nil
	})
	return result, err
}

func (tx *transactionStore) SaveAttempt(ctx context.Context, value domainaction.Attempt) error {
	return tx.withActive(ctx, true, func() error {
		if err := value.Validate(); err != nil {
			return err
		}
		action, ok := tx.lookupAction(value.ActionID)
		if !ok {
			return fmt.Errorf("attempt action is unavailable: %w", domainaction.ErrNotFound)
		}
		if value.NativeDelegation != nil && !isNativeDelegationAction(action) {
			return fmt.Errorf("typed native delegation state requires its canonical action")
		}
		existing, exists := tx.lookupAttempt(value.AttemptID)
		if exists {
			if existing.ActionID != value.ActionID || existing.StartReason != value.StartReason || !existing.StartedAt.Equal(value.StartedAt) {
				return fmt.Errorf("attempt identity and start fields are immutable")
			}
			if err := validateAttemptRecordTransition(existing, value); err != nil {
				return err
			}
		} else if isNativeDelegationAction(action) {
			if err := validateInitialNativeAttempt(action, value); err != nil {
				return err
			}
		} else if value.NativeDelegation != nil {
			return fmt.Errorf("typed native delegation state requires its canonical action")
		}
		if value.Status == domainaction.AttemptStatusRunning {
			for _, item := range tx.attemptsOfAction(value.ActionID) {
				if item.ActionID == value.ActionID && item.Status == domainaction.AttemptStatusRunning && item.AttemptID != value.AttemptID {
					return fmt.Errorf("action already has active attempt %s", item.AttemptID)
				}
			}
		}
		value = cloneAttempt(value)
		tx.attempts[value.AttemptID] = value
		tx.pendingAttempts = append(tx.pendingAttempts, value)
		return nil
	})
}

func (tx *transactionStore) GetAttempt(ctx context.Context, attemptID modulecore.AttemptID) (domainaction.Attempt, error) {
	var result domainaction.Attempt
	err := tx.withActive(ctx, false, func() error {
		if err := attemptID.Validate(); err != nil {
			return err
		}
		item, ok := tx.lookupAttempt(attemptID)
		if !ok {
			return domainaction.ErrNotFound
		}
		result = cloneAttempt(item)
		return nil
	})
	return result, err
}

func (tx *transactionStore) ListAttempts(ctx context.Context, filter domainaction.AttemptFilter) ([]domainaction.Attempt, error) {
	var result []domainaction.Attempt
	err := tx.withActive(ctx, false, func() error {
		if err := validateAttemptFilter(filter); err != nil {
			return err
		}
		result = tx.selectAttempts(filter)
		sort.SliceStable(result, func(i, j int) bool {
			if result[i].StartedAt.Equal(result[j].StartedAt) {
				return string(result[i].AttemptID) < string(result[j].AttemptID)
			}
			return result[i].StartedAt.Before(result[j].StartedAt)
		})
		if filter.Limit > 0 && len(result) > filter.Limit {
			result = result[:filter.Limit]
		}
		return nil
	})
	return result, err
}

func (tx *transactionStore) pendingPayloads() (map[string][]byte, error) {
	var payloads map[string][]byte
	err := tx.withActive(context.Background(), false, func() error {
		var err error
		payloads, err = tx.pendingPayloadsLocked()
		return err
	})
	return payloads, err
}

// pendingPayloadsLocked encodes the complete pending overlay while dataMu is
// held. It must not acquire lifecycle or data locks itself.
func (tx *transactionStore) pendingPayloadsLocked() (map[string][]byte, error) {
	var payloads map[string][]byte
	var err error
	if len(tx.pendingActions) > 0 {
		payloads = make(map[string][]byte, 2)
		payloads[actionFilename], err = marshalJSONLLines(tx.pendingActions)
		if err != nil {
			return nil, err
		}
	}
	if len(tx.pendingAttempts) > 0 {
		if payloads == nil {
			payloads = make(map[string][]byte, 2)
		}
		payloads[attemptFilename], err = marshalJSONLLines(tx.pendingAttempts)
		if err != nil {
			return nil, err
		}
	}
	return payloads, nil
}

func (tx *transactionStore) withActive(ctx context.Context, write bool, callback func() error) error {
	if callback == nil {
		return fmt.Errorf("action transaction operation is nil")
	}
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tx.lifecycle.RLock()
	defer tx.lifecycle.RUnlock()
	if !tx.active {
		return errExpiredTransaction
	}
	if write && tx.readOnly {
		return errReadOnlyTransaction
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tx.dataMu.Lock()
	defer tx.dataMu.Unlock()
	return callback()
}

func (tx *transactionStore) withCallback(ctx context.Context, write bool, callback func() error) error {
	if callback == nil {
		return fmt.Errorf("action transaction callback is nil")
	}
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tx.lifecycle.RLock()
	if !tx.active {
		tx.lifecycle.RUnlock()
		return errExpiredTransaction
	}
	if write && tx.readOnly {
		tx.lifecycle.RUnlock()
		return errReadOnlyTransaction
	}
	if err := ctx.Err(); err != nil {
		tx.lifecycle.RUnlock()
		return err
	}
	tx.lifecycle.RUnlock()
	return callback()
}

func (tx *transactionStore) invalidate() {
	tx.lifecycle.Lock()
	tx.active = false
	tx.lifecycle.Unlock()
}

type readOnlyTransactionStore struct {
	tx *transactionStore
}

func (tx *readOnlyTransactionStore) Transaction(ctx context.Context, _ func(domainaction.Store) error) error {
	return tx.tx.withActive(ctx, true, func() error { return errReadOnlyTransaction })
}

func (tx *readOnlyTransactionStore) ReadTransaction(ctx context.Context, callback func(domainaction.Store) error) error {
	if callback == nil {
		return fmt.Errorf("action read transaction callback is nil")
	}
	return tx.tx.withCallback(ctx, false, func() error { return callback(tx) })
}

func (tx *readOnlyTransactionStore) SaveAction(ctx context.Context, _ domainaction.Action) error {
	return tx.tx.withActive(ctx, true, func() error { return errReadOnlyTransaction })
}

func (tx *readOnlyTransactionStore) GetAction(ctx context.Context, actionID modulecore.ActionID) (domainaction.Action, error) {
	return tx.tx.GetAction(ctx, actionID)
}

func (tx *readOnlyTransactionStore) ListActions(ctx context.Context, filter domainaction.Filter) ([]domainaction.Action, error) {
	return tx.tx.ListActions(ctx, filter)
}

func (tx *readOnlyTransactionStore) SaveAttempt(ctx context.Context, _ domainaction.Attempt) error {
	return tx.tx.withActive(ctx, true, func() error { return errReadOnlyTransaction })
}

func (tx *readOnlyTransactionStore) GetAttempt(ctx context.Context, attemptID modulecore.AttemptID) (domainaction.Attempt, error) {
	return tx.tx.GetAttempt(ctx, attemptID)
}

func (tx *readOnlyTransactionStore) ListAttempts(ctx context.Context, filter domainaction.AttemptFilter) ([]domainaction.Attempt, error) {
	return tx.tx.ListAttempts(ctx, filter)
}

func validateStoreContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	return ctx.Err()
}

func validateActionFilter(filter domainaction.Filter) error {
	if filter.TaskID != "" {
		if err := filter.TaskID.Validate(); err != nil {
			return fmt.Errorf("task_id is invalid: %w", err)
		}
	}
	if filter.RunID != "" {
		if err := filter.RunID.Validate(); err != nil {
			return fmt.Errorf("run_id is invalid: %w", err)
		}
	}
	if filter.Status != "" && !domainaction.ValidStatus(filter.Status) {
		return fmt.Errorf("invalid action status: %s", filter.Status)
	}
	if filter.Kind != "" && !domainaction.ValidKind(filter.Kind) {
		return fmt.Errorf("invalid action kind: %s", filter.Kind)
	}
	return nil
}

func validateAttemptFilter(filter domainaction.AttemptFilter) error {
	if filter.ActionID != "" {
		if err := filter.ActionID.Validate(); err != nil {
			return fmt.Errorf("action_id is invalid: %w", err)
		}
	}
	if filter.Status != "" && !domainaction.ValidAttemptStatus(filter.Status) {
		return fmt.Errorf("invalid attempt status: %s", filter.Status)
	}
	return nil
}

func validateSnapshot(actions []domainaction.Action, attempts []domainaction.Attempt) error {
	actionByID := make(map[modulecore.ActionID]domainaction.Action, len(actions))
	nativeByTaskRun := make(map[[2]string]modulecore.ActionID)
	for index, item := range actions {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("action snapshot record %d is invalid: %w", index, err)
		}
		if isNativeDelegationAction(item) {
			key := [2]string{string(item.TaskID), string(item.RunID)}
			if previous, ok := nativeByTaskRun[key]; ok && previous != item.ActionID {
				return fmt.Errorf("Task Run %s/%s has multiple native delegation actions", item.TaskID, item.RunID)
			}
			nativeByTaskRun[key] = item.ActionID
		}
		actionByID[item.ActionID] = item
	}
	attemptByID := make(map[modulecore.AttemptID]domainaction.Attempt, len(attempts))
	activeByAction := make(map[modulecore.ActionID]modulecore.AttemptID)
	attemptCountByAction := make(map[modulecore.ActionID]int)
	typedNativeByAction := make(map[modulecore.ActionID]bool)
	for index, item := range attempts {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("attempt snapshot record %d is invalid: %w", index, err)
		}
		if _, ok := actionByID[item.ActionID]; !ok {
			return fmt.Errorf("attempt %s refers to unavailable action %s", item.AttemptID, item.ActionID)
		}
		if item.NativeDelegation != nil {
			if !isNativeDelegationAction(actionByID[item.ActionID]) {
				return fmt.Errorf("typed native delegation state belongs to non-native action %s", item.ActionID)
			}
			typedNativeByAction[item.ActionID] = true
		}
		attemptCountByAction[item.ActionID]++
		if item.Status == domainaction.AttemptStatusRunning {
			if activeID, ok := activeByAction[item.ActionID]; ok && activeID != item.AttemptID {
				return fmt.Errorf("action %s has multiple active attempts", item.ActionID)
			}
			activeByAction[item.ActionID] = item.AttemptID
		}
		attemptByID[item.AttemptID] = item
	}
	for _, action := range actions {
		if action.CurrentAttemptID == "" {
			if typedNativeByAction[action.ActionID] {
				return fmt.Errorf("typed native delegation %s has no current attempt", action.ActionID)
			}
			continue
		}
		attempt, ok := attemptByID[action.CurrentAttemptID]
		if !ok {
			return fmt.Errorf("action %s refers to unavailable current attempt %s", action.ActionID, action.CurrentAttemptID)
		}
		if attempt.ActionID != action.ActionID {
			return fmt.Errorf("action %s current attempt %s belongs to action %s", action.ActionID, action.CurrentAttemptID, attempt.ActionID)
		}
		if attempt.NativeDelegation != nil {
			wantReason := domainaction.AttemptStartReasonFirst
			if attempt.NativeDelegation.EffectiveMode() == domainaction.NativeDelegationModeResume {
				wantReason = domainaction.AttemptStartReasonExplicitResume
			}
			if !isNativeDelegationAction(action) || attempt.StartReason != wantReason || attemptCountByAction[action.ActionID] != 1 {
				return fmt.Errorf("typed native delegation %s must own exactly one initial attempt for its mode", action.ActionID)
			}
			if len(attempt.NativeDelegation.RunResult) == 0 {
				stillUnknown := action.IsOpen() && attempt.IsActive() && !nativeHasDefiniteRejection(attempt.NativeDelegation)
				rejected := action.Status == domainaction.StatusFailed && attempt.Status == domainaction.AttemptStatusFailed && nativeHasDefiniteRejection(attempt.NativeDelegation)
				if !stillUnknown && !rejected {
					return fmt.Errorf("native delegation without a run result must remain open/running or be terminally rejected")
				}
			} else if action.IsOpen() || attempt.IsActive() {
				return fmt.Errorf("native delegation result and terminal state must be committed together")
			} else if attempt.Status == domainaction.AttemptStatusSucceeded && attempt.NativeDelegation.Proof == nil {
				return fmt.Errorf("successful native delegation requires a durable criteria proof")
			} else if attempt.NativeDelegation.Proof != nil && attempt.Status != domainaction.AttemptStatusSucceeded {
				return fmt.Errorf("native criteria proof requires a successful Action and Attempt")
			}
		}
		if !action.IsOpen() && attempt.IsActive() {
			return fmt.Errorf("terminal action %s current attempt %s is still running", action.ActionID, action.CurrentAttemptID)
		}
	}
	for actionID, attemptID := range activeByAction {
		action, ok := actionByID[actionID]
		if !ok {
			return fmt.Errorf("active attempt %s refers to unavailable action %s", attemptID, actionID)
		}
		if !action.IsOpen() || action.CurrentAttemptID != attemptID {
			return fmt.Errorf("active attempt %s is not current for action %s", attemptID, actionID)
		}
	}
	return nil
}

func (s *JSONLStore) loadActions(ctx context.Context) ([]domainaction.Action, error) {
	return loadActionsFrom(ctx, s.actionPath)
}

func (s *JSONLStore) loadAttempts(ctx context.Context) ([]domainaction.Attempt, error) {
	return loadAttemptsFrom(ctx, s.attemptPath)
}

// loadActionsFrom is the full read of an Action log: every line decoded and held
// to the rules of the fold, the latest version of each Action returned.
func loadActionsFrom(ctx context.Context, path string) ([]domainaction.Action, error) {
	items, err := readJSONLLines[domainaction.Action](ctx, path)
	if err != nil {
		return nil, err
	}
	fold := newActionFold(len(items))
	for index, item := range items {
		if err := fold.apply(index, item); err != nil {
			return nil, err
		}
	}
	result := make([]domainaction.Action, 0, len(fold.latest))
	for _, item := range fold.latest {
		result = append(result, item)
	}
	return result, nil
}

// loadAttemptsFrom is loadActionsFrom for an Attempt log.
func loadAttemptsFrom(ctx context.Context, path string) ([]domainaction.Attempt, error) {
	items, err := readJSONLLines[domainaction.Attempt](ctx, path)
	if err != nil {
		return nil, err
	}
	fold := newAttemptFold(len(items))
	for index, item := range items {
		if err := fold.apply(index, item); err != nil {
			return nil, err
		}
	}
	result := make([]domainaction.Attempt, 0, len(fold.latest))
	for _, item := range fold.latest {
		result = append(result, item)
	}
	return result, nil
}

func validateInitialNativeAttempt(action domainaction.Action, attempt domainaction.Attempt) error {
	if !isNativeDelegationAction(action) || action.Name != domainaction.NativeDelegationActionName {
		return fmt.Errorf("initial native attempt requires its canonical Action")
	}
	if action.Status != domainaction.StatusOpen || action.CurrentAttemptID != attempt.AttemptID {
		return fmt.Errorf("initial native attempt must be the current attempt of an open Action")
	}
	if attempt.NativeDelegation == nil || !domaintask.ValidCriteriaRevision(attempt.NativeDelegation.ExpectedCriteriaRevision) {
		return fmt.Errorf("new native attempt requires a valid frozen criteria revision")
	}
	return validateInitialNativeAttemptShape(attempt)
}

func validateInitialNativeAttemptShape(attempt domainaction.Attempt) error {
	if attempt.NativeDelegation == nil {
		return fmt.Errorf("initial native attempt requires typed native state")
	}
	wantReason := domainaction.AttemptStartReasonFirst
	if attempt.NativeDelegation.EffectiveMode() == domainaction.NativeDelegationModeResume {
		wantReason = domainaction.AttemptStartReasonExplicitResume
	}
	if attempt.StartReason != wantReason || attempt.Status != domainaction.AttemptStatusRunning || attempt.CompletedAt != nil || attempt.Summary != "" {
		return fmt.Errorf("initial native attempt must be running with a mode-matching start reason")
	}
	state := attempt.NativeDelegation
	if state.Open != nil || state.Start != nil || state.Resume != nil || len(state.RunResult) != 0 || state.Proof != nil || state.LastObservation != nil {
		return fmt.Errorf("initial native attempt may not contain mutation outcomes or terminal data")
	}
	return nil
}

func marshalJSONLLines[T any](items []T) ([]byte, error) {
	var builder strings.Builder
	for _, item := range items {
		data, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		builder.Write(data)
		builder.WriteByte('\n')
	}
	return []byte(builder.String()), nil
}

func cloneAction(value domainaction.Action) domainaction.Action {
	if value.CompletedAt != nil {
		completedAt := *value.CompletedAt
		value.CompletedAt = &completedAt
	}
	return value
}

func cloneAttempt(value domainaction.Attempt) domainaction.Attempt {
	return value.Clone()
}

func validateAttemptRecordTransition(previous, next domainaction.Attempt) error {
	if previous.NativeDelegation == nil {
		if next.NativeDelegation != nil {
			return fmt.Errorf("typed native delegation state cannot be attached after attempt creation")
		}
		return nil
	}
	if next.NativeDelegation == nil {
		return fmt.Errorf("native delegation state is immutable and cannot be removed")
	}
	oldState := previous.NativeDelegation
	newState := next.NativeDelegation
	if oldState.Mode != newState.Mode || !sameNativeDelegationSource(oldState.ResumeSource, newState.ResumeSource) {
		return fmt.Errorf("native delegation mode and Resume source are immutable")
	}
	if oldState.ExpectedCriteriaRevision != newState.ExpectedCriteriaRevision {
		return fmt.Errorf("native delegation expected criteria revision is immutable")
	}
	if !sameNativeReference(oldState.InputReference, newState.InputReference) {
		return fmt.Errorf("native delegation input reference is immutable")
	}
	if err := validateMutationTransition(oldState.Open, newState.Open); err != nil {
		return fmt.Errorf("open mutation transition is invalid: %w", err)
	}
	if err := validateMutationTransition(oldState.Start, newState.Start); err != nil {
		return fmt.Errorf("start mutation transition is invalid: %w", err)
	}
	if err := validateMutationTransition(oldState.Resume, newState.Resume); err != nil {
		return fmt.Errorf("Resume mutation transition is invalid: %w", err)
	}
	if len(oldState.RunResult) != 0 && !bytes.Equal(oldState.RunResult, newState.RunResult) {
		return fmt.Errorf("native run result is immutable")
	}
	if oldState.Proof != nil && !sameNativeDelegationProof(oldState.Proof, newState.Proof) {
		return fmt.Errorf("native criteria proof is immutable")
	}
	if oldState.Proof == nil && newState.Proof != nil &&
		(previous.Status != domainaction.AttemptStatusRunning || next.Status != domainaction.AttemptStatusSucceeded) {
		return fmt.Errorf("native criteria proof can be attached only with successful terminal state")
	}
	if previous.Status != domainaction.AttemptStatusRunning {
		if next.Status != previous.Status || !sameTimePtr(previous.CompletedAt, next.CompletedAt) || previous.Summary != next.Summary {
			return fmt.Errorf("terminal native attempt state is immutable")
		}
	}
	if len(newState.RunResult) != 0 && (next.Status == domainaction.AttemptStatusRunning || previous.Status != domainaction.AttemptStatusRunning) {
		return fmt.Errorf("native run result must be recorded with terminal attempt state")
	}
	if next.Status == domainaction.AttemptStatusRunning && nativeHasDefiniteRejection(newState) {
		return fmt.Errorf("definite native rejection must terminalize the Action and Attempt")
	}
	if next.Status != domainaction.AttemptStatusRunning && len(newState.RunResult) == 0 &&
		(next.Status != domainaction.AttemptStatusFailed || !nativeHasDefiniteRejection(newState)) {
		return fmt.Errorf("native attempt without a RunResult can terminate only after a definite rejection")
	}
	return nil
}

func nativeHasDefiniteRejection(state *domainaction.NativeDelegation) bool {
	if state == nil {
		return false
	}
	return state.Open != nil && state.Open.Status == domainaction.NativeMutationStatusRejected ||
		state.Start != nil && state.Start.Status == domainaction.NativeMutationStatusRejected ||
		state.Resume != nil && state.Resume.Status == domainaction.NativeMutationStatusRejected
}

func sameNativeDelegationSource(left, right *domainaction.NativeDelegationResumeSource) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.ActionID == right.ActionID && left.AttemptID == right.AttemptID && left.HarnessTaskID == right.HarnessTaskID &&
		left.ThreadID == right.ThreadID && left.RunID == right.RunID && left.ReceiptID == right.ReceiptID &&
		left.PreviousRunID == right.PreviousRunID && left.RunResultSHA256 == right.RunResultSHA256 &&
		(left.CheckpointID == nil && right.CheckpointID == nil || left.CheckpointID != nil && right.CheckpointID != nil && *left.CheckpointID == *right.CheckpointID)
}

func sameNativeReference(left, right *conversation.AcceptedOPSInputReference) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameNativeDelegationProof(left, right *domainaction.NativeDelegationProof) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if left.Version != right.Version || left.ExpectedCriteriaRevision != right.ExpectedCriteriaRevision ||
		left.RunResultSHA256 != right.RunResultSHA256 || left.TerminalEventID != right.TerminalEventID ||
		left.TerminalEventSeq != right.TerminalEventSeq || len(left.Evidence) != len(right.Evidence) {
		return false
	}
	for index := range left.Evidence {
		if left.Evidence[index] != right.Evidence[index] {
			return false
		}
	}
	return true
}

func validateMutationTransition(previous, next *domainaction.NativeMutation) error {
	if previous == nil {
		if next == nil {
			return nil
		}
		if next.Status != domainaction.NativeMutationStatusPrepared {
			return fmt.Errorf("new mutation must start prepared")
		}
		return nil
	}
	if next == nil {
		return fmt.Errorf("prepared mutation cannot be removed")
	}
	if previous.Key != next.Key || previous.PayloadSHA256 != next.PayloadSHA256 || !bytes.Equal(previous.Payload, next.Payload) {
		return fmt.Errorf("mutation key and exact payload are immutable")
	}
	if len(previous.Result) != 0 || previous.ErrorCode != "" {
		if previous.Status != next.Status || !bytes.Equal(previous.Result, next.Result) || previous.ErrorCode != next.ErrorCode {
			return fmt.Errorf("definite mutation outcome is immutable")
		}
	}
	allowed := previous.Status == next.Status ||
		previous.Status == domainaction.NativeMutationStatusPrepared && next.Status == domainaction.NativeMutationStatusDeliveryUnknown ||
		previous.Status == domainaction.NativeMutationStatusDeliveryUnknown && (next.Status == domainaction.NativeMutationStatusAccepted || next.Status == domainaction.NativeMutationStatusRejected)
	if !allowed {
		return fmt.Errorf("mutation status cannot move from %s to %s", previous.Status, next.Status)
	}
	return nil
}

func sameTimePtr(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func isNativeDelegationAction(value domainaction.Action) bool {
	return value.Kind == domainaction.KindDelegation && domainaction.NormalizeName(value.Name) == domainaction.NativeDelegationActionName
}

// readJSONLLines decodes every non-blank line of the file at path. A missing
// file holds no lines.
func readJSONLLines[T any](ctx context.Context, path string) ([]T, error) {
	var items []T
	_, _, err := scanJSONLFile(ctx, path, func(line []byte) error {
		var item T
		if unmarshalErr := json.Unmarshal(line, &item); unmarshalErr != nil {
			return unmarshalErr
		}
		items = append(items, item)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// scanJSONLFile calls fn with every non-blank line of the file at path, trimmed
// of surrounding space; fn must not keep the slice. It returns how many bytes it
// read and whether the last line ended with a newline (a file that ends without
// one was not written by the append path). A missing file holds no lines. A
// record over jsonlbatch.MaxJSONLRecordBytes (its line ending excluded) fails the
// scan, as does any error fn returns.
func scanJSONLFile(ctx context.Context, path string, fn func(line []byte) error) (int64, bool, error) {
	if err := validateStoreContext(ctx); err != nil {
		return 0, false, err
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, true, nil
		}
		return 0, false, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, jsonlbatch.MaxJSONLReadBufferSize)
	var read int64
	terminated := true
	for {
		if err := ctx.Err(); err != nil {
			return read, terminated, err
		}
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			return read, terminated, fmt.Errorf("jsonl record exceeds maximum allowed %d bytes", jsonlbatch.MaxJSONLRecordBytes)
		}
		if len(line) > 0 {
			read += int64(len(line))
			recordBytes := len(line)
			terminated = line[len(line)-1] == '\n'
			if terminated {
				recordBytes--
			}
			if recordBytes > jsonlbatch.MaxJSONLRecordBytes {
				return read, terminated, fmt.Errorf("jsonl record exceeds maximum allowed %d bytes", jsonlbatch.MaxJSONLRecordBytes)
			}
			if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
				if fnErr := fn(trimmed); fnErr != nil {
					return read, terminated, fnErr
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return read, terminated, err
		}
	}
	return read, terminated, nil
}
