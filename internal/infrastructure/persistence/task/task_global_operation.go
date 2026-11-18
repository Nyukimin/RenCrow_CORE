package task

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// ExecuteIdempotentGlobalTaskOperation commits an unrestricted Task Store
// transaction and its global-scope owner receipt in one JSONL batch. The
// callback is not invoked when the operation already has a matching receipt.
func (s *JSONLStore) ExecuteIdempotentGlobalTaskOperation(
	ctx context.Context,
	operationID string,
	requestHash string,
	fn func(domaintask.Store) (json.RawMessage, error),
) (json.RawMessage, error) {
	return s.executeIdempotentGlobalTaskOperation(ctx, operationID, requestHash, false, fn)
}

func (s *JSONLStore) executeIdempotentGlobalTaskOperation(
	ctx context.Context,
	operationID string,
	requestHash string,
	allowOwnerOnlyReplay bool,
	fn func(domaintask.Store) (json.RawMessage, error),
) (json.RawMessage, error) {
	if s == nil {
		return nil, errors.New("task store is nil")
	}
	if ctx == nil {
		return nil, errors.New("global task operation context is nil")
	}
	if fn == nil {
		return nil, errors.New("global task operation callback is nil")
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

	releaseSynchronousWrite, err := s.beginSynchronousWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer releaseSynchronousWrite()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, os.ErrClosed
	}
	if s.batch == nil || s.writerLock == nil || s.writerGeneration == 0 {
		return nil, errors.New("task store writer is unavailable")
	}
	var result json.RawMessage
	err = s.writeBatchNow(withTxLabel(ctx, "ExecuteIdempotentGlobalTaskOperation", ""), func() (map[string][]byte, error) {
		previous, ok, err := s.loadReceipt(ctx, operationID)
		if err != nil {
			return nil, err
		}
		if ok {
			if effectiveTaskOperationScope(previous.Scope) != TaskOperationScopeGlobal || previous.TaskID != "" || previous.RequestHash != requestHash {
				return nil, ErrTaskOperationConflict
			}
			if previous.OwnerOnly && !allowOwnerOnlyReplay {
				return nil, ErrTaskOperationNotFound
			}
			if allowOwnerOnlyReplay && !previous.OwnerOnly {
				return nil, ErrTaskOperationReceiptCorrupt
			}
			result = append(json.RawMessage(nil), previous.Result...)
			return nil, nil
		}

		tx := newTaskTransaction(s, false, modulecore.TaskID(""), nil)
		defer tx.end()
		callbackResult, callbackErr := fn(tx)
		ownerOnly, ownerOnlyErr := taskTransactionTouchesOwnerOnlyTask(ctx, tx)
		snapshot := tx.freezeAndSnapshot()
		if callbackErr != nil {
			return nil, callbackErr
		}
		if ownerOnlyErr != nil {
			return nil, ownerOnlyErr
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
			Scope:            TaskOperationScopeGlobal,
			OwnerOnly:        ownerOnly,
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

func taskTransactionTouchesOwnerOnlyTask(ctx context.Context, tx *transactionStore) (bool, error) {
	if tx == nil || tx.state == nil {
		return false, errTaskTransactionExpired
	}
	tx.state.mu.Lock()
	if tx.state.ownerOnlyAccessed {
		tx.state.mu.Unlock()
		return true, nil
	}
	touched := make([]modulecore.TaskID, 0, len(tx.state.touched))
	for taskID := range tx.state.touched {
		touched = append(touched, taskID)
	}
	tx.state.mu.Unlock()
	for _, taskID := range touched {
		task, err := tx.getTaskRaw(ctx, taskID)
		if err != nil {
			return false, err
		}
		if task.PrivateAccess != nil || domaintask.IsOwnerManagedTask(task) {
			return true, nil
		}
	}
	return false, nil
}

// LookupGlobalTaskOperation returns a committed global Task operation result
// without creating or repairing any owner state. A matching operation ID with
// a different request hash or scope is a conflict, never a replay.
func (s *JSONLStore) LookupGlobalTaskOperation(
	ctx context.Context,
	operationID string,
	requestHash string,
) (json.RawMessage, bool, error) {
	if s == nil {
		return nil, false, errors.New("task store is nil")
	}
	if ctx == nil {
		return nil, false, errors.New("global task operation lookup context is nil")
	}
	if err := validateTaskOperationID(operationID); err != nil {
		return nil, false, err
	}
	if err := validateTaskRequestHash(requestHash); err != nil {
		return nil, false, err
	}
	receipt, err := s.LookupTaskOperation(ctx, operationID)
	if errors.Is(err, ErrTaskOperationNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if effectiveTaskOperationScope(receipt.Scope) != TaskOperationScopeGlobal || receipt.TaskID != "" || receipt.RequestHash != requestHash {
		return nil, false, ErrTaskOperationConflict
	}
	var linked struct {
		TaskID modulecore.TaskID `json:"task_id"`
	}
	if err := json.Unmarshal(receipt.Result, &linked); err == nil && linked.TaskID != "" {
		if _, err := s.GetTask(ctx, linked.TaskID); err != nil {
			if errors.Is(err, domaintask.ErrNotFound) {
				return nil, false, nil
			}
			return nil, false, err
		}
	}
	return append(json.RawMessage(nil), receipt.Result...), true, nil
}
