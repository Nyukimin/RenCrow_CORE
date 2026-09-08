package action

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

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
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
}

// NewJSONLStore opens the Action files, repairs a pending WAL as a writer, and
// rejects malformed persisted state before returning a usable store.
func NewJSONLStore(root string) (*JSONLStore, error) {
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
	if err := batch.Read(context.Background(), func() error {
		if _, err := store.loadActions(context.Background()); err != nil {
			return fmt.Errorf("validate action state: %w", err)
		}
		if _, err := store.loadAttempts(context.Background()); err != nil {
			return fmt.Errorf("validate attempt state: %w", err)
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
	if err := validateStoreContext(ctx); err != nil {
		return err
	}
	if callback == nil {
		return fmt.Errorf("action transaction callback is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.batch.Write(ctx, func() (map[string][]byte, error) {
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

// ReadTransaction runs against one OS-locked, committed snapshot. Its
// callback receives a read-only transaction store and cannot write state.
func (s *JSONLStore) ReadTransaction(ctx context.Context, callback func(domainaction.Store) error) error {
	if err := validateStoreContext(ctx); err != nil {
		return err
	}
	if callback == nil {
		return fmt.Errorf("action read transaction callback is nil")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.batch.Read(ctx, func() error {
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

func (s *JSONLStore) SaveAction(ctx context.Context, value domainaction.Action) error {
	return s.Transaction(ctx, func(tx domainaction.Store) error {
		return tx.SaveAction(ctx, value)
	})
}

func (s *JSONLStore) GetAction(ctx context.Context, actionID modulecore.ActionID) (domainaction.Action, error) {
	if err := actionID.Validate(); err != nil {
		return domainaction.Action{}, err
	}
	var result domainaction.Action
	err := s.ReadTransaction(ctx, func(tx domainaction.Store) error {
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
	err := s.ReadTransaction(ctx, func(tx domainaction.Store) error {
		var err error
		result, err = tx.ListActions(ctx, filter)
		return err
	})
	return result, err
}

func (s *JSONLStore) SaveAttempt(ctx context.Context, value domainaction.Attempt) error {
	return s.Transaction(ctx, func(tx domainaction.Store) error {
		return tx.SaveAttempt(ctx, value)
	})
}

func (s *JSONLStore) GetAttempt(ctx context.Context, attemptID modulecore.AttemptID) (domainaction.Attempt, error) {
	if err := attemptID.Validate(); err != nil {
		return domainaction.Attempt{}, err
	}
	var result domainaction.Attempt
	err := s.ReadTransaction(ctx, func(tx domainaction.Store) error {
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
	err := s.ReadTransaction(ctx, func(tx domainaction.Store) error {
		var err error
		result, err = tx.ListAttempts(ctx, filter)
		return err
	})
	return result, err
}

type transactionStore struct {
	lifecycle       sync.RWMutex
	dataMu          sync.Mutex
	active          bool
	readOnly        bool
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

func (tx *transactionStore) validateSnapshot() error {
	return tx.withActive(context.Background(), false, func() error {
		return tx.validateSnapshotLocked()
	})
}

// validateSnapshotLocked validates the overlay while dataMu is held. The
// finalizer calls this under the exclusive lifecycle lock so no operation can
// append to the pending overlay after validation or payload extraction.
func (tx *transactionStore) validateSnapshotLocked() error {
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
		if existing, ok := tx.actions[value.ActionID]; ok {
			if existing.TaskID != value.TaskID || existing.RunID != value.RunID || existing.Kind != value.Kind || !existing.CreatedAt.Equal(value.CreatedAt) {
				return fmt.Errorf("action identity fields are immutable")
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
		item, ok := tx.actions[actionID]
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
		result = make([]domainaction.Action, 0, len(tx.actions))
		for _, item := range tx.actions {
			if filter.TaskID != "" && item.TaskID != filter.TaskID {
				continue
			}
			if filter.RunID != "" && item.RunID != filter.RunID {
				continue
			}
			if filter.Status != "" && item.Status != filter.Status {
				continue
			}
			result = append(result, cloneAction(item))
		}
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
		if _, ok := tx.actions[value.ActionID]; !ok {
			return fmt.Errorf("attempt action is unavailable: %w", domainaction.ErrNotFound)
		}
		if existing, ok := tx.attempts[value.AttemptID]; ok {
			if existing.ActionID != value.ActionID || existing.StartReason != value.StartReason || !existing.StartedAt.Equal(value.StartedAt) {
				return fmt.Errorf("attempt identity and start fields are immutable")
			}
		}
		if value.Status == domainaction.AttemptStatusRunning {
			for _, item := range tx.attempts {
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
		item, ok := tx.attempts[attemptID]
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
		result = make([]domainaction.Attempt, 0, len(tx.attempts))
		for _, item := range tx.attempts {
			if filter.ActionID != "" && item.ActionID != filter.ActionID {
				continue
			}
			if filter.Status != "" && item.Status != filter.Status {
				continue
			}
			result = append(result, cloneAttempt(item))
		}
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
	for index, item := range actions {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("action snapshot record %d is invalid: %w", index, err)
		}
		actionByID[item.ActionID] = item
	}
	attemptByID := make(map[modulecore.AttemptID]domainaction.Attempt, len(attempts))
	activeByAction := make(map[modulecore.ActionID]modulecore.AttemptID)
	for index, item := range attempts {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("attempt snapshot record %d is invalid: %w", index, err)
		}
		if _, ok := actionByID[item.ActionID]; !ok {
			return fmt.Errorf("attempt %s refers to unavailable action %s", item.AttemptID, item.ActionID)
		}
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
			continue
		}
		attempt, ok := attemptByID[action.CurrentAttemptID]
		if !ok {
			return fmt.Errorf("action %s refers to unavailable current attempt %s", action.ActionID, action.CurrentAttemptID)
		}
		if attempt.ActionID != action.ActionID {
			return fmt.Errorf("action %s current attempt %s belongs to action %s", action.ActionID, action.CurrentAttemptID, attempt.ActionID)
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
	if value.CompletedAt != nil {
		completedAt := *value.CompletedAt
		value.CompletedAt = &completedAt
	}
	return value
}

func readJSONLLines[T any](ctx context.Context, path string) ([]T, error) {
	if err := validateStoreContext(ctx); err != nil {
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
