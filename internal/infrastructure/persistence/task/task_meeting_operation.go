package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// ExecuteIdempotentTaskMeetingOperation stores a receipt that is available only
// through the TaskMeeting owner port. The authorization callback is checked
// before the transaction and again after the result is committed.
func (s *JSONLStore) ExecuteIdempotentTaskMeetingOperation(
	ctx context.Context,
	operationID string,
	requestHash string,
	authorize domaintask.TaskMeetingInvocationOperationAuthorizer,
	fn func(domaintask.Store) (json.RawMessage, error),
) (json.RawMessage, error) {
	if s == nil || ctx == nil || authorize == nil || fn == nil {
		return nil, domaintask.ErrTaskMeetingOwnerRequired
	}
	if err := authorize(ctx); err != nil {
		return nil, fmt.Errorf("TaskMeeting operation authorization: %w", err)
	}
	result, err := s.executeIdempotentGlobalTaskOperation(ctx, operationID, requestHash, true, fn)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := authorize(ctx); err != nil {
		return nil, fmt.Errorf("TaskMeeting operation reauthorization: %w", err)
	}
	return append(json.RawMessage(nil), result...), nil
}

// LookupTaskMeetingOperationReceipt returns only owner-only global receipts.
// Missing means the operation has never committed; malformed or foreign
// receipts remain errors and must not trigger a second creation attempt.
func (s *JSONLStore) LookupTaskMeetingOperationReceipt(
	ctx context.Context,
	operationID string,
	authorize domaintask.TaskMeetingInvocationOperationAuthorizer,
) (json.RawMessage, string, bool, error) {
	if s == nil || ctx == nil || authorize == nil {
		return nil, "", false, domaintask.ErrTaskMeetingOwnerRequired
	}
	if err := authorize(ctx); err != nil {
		return nil, "", false, fmt.Errorf("TaskMeeting receipt authorization: %w", err)
	}
	receipt, err := s.lookupTaskOperationRaw(ctx, operationID)
	if err != nil {
		if errors.Is(err, ErrTaskOperationNotFound) {
			return nil, "", false, nil
		}
		return nil, "", false, err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", false, err
	}
	if err := authorize(ctx); err != nil {
		return nil, "", false, fmt.Errorf("TaskMeeting receipt reauthorization: %w", err)
	}
	if receipt.Scope != TaskOperationScopeGlobal || receipt.TaskID != "" || !receipt.OwnerOnly {
		return nil, "", false, ErrTaskOperationReceiptCorrupt
	}
	return append(json.RawMessage(nil), receipt.Result...), receipt.RequestHash, true, nil
}

var _ domaintask.TaskMeetingInvocationOperationStore = (*JSONLStore)(nil)
