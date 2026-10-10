package task

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	taskOperationReceiptFilename = "task_operation_receipts.jsonl"
	taskExecutionFenceFilename   = "task_execution_fences.jsonl"

	maxTaskOperationIDBytes     = 128
	maxTaskOperationResultBytes = 64 * 1024
	maxTaskExecutionFenceID     = 128
)

var (
	ErrTaskOperationConflict              = errors.New("task operation id conflicts with a stored receipt")
	ErrTaskOperationNotFound              = errors.New("task operation receipt not found")
	ErrTaskOperationReceiptCorrupt        = errors.New("task operation receipt log is corrupt")
	ErrTaskExecutionFenceActive           = errors.New("task has an active execution fence")
	ErrTaskExecutionFenceMismatch         = errors.New("task execution fence capability mismatch")
	ErrTaskExecutionFenceNotFound         = errors.New("task execution fence record not found")
	ErrTaskExecutionFenceCorrupt          = errors.New("task execution fence log is corrupt")
	ErrTaskExecutionFenceRecoveryRequired = errors.New("task execution fence requires recovery")
)

// TaskOperationScope distinguishes one Task-scoped receipt from a global Task
// store transaction receipt. An empty scope is accepted only when reading
// legacy task-scoped records written before the discriminator was introduced.
type TaskOperationScope string

const (
	TaskOperationScopeTask   TaskOperationScope = "task"
	TaskOperationScopeGlobal TaskOperationScope = "global"
)

// TaskOperationReceipt is the shared durable result schema for task-scoped and
// global Task owner operations.
type TaskOperationReceipt struct {
	OperationID      string             `json:"operation_id"`
	RequestHash      string             `json:"request_hash"`
	Scope            TaskOperationScope `json:"scope,omitempty"`
	TaskID           modulecore.TaskID  `json:"task_id,omitempty"`
	Result           json.RawMessage    `json:"result"`
	WriterGeneration uint64             `json:"writer_generation"`
}

type TaskExecutionFenceState string

const (
	TaskExecutionFenceStateActive    TaskExecutionFenceState = "active"
	TaskExecutionFenceStateReleased  TaskExecutionFenceState = "released"
	TaskExecutionFenceStateUncertain TaskExecutionFenceState = "uncertain"
)

// TaskExecutionFenceStatus exposes the exact durable capability and the
// writer generations that admitted and released it. A writer restart alone
// never changes State or authorizes reuse.
type TaskExecutionFenceStatus struct {
	TaskID                  modulecore.TaskID       `json:"task_id"`
	FenceID                 string                  `json:"fence_id"`
	State                   TaskExecutionFenceState `json:"state"`
	AcquireWriterGeneration uint64                  `json:"acquire_writer_generation"`
	ReleaseWriterGeneration uint64                  `json:"release_writer_generation,omitempty"`
	RecoveryRequired        bool                    `json:"recovery_required"`
}

type taskExecutionFenceEvent struct {
	Event            string            `json:"event"`
	TaskID           modulecore.TaskID `json:"task_id"`
	FenceID          string            `json:"fence_id"`
	WriterGeneration uint64            `json:"writer_generation"`
}

type persistedTaskFence struct {
	TaskID            modulecore.TaskID
	FenceID           string
	Active            bool
	AcquireGeneration uint64
	ReleaseGeneration uint64
	Local             bool
}

type taskExecutionFenceSnapshot struct {
	byID   map[string]persistedTaskFence
	active map[modulecore.TaskID]string
	latest map[modulecore.TaskID]string
}

// ExecuteIdempotentTaskOperation commits task mutations and a bounded replay
// receipt in the same JSONL batch. The callback is not invoked for a matching
// stored receipt.
func (s *JSONLStore) ExecuteIdempotentTaskOperation(
	ctx context.Context,
	taskID modulecore.TaskID,
	operationID string,
	requestHash string,
	fn func(domaintask.Store) (json.RawMessage, error),
) (json.RawMessage, error) {
	if s == nil {
		return nil, errors.New("task store is nil")
	}
	if ctx == nil {
		return nil, errors.New("task operation context is nil")
	}
	if fn == nil {
		return nil, errors.New("task operation callback is nil")
	}
	if err := taskID.Validate(); err != nil {
		return nil, fmt.Errorf("task operation task_id is invalid: %w", err)
	}
	if err := validateTaskOperationID(operationID); err != nil {
		return nil, err
	}
	if err := validateTaskRequestHash(requestHash); err != nil {
		return nil, err
	}
	if s.readOnly {
		return nil, errors.New("task store is read-only")
	}
	releaseAdmission, err := s.lifecycle.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer releaseAdmission()
	lease, err := s.executionFence.acquire(ctx, taskID)
	if err != nil {
		return nil, err
	}
	defer lease.release()

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, os.ErrClosed
	}
	if s.batch == nil || s.writerLock == nil || s.writerGeneration == 0 {
		return nil, errors.New("task store writer is unavailable")
	}
	var result json.RawMessage
	err = s.writeBatch(withTxLabel(ctx, "ExecuteIdempotentTaskOperation", taskID), func() (map[string][]byte, error) {
		previous, ok, err := s.loadReceipt(ctx, operationID)
		if err != nil {
			return nil, err
		}
		if ok {
			if previous.TaskID != taskID || previous.RequestHash != requestHash {
				return nil, ErrTaskOperationConflict
			}
			result = append(json.RawMessage(nil), previous.Result...)
			return nil, nil
		}
		if err := s.activeTaskExecutionFenceError(taskID); err != nil {
			return nil, err
		}

		tx := newTaskTransaction(s, false, taskID, lease)
		defer tx.end()
		if err := tx.touchTask(ctx, taskID); err != nil {
			return nil, err
		}
		callbackResult, callbackErr := fn(tx)
		snapshot := tx.freezeAndSnapshot()
		if callbackErr != nil {
			return nil, callbackErr
		}
		if err := validateTaskOperationResult(callbackResult); err != nil {
			return nil, err
		}
		if err := validateTaskTransaction(ctx, s, snapshot); err != nil {
			return nil, err
		}
		receipt := TaskOperationReceipt{
			OperationID:      operationID,
			RequestHash:      requestHash,
			Scope:            TaskOperationScopeTask,
			TaskID:           taskID,
			Result:           append(json.RawMessage(nil), callbackResult...),
			WriterGeneration: s.writerGeneration,
		}
		encoded, err := json.Marshal(receipt)
		if err != nil {
			return nil, err
		}
		if len(encoded) > maxTaskOperationResultBytes+1024 {
			return nil, errors.New("task operation receipt exceeds size limit")
		}
		snapshot.payloads[taskOperationReceiptFilename] = append(encoded, '\n')
		result = append(json.RawMessage(nil), callbackResult...)
		return snapshot.payloads, nil
	})
	if err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), result...), nil
}

// LookupTaskOperation returns a stored receipt by its globally stable operation ID.
// It is intended for protocol-journal reconciliation after a storage-host restart.
func (s *JSONLStore) LookupTaskOperation(ctx context.Context, operationID string) (TaskOperationReceipt, error) {
	if s == nil {
		return TaskOperationReceipt{}, errors.New("task store is nil")
	}
	if ctx == nil {
		return TaskOperationReceipt{}, errors.New("task operation lookup context is nil")
	}
	if err := validateTaskOperationID(operationID); err != nil {
		return TaskOperationReceipt{}, err
	}
	releaseAdmission, err := s.lifecycle.admit(ctx)
	if err != nil {
		return TaskOperationReceipt{}, err
	}
	defer releaseAdmission()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return TaskOperationReceipt{}, os.ErrClosed
	}
	if s.batch == nil {
		return TaskOperationReceipt{}, errors.New("task store batch is unavailable")
	}
	if s.idx != nil {
		return s.indexLookupTaskOperation(ctx, operationID)
	}
	var result TaskOperationReceipt
	err = s.batch.Read(withTxLabel(ctx, "LookupTaskOperation", ""), func() error {
		receipts, err := readTaskOperationReceipts(ctx, filepath.Join(s.root, taskOperationReceiptFilename))
		if err != nil {
			return err
		}
		var ok bool
		result, ok = receipts[operationID]
		if !ok {
			return ErrTaskOperationNotFound
		}
		result.Result = append(json.RawMessage(nil), result.Result...)
		return nil
	})
	return result, err
}

// AcquireTaskExecutionFence durably admits one caller-stable capability. A
// retry with the same task and active capability is idempotent; other active
// capabilities are rejected.
func (s *JSONLStore) AcquireTaskExecutionFence(ctx context.Context, taskID modulecore.TaskID, fenceID string) error {
	if s == nil {
		return errors.New("task store is nil")
	}
	if ctx == nil {
		return errors.New("task execution fence context is nil")
	}
	if err := taskID.Validate(); err != nil {
		return err
	}
	if err := validateTaskExecutionFenceID(fenceID); err != nil {
		return err
	}
	if s.readOnly {
		return errors.New("task store is read-only")
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
	return s.persistTaskExecutionFenceAcquire(ctx, taskID, fenceID, false)
}

// ReleaseTaskExecutionFence durably releases the exact task/capability pair.
// A committed release retry is idempotent; a different capability is a mismatch.
func (s *JSONLStore) ReleaseTaskExecutionFence(ctx context.Context, taskID modulecore.TaskID, fenceID string) error {
	if s == nil {
		return errors.New("task store is nil")
	}
	if ctx == nil {
		return errors.New("task execution fence context is nil")
	}
	if err := taskID.Validate(); err != nil {
		return err
	}
	if err := validateTaskExecutionFenceID(fenceID); err != nil {
		return err
	}
	if s.readOnly {
		return errors.New("task store is read-only")
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
	return s.persistTaskExecutionFenceRelease(ctx, taskID, fenceID)
}

// GetTaskExecutionFence returns the latest durable fence record for one Task.
// It reports uncertain when the owner batch has an unresolved durability boundary.
func (s *JSONLStore) GetTaskExecutionFence(ctx context.Context, taskID modulecore.TaskID) (TaskExecutionFenceStatus, error) {
	if s == nil {
		return TaskExecutionFenceStatus{}, errors.New("task store is nil")
	}
	if ctx == nil {
		return TaskExecutionFenceStatus{}, errors.New("task execution fence lookup context is nil")
	}
	if err := taskID.Validate(); err != nil {
		return TaskExecutionFenceStatus{}, err
	}
	releaseAdmission, err := s.lifecycle.admit(ctx)
	if err != nil {
		return TaskExecutionFenceStatus{}, err
	}
	defer releaseAdmission()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return TaskExecutionFenceStatus{}, os.ErrClosed
	}
	if s.batch == nil {
		return TaskExecutionFenceStatus{}, errors.New("task store batch is unavailable")
	}
	var result TaskExecutionFenceStatus
	err = s.batch.Read(withTxLabel(ctx, "GetTaskExecutionFence", taskID), func() error {
		snapshot, err := readTaskExecutionFences(ctx, filepath.Join(s.root, taskExecutionFenceFilename))
		if err != nil {
			return err
		}
		result, err = taskFenceStatusForTask(snapshot, taskID)
		return err
	})
	if err == nil {
		return result, nil
	}
	if !errors.Is(err, jsonlbatch.ErrRecoveryRequired) {
		return TaskExecutionFenceStatus{}, err
	}
	if known, ok := s.knownActiveTaskFenceStatus(taskID); ok {
		known.State = TaskExecutionFenceStateUncertain
		known.RecoveryRequired = true
		return known, errors.Join(ErrTaskExecutionFenceRecoveryRequired, err)
	}
	return TaskExecutionFenceStatus{TaskID: taskID, State: TaskExecutionFenceStateUncertain, RecoveryRequired: true}, errors.Join(ErrTaskExecutionFenceRecoveryRequired, err)
}

func (s *JSONLStore) withDurableTaskExecutionFence(ctx context.Context, taskID modulecore.TaskID, fn func() error) error {
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
		return errors.New("task store is read-only")
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
	fenceID, err := newLocalTaskExecutionFenceID()
	if err != nil {
		return err
	}
	if err := s.persistTaskExecutionFenceAcquire(ctx, taskID, fenceID, true); err != nil {
		return err
	}
	callbackErr := fn()
	releaseErr := s.persistTaskExecutionFenceRelease(context.WithoutCancel(ctx), taskID, fenceID)
	return errors.Join(callbackErr, releaseErr)
}

func newLocalTaskExecutionFenceID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate task execution fence capability: %w", err)
	}
	return "local:" + hex.EncodeToString(bytes[:]), nil
}

func taskBatchReaderFilenames(root string) ([]string, error) {
	filenames := []string{stateFilename, runFilename, contextFilename, notificationsFilename}
	for _, optional := range []string{taskOperationReceiptFilename, taskExecutionFenceFilename} {
		if _, err := os.Lstat(filepath.Join(root, optional)); err == nil {
			filenames = append(filenames, optional)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return filenames, nil
}

func validateTaskOperationID(operationID string) error {
	return validateStableTaskIdentifier("operation_id", operationID, maxTaskOperationIDBytes)
}

func validateTaskExecutionFenceID(fenceID string) error {
	return validateStableTaskIdentifier("fence_id", fenceID, maxTaskExecutionFenceID)
}

func validateStableTaskIdentifier(name, value string, maxBytes int) error {
	if len(value) == 0 || len(value) > maxBytes {
		return fmt.Errorf("%s must contain 1 to %d bytes", name, maxBytes)
	}
	for i := 0; i < len(value); i++ {
		b := value[i]
		alphaNumeric := (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
		if !alphaNumeric && b != '-' && b != '_' && b != '.' && b != ':' {
			return fmt.Errorf("%s contains an invalid character", name)
		}
		if i == 0 && !alphaNumeric {
			return fmt.Errorf("%s must begin with an alphanumeric character", name)
		}
	}
	return nil
}

func validateTaskRequestHash(requestHash string) error {
	if len(requestHash) != 64 || strings.ToLower(requestHash) != requestHash {
		return errors.New("request_hash must be a lowercase SHA-256 hex digest")
	}
	if _, err := hex.DecodeString(requestHash); err != nil {
		return errors.New("request_hash must be a lowercase SHA-256 hex digest")
	}
	return nil
}

func validateTaskOperationResult(result json.RawMessage) error {
	if len(result) == 0 || len(result) > maxTaskOperationResultBytes || !json.Valid(result) {
		return fmt.Errorf("task operation result must be valid JSON of at most %d bytes", maxTaskOperationResultBytes)
	}
	return nil
}

func validateTaskOperationReceipt(receipt TaskOperationReceipt) error {
	if err := validateTaskOperationID(receipt.OperationID); err != nil {
		return err
	}
	if err := validateTaskRequestHash(receipt.RequestHash); err != nil {
		return err
	}
	switch receipt.Scope {
	case "", TaskOperationScopeTask:
		if err := receipt.TaskID.Validate(); err != nil {
			return err
		}
	case TaskOperationScopeGlobal:
		if receipt.TaskID != "" {
			return errors.New("global task operation receipt must not have a task_id")
		}
	default:
		return errors.New("task operation receipt scope is invalid")
	}
	if err := validateTaskOperationResult(receipt.Result); err != nil {
		return err
	}
	if receipt.WriterGeneration == 0 {
		return errors.New("task operation receipt writer_generation must be positive")
	}
	return nil
}

func readTaskOperationReceipts(ctx context.Context, path string) (map[string]TaskOperationReceipt, error) {
	items, err := readJSONLLines[TaskOperationReceipt](ctx, path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTaskOperationReceiptCorrupt, err)
	}
	receipts := make(map[string]TaskOperationReceipt, len(items))
	for index, receipt := range items {
		if err := validateTaskOperationReceipt(receipt); err != nil {
			return nil, fmt.Errorf("%w: record %d: %v", ErrTaskOperationReceiptCorrupt, index, err)
		}
		if previous, ok := receipts[receipt.OperationID]; ok {
			if !sameTaskOperationReceipt(previous, receipt) {
				return nil, fmt.Errorf("%w: operation_id %q has conflicting duplicate receipts", ErrTaskOperationReceiptCorrupt, receipt.OperationID)
			}
			continue
		}
		receipt.Result = append(json.RawMessage(nil), receipt.Result...)
		receipts[receipt.OperationID] = receipt
	}
	return receipts, nil
}

func sameTaskOperationReceipt(left, right TaskOperationReceipt) bool {
	return left.OperationID == right.OperationID && left.RequestHash == right.RequestHash && effectiveTaskOperationScope(left.Scope) == effectiveTaskOperationScope(right.Scope) && left.TaskID == right.TaskID && left.WriterGeneration == right.WriterGeneration && bytes.Equal(left.Result, right.Result)
}

func effectiveTaskOperationScope(scope TaskOperationScope) TaskOperationScope {
	if scope == "" {
		return TaskOperationScopeTask
	}
	return scope
}

func readTaskExecutionFences(ctx context.Context, path string) (taskExecutionFenceSnapshot, error) {
	items, err := readJSONLLines[taskExecutionFenceEvent](ctx, path)
	if err != nil {
		return taskExecutionFenceSnapshot{}, fmt.Errorf("%w: %v", ErrTaskExecutionFenceCorrupt, err)
	}
	snapshot := taskExecutionFenceSnapshot{
		byID:   make(map[string]persistedTaskFence, len(items)),
		active: make(map[modulecore.TaskID]string),
		latest: make(map[modulecore.TaskID]string),
	}
	for index, event := range items {
		if err := event.TaskID.Validate(); err != nil {
			return taskExecutionFenceSnapshot{}, fmt.Errorf("%w: record %d task_id: %v", ErrTaskExecutionFenceCorrupt, index, err)
		}
		if err := validateTaskExecutionFenceID(event.FenceID); err != nil {
			return taskExecutionFenceSnapshot{}, fmt.Errorf("%w: record %d: %v", ErrTaskExecutionFenceCorrupt, index, err)
		}
		if event.WriterGeneration == 0 {
			return taskExecutionFenceSnapshot{}, fmt.Errorf("%w: record %d has no writer generation", ErrTaskExecutionFenceCorrupt, index)
		}
		previous, exists := snapshot.byID[event.FenceID]
		switch event.Event {
		case "acquired":
			if exists {
				if previous.TaskID == event.TaskID && previous.Active && previous.AcquireGeneration == event.WriterGeneration {
					continue
				}
				return taskExecutionFenceSnapshot{}, fmt.Errorf("%w: record %d reuses fence capability %q", ErrTaskExecutionFenceCorrupt, index, event.FenceID)
			}
			if activeID := snapshot.active[event.TaskID]; activeID != "" {
				return taskExecutionFenceSnapshot{}, fmt.Errorf("%w: task %s has overlapping capabilities", ErrTaskExecutionFenceCorrupt, event.TaskID)
			}
			snapshot.byID[event.FenceID] = persistedTaskFence{
				TaskID:            event.TaskID,
				FenceID:           event.FenceID,
				Active:            true,
				AcquireGeneration: event.WriterGeneration,
			}
			snapshot.active[event.TaskID] = event.FenceID
			snapshot.latest[event.TaskID] = event.FenceID
		case "released":
			if !exists || previous.TaskID != event.TaskID {
				return taskExecutionFenceSnapshot{}, fmt.Errorf("%w: record %d releases an unknown capability", ErrTaskExecutionFenceCorrupt, index)
			}
			if !previous.Active {
				if previous.ReleaseGeneration == event.WriterGeneration {
					continue
				}
				return taskExecutionFenceSnapshot{}, fmt.Errorf("%w: record %d conflicts with a prior release", ErrTaskExecutionFenceCorrupt, index)
			}
			if event.WriterGeneration < previous.AcquireGeneration || snapshot.active[event.TaskID] != event.FenceID {
				return taskExecutionFenceSnapshot{}, fmt.Errorf("%w: record %d release does not match the active capability", ErrTaskExecutionFenceCorrupt, index)
			}
			previous.Active = false
			previous.ReleaseGeneration = event.WriterGeneration
			snapshot.byID[event.FenceID] = previous
			delete(snapshot.active, event.TaskID)
			snapshot.latest[event.TaskID] = event.FenceID
		default:
			return taskExecutionFenceSnapshot{}, fmt.Errorf("%w: record %d has invalid event %q", ErrTaskExecutionFenceCorrupt, index, event.Event)
		}
	}
	return snapshot, nil
}

func taskFenceStatusForTask(snapshot taskExecutionFenceSnapshot, taskID modulecore.TaskID) (TaskExecutionFenceStatus, error) {
	fenceID := snapshot.latest[taskID]
	if fenceID == "" {
		return TaskExecutionFenceStatus{}, ErrTaskExecutionFenceNotFound
	}
	record, ok := snapshot.byID[fenceID]
	if !ok {
		return TaskExecutionFenceStatus{}, fmt.Errorf("%w: latest capability is absent", ErrTaskExecutionFenceCorrupt)
	}
	status := TaskExecutionFenceStatus{
		TaskID:                  record.TaskID,
		FenceID:                 record.FenceID,
		AcquireWriterGeneration: record.AcquireGeneration,
		ReleaseWriterGeneration: record.ReleaseGeneration,
	}
	if record.Active {
		status.State = TaskExecutionFenceStateActive
	} else {
		status.State = TaskExecutionFenceStateReleased
	}
	return status, nil
}

func (s *JSONLStore) knownActiveTaskFenceStatus(taskID modulecore.TaskID) (TaskExecutionFenceStatus, bool) {
	s.fenceMu.RLock()
	defer s.fenceMu.RUnlock()
	fenceID := s.activeFences[taskID]
	if fenceID == "" {
		return TaskExecutionFenceStatus{}, false
	}
	record, ok := s.fenceByID[fenceID]
	if !ok {
		return TaskExecutionFenceStatus{}, false
	}
	return TaskExecutionFenceStatus{
		TaskID:                  record.TaskID,
		FenceID:                 record.FenceID,
		State:                   TaskExecutionFenceStateUncertain,
		AcquireWriterGeneration: record.AcquireGeneration,
		ReleaseWriterGeneration: record.ReleaseGeneration,
		RecoveryRequired:        true,
	}, true
}

func (s *JSONLStore) installTaskFenceSnapshot(snapshot taskExecutionFenceSnapshot) {
	if s == nil {
		return
	}
	s.fenceMu.Lock()
	s.fenceByID = make(map[string]persistedTaskFence, len(snapshot.byID))
	for fenceID, record := range snapshot.byID {
		record.Local = false
		s.fenceByID[fenceID] = record
	}
	s.activeFences = make(map[modulecore.TaskID]string, len(snapshot.active))
	for taskID, fenceID := range snapshot.active {
		s.activeFences[taskID] = fenceID
	}
	s.fenceMu.Unlock()
}

func (s *JSONLStore) activeTaskExecutionFenceError(taskID modulecore.TaskID) error {
	if s == nil {
		return errors.New("task store is nil")
	}
	s.fenceMu.RLock()
	defer s.fenceMu.RUnlock()
	if s.fenceRecovery {
		return errors.Join(ErrTaskExecutionFenceRecoveryRequired, jsonlbatch.ErrRecoveryRequired)
	}
	if fenceID := s.activeFences[taskID]; fenceID != "" {
		return fmt.Errorf("%w: task %s capability %s", ErrTaskExecutionFenceActive, taskID, fenceID)
	}
	return nil
}

func (s *JSONLStore) persistTaskExecutionFenceAcquire(ctx context.Context, taskID modulecore.TaskID, fenceID string, local bool) error {
	s.fenceMu.Lock()
	if s.fenceRecovery {
		s.fenceMu.Unlock()
		return errors.Join(ErrTaskExecutionFenceRecoveryRequired, jsonlbatch.ErrRecoveryRequired)
	}
	if previous, ok := s.fenceByID[fenceID]; ok {
		s.fenceMu.Unlock()
		if previous.TaskID != taskID || !previous.Active {
			return ErrTaskExecutionFenceMismatch
		}
		return nil
	}
	if activeID := s.activeFences[taskID]; activeID != "" {
		s.fenceMu.Unlock()
		return fmt.Errorf("%w: task %s capability %s", ErrTaskExecutionFenceActive, taskID, activeID)
	}
	provisional := persistedTaskFence{TaskID: taskID, FenceID: fenceID, Active: true, AcquireGeneration: s.writerGeneration, Local: local}
	s.fenceByID[fenceID] = provisional
	s.activeFences[taskID] = fenceID
	s.fenceMu.Unlock()

	event := taskExecutionFenceEvent{Event: "acquired", TaskID: taskID, FenceID: fenceID, WriterGeneration: s.writerGeneration}
	var observed taskExecutionFenceSnapshot
	observedOnDisk := false
	err := s.writeTaskFenceEvent(withTxLabel(ctx, "AcquireTaskExecutionFence", taskID), func() (map[string][]byte, error) {
		snapshot, err := readTaskExecutionFences(ctx, filepath.Join(s.root, taskExecutionFenceFilename))
		if err != nil {
			return nil, err
		}
		if previous, ok := snapshot.byID[fenceID]; ok {
			if previous.TaskID != taskID || !previous.Active {
				return nil, ErrTaskExecutionFenceMismatch
			}
			observed = snapshot
			observedOnDisk = true
			return nil, nil
		}
		if activeID := snapshot.active[taskID]; activeID != "" {
			return nil, fmt.Errorf("%w: task %s capability %s", ErrTaskExecutionFenceActive, taskID, activeID)
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		return map[string][]byte{taskExecutionFenceFilename: append(encoded, '\n')}, nil
	})
	if err != nil {
		if errors.Is(err, jsonlbatch.ErrRecoveryRequired) {
			s.fenceMu.Lock()
			s.fenceRecovery = true
			s.fenceMu.Unlock()
			return errors.Join(ErrTaskExecutionFenceRecoveryRequired, err)
		}
		s.clearProvisionalTaskFence(taskID, fenceID)
		return err
	}
	if observedOnDisk {
		s.installTaskFenceSnapshot(observed)
		return nil
	}
	s.fenceMu.Lock()
	provisional.AcquireGeneration = s.writerGeneration
	provisional.Local = local
	s.fenceByID[fenceID] = provisional
	s.activeFences[taskID] = fenceID
	s.fenceMu.Unlock()
	return nil
}

func (s *JSONLStore) persistTaskExecutionFenceRelease(ctx context.Context, taskID modulecore.TaskID, fenceID string) error {
	s.fenceMu.RLock()
	if s.fenceRecovery {
		s.fenceMu.RUnlock()
		return errors.Join(ErrTaskExecutionFenceRecoveryRequired, jsonlbatch.ErrRecoveryRequired)
	}
	previous, exists := s.fenceByID[fenceID]
	activeID := s.activeFences[taskID]
	s.fenceMu.RUnlock()
	if !exists || previous.TaskID != taskID {
		return ErrTaskExecutionFenceMismatch
	}
	if !previous.Active {
		return nil
	}
	if activeID != fenceID {
		return ErrTaskExecutionFenceMismatch
	}

	event := taskExecutionFenceEvent{Event: "released", TaskID: taskID, FenceID: fenceID, WriterGeneration: s.writerGeneration}
	var observed taskExecutionFenceSnapshot
	observedOnDisk := false
	err := s.writeTaskFenceEvent(withTxLabel(ctx, "ReleaseTaskExecutionFence", taskID), func() (map[string][]byte, error) {
		snapshot, err := readTaskExecutionFences(ctx, filepath.Join(s.root, taskExecutionFenceFilename))
		if err != nil {
			return nil, err
		}
		current, ok := snapshot.byID[fenceID]
		if !ok || current.TaskID != taskID {
			return nil, ErrTaskExecutionFenceMismatch
		}
		if !current.Active {
			observed = snapshot
			observedOnDisk = true
			return nil, nil
		}
		if snapshot.active[taskID] != fenceID {
			return nil, ErrTaskExecutionFenceMismatch
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		return map[string][]byte{taskExecutionFenceFilename: append(encoded, '\n')}, nil
	})
	if err != nil {
		if errors.Is(err, jsonlbatch.ErrRecoveryRequired) {
			s.fenceMu.Lock()
			s.fenceRecovery = true
			s.fenceMu.Unlock()
			return errors.Join(ErrTaskExecutionFenceRecoveryRequired, err)
		}
		return err
	}
	if observedOnDisk {
		s.installTaskFenceSnapshot(observed)
		return nil
	}
	s.fenceMu.Lock()
	previous.Active = false
	previous.ReleaseGeneration = s.writerGeneration
	previous.Local = false
	s.fenceByID[fenceID] = previous
	delete(s.activeFences, taskID)
	s.fenceMu.Unlock()
	return nil
}

func (s *JSONLStore) clearProvisionalTaskFence(taskID modulecore.TaskID, fenceID string) {
	s.fenceMu.Lock()
	if current, ok := s.fenceByID[fenceID]; ok && current.TaskID == taskID && current.Active {
		delete(s.fenceByID, fenceID)
		if s.activeFences[taskID] == fenceID {
			delete(s.activeFences, taskID)
		}
	}
	s.fenceMu.Unlock()
}

func (s *JSONLStore) writeTaskFenceEvent(ctx context.Context, callback func() (map[string][]byte, error)) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return os.ErrClosed
	}
	if s.readOnly || s.writerLock == nil || s.batch == nil {
		return errors.New("task store writer is unavailable")
	}
	return s.writeBatch(ctx, callback)
}
