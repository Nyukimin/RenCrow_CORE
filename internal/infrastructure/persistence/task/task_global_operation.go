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

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, os.ErrClosed
	}
	if s.batch == nil || s.writerLock == nil || s.writerGeneration == 0 {
		return nil, errors.New("task store writer is unavailable")
	}
	var result json.RawMessage
	err = s.writeBatch(withTxLabel(ctx, "ExecuteIdempotentGlobalTaskOperation", ""), func() (map[string][]byte, error) {
		previous, ok, err := s.loadReceipt(ctx, operationID)
		if err != nil {
			return nil, err
		}
		if ok {
			if effectiveTaskOperationScope(previous.Scope) != TaskOperationScopeGlobal || previous.TaskID != "" || previous.RequestHash != requestHash {
				return nil, ErrTaskOperationConflict
			}
			result = append(json.RawMessage(nil), previous.Result...)
			return nil, nil
		}

		tx := newTaskTransaction(s, false, modulecore.TaskID(""), nil)
		defer tx.end()
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
			Scope:            TaskOperationScopeGlobal,
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
