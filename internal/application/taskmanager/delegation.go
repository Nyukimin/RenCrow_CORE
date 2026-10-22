package taskmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const delegationStartOperation = "start"

var (
	ErrDelegationAdmissionCheckRequired = errors.New("delegation admission check is required")
	ErrDelegationReceiptCorrupt         = errors.New("delegation Task receipt is corrupt")
	ErrGlobalTaskOperationUnsupported   = errors.New("Task store does not support global idempotent operations")
)

// DelegationAdmissionCheck must read the current authenticated delegation
// owner state and recognize the owner's canonical accepted-readback semantics.
// The current EventStore persists that state as "accepted" (the spec's
// ACKNOWLEDGED state); the caller maps the semantic transition and this API
// does not assume a literal state label. Reject a stale revision,
// canceled/rejected delegation, or missing permission to start the requested
// Task. An empty capability allowlist is deny-all. The caller must bind this
// check to the submitted Task draft. It is called before storage admission and
// again immediately before the Task-owner transaction commits. It must be
// read-only and safe to call more than once.
type DelegationAdmissionCheck = func(context.Context, modulecore.RequestID, uint64) error

type globalTaskOperationStore interface {
	ExecuteIdempotentGlobalTaskOperation(context.Context, string, string, func(Store) (json.RawMessage, error)) (json.RawMessage, error)
}

type delegationTaskOperationReceipt struct {
	TaskID modulecore.TaskID `json:"task_id"`
	RunID  modulecore.RunID  `json:"run_id"`
}

type delegationTaskOperationRequest struct {
	Operation    string                       `json:"operation"`
	DelegationID modulecore.RequestID         `json:"delegation_id"`
	Revision     uint64                       `json:"revision"`
	Task         domaintask.Task              `json:"task"`
	Shared       domaintask.SharedRoleContext `json:"shared_context"`
}

// DelegationTaskOperationID returns the canonical Task-owner operation identity
// for one delegation revision. EventStore stores this identity when binding
// the Task, while TaskManager remains the owner of Task operation receipts.
// The exact format is a cross-store contract: delegation:<id>:<revision>:start.
func DelegationTaskOperationID(delegationID modulecore.RequestID, revision uint64) string {
	return delegationTaskOperationID(delegationID, revision)
}

// EnsureTaskForDelegation atomically creates a delegated Task, its SharedRoleContext,
// its first Run, and a Task-owner replay receipt. The operation identity is
// stable for delegationID/revision, so a caller can recover the same Task and
// Run after an EventStore work_task_id bind failure or process restart by
// retrying the same delegationID/revision. The Task receipt is local to the
// Task owner; this method does not write EventStore state or schedule the Run.
//
// The admission check is required because a persisted readback is not by itself
// authorization to execute. The check should consult the current authenticated
// delegation record and validate its revision, cancellation state, and Task
// execution permission. Task creation, first-Run policy/capacity admission, and
// Run validation continue through the normal TaskManager path. Admission does
// not grant durable execution authority: immediately before any external side
// effect, the executor must revalidate the active Task/Run and current
// delegation revision, cancellation state, and policy through their owners,
// inside the Task/Run execution-fence boundary.
func (m *Manager) EnsureTaskForDelegation(
	ctx context.Context,
	delegationID modulecore.RequestID,
	revision uint64,
	draft domaintask.Task,
	shared domaintask.SharedRoleContext,
	check DelegationAdmissionCheck,
) (domaintask.Task, domaintask.Run, error) {
	if m == nil || m.store == nil {
		return domaintask.Task{}, domaintask.Run{}, errors.New("task manager store is unavailable")
	}
	if ctx == nil {
		return domaintask.Task{}, domaintask.Run{}, errors.New("delegation Task context is nil")
	}
	if err := delegationID.Validate(); err != nil {
		return domaintask.Task{}, domaintask.Run{}, fmt.Errorf("delegation_id is invalid: %w", err)
	}
	if revision == 0 {
		return domaintask.Task{}, domaintask.Run{}, errors.New("delegation revision must be positive")
	}
	if check == nil {
		return domaintask.Task{}, domaintask.Run{}, ErrDelegationAdmissionCheckRequired
	}
	store, ok := m.store.(globalTaskOperationStore)
	if !ok {
		return domaintask.Task{}, domaintask.Run{}, ErrGlobalTaskOperationUnsupported
	}

	if err := check(ctx, delegationID, revision); err != nil {
		return domaintask.Task{}, domaintask.Run{}, fmt.Errorf("delegation admission check: %w", err)
	}
	operationID := DelegationTaskOperationID(delegationID, revision)
	requestHash, err := hashDelegationTaskOperation(delegationTaskOperationRequest{
		Operation:    delegationStartOperation,
		DelegationID: delegationID,
		Revision:     revision,
		Task:         draft,
		Shared:       shared,
	})
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, fmt.Errorf("hash delegation Task request: %w", err)
	}

	result, err := store.ExecuteIdempotentGlobalTaskOperation(ctx, operationID, requestHash, func(tx Store) (json.RawMessage, error) {
		if tx == nil {
			return nil, errors.New("global Task transaction store is unavailable")
		}
		txManager := &Manager{
			store:                    tx,
			limits:                   m.limits,
			expectedCriteriaRevision: m.expectedCriteriaRevision,
			idIndexed:                m.idIndexed,
			now:                      m.now,
		}
		task, run, err := txManager.createAndStartRunInTransaction(ctx, draft, shared)
		if err != nil {
			return nil, err
		}
		// Revalidate after staging all Task-owner records and directly before
		// returning the transaction payload to its commit boundary.
		if err := check(ctx, delegationID, revision); err != nil {
			return nil, fmt.Errorf("delegation pre-commit admission check: %w", err)
		}
		return json.Marshal(delegationTaskOperationReceipt{TaskID: task.TaskID, RunID: run.RunID})
	})
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}

	receipt, err := decodeDelegationTaskOperationReceipt(result)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	var task domaintask.Task
	var run domaintask.Run
	err = m.readTransaction(ctx, func(txManager *Manager) error {
		var err error
		task, err = txManager.store.GetTask(ctx, receipt.TaskID)
		if err != nil {
			return fmt.Errorf("read delegated Task from receipt: %w", err)
		}
		run, err = txManager.store.GetRun(ctx, receipt.RunID)
		if err != nil {
			return fmt.Errorf("read delegated Run from receipt: %w", err)
		}
		shared, err := txManager.store.GetContext(ctx, receipt.TaskID)
		if err != nil {
			return fmt.Errorf("read delegated Task context from receipt: %w", err)
		}
		if task.TaskID != receipt.TaskID || run.RunID != receipt.RunID || run.TaskID != receipt.TaskID || run.StartReason != domaintask.RunStartReasonFirst || shared.TaskID != receipt.TaskID {
			return ErrDelegationReceiptCorrupt
		}
		return nil
	})
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	return task, run, nil
}

func delegationTaskOperationID(delegationID modulecore.RequestID, revision uint64) string {
	return "delegation:" + string(delegationID) + ":" + strconv.FormatUint(revision, 10) + ":" + delegationStartOperation
}

func hashDelegationTaskOperation(request delegationTaskOperationRequest) (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func decodeDelegationTaskOperationReceipt(raw json.RawMessage) (delegationTaskOperationReceipt, error) {
	var receipt delegationTaskOperationReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return delegationTaskOperationReceipt{}, fmt.Errorf("%w: %v", ErrDelegationReceiptCorrupt, err)
	}
	if receipt.TaskID.Validate() != nil || receipt.RunID.Validate() != nil {
		return delegationTaskOperationReceipt{}, ErrDelegationReceiptCorrupt
	}
	return receipt, nil
}
