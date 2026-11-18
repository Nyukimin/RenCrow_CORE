package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// NativePlanningOperationAuthorizationCheck must verify the current exact
// parent Task/Run and startup-bound actor. It runs before receipt access and
// again before the result is returned. The result remains owner-only in the
// generic receipt API, including when its child Task is public.
type NativePlanningOperationAuthorizationCheck = domaintask.NativePlanningParentAuthorizationCheck

// ExecuteIdempotentNativePlanningOperation atomically creates a bounded
// planning Task/Run and an owner-only replay receipt. A matching replay may
// read that receipt only after the caller revalidates its parent authority.
func (s *JSONLStore) ExecuteIdempotentNativePlanningOperation(
	ctx context.Context,
	operationID string,
	requestHash string,
	authorize NativePlanningOperationAuthorizationCheck,
	fn func(domaintask.Store) (json.RawMessage, error),
) (json.RawMessage, error) {
	if s == nil {
		return nil, errors.New("task store is nil")
	}
	if ctx == nil || authorize == nil {
		return nil, errors.New("native planning operation requires context and parent authorization")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := authorize(ctx); err != nil {
		return nil, fmt.Errorf("native planning parent authorization: %w", err)
	}
	result, err := s.executeIdempotentGlobalTaskOperation(ctx, operationID, requestHash, true, fn)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := authorize(ctx); err != nil {
		return nil, fmt.Errorf("native planning parent revalidation: %w", err)
	}
	return append(json.RawMessage(nil), result...), nil
}

// LookupNativePlanningOperationReceipt is a read-only owner-authorized replay
// lookup. It never exposes public receipts through the generic owner API.
func (s *JSONLStore) LookupNativePlanningOperationReceipt(
	ctx context.Context,
	operationID string,
	requestHash string,
	authorize NativePlanningOperationAuthorizationCheck,
) (json.RawMessage, bool, error) {
	if s == nil {
		return nil, false, errors.New("task store is nil")
	}
	if ctx == nil || authorize == nil {
		return nil, false, errors.New("native planning receipt lookup requires context and parent authorization")
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := authorize(ctx); err != nil {
		return nil, false, fmt.Errorf("native planning parent authorization: %w", err)
	}
	if err := validateTaskOperationID(operationID); err != nil {
		return nil, false, err
	}
	if err := validateTaskRequestHash(requestHash); err != nil {
		return nil, false, err
	}
	releaseAdmission, err := s.lifecycle.admit(ctx)
	if err != nil {
		return nil, false, err
	}
	defer releaseAdmission()
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, false, os.ErrClosed
	}
	receipt, found, err := s.loadReceipt(ctx, operationID)
	s.mu.RUnlock()
	if err != nil {
		return nil, false, err
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := authorize(ctx); err != nil {
		return nil, false, fmt.Errorf("native planning parent revalidation: %w", err)
	}
	if !found {
		return nil, false, nil
	}
	if effectiveTaskOperationScope(receipt.Scope) != TaskOperationScopeGlobal || receipt.TaskID != "" || receipt.RequestHash != requestHash {
		return nil, false, ErrTaskOperationConflict
	}
	if !receipt.OwnerOnly {
		return nil, false, ErrTaskOperationReceiptCorrupt
	}
	return append(json.RawMessage(nil), receipt.Result...), true, nil
}
