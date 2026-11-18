package task

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func (s *transactionStore) SaveTaskCenterExecutionState(
	ctx context.Context,
	task domaintask.Task,
	run domaintask.Run,
	shared *domaintask.SharedRoleContext,
	authorize domaintask.TaskCenterExecutionAuthorizationCheck,
) error {
	if ctx == nil {
		return errors.New("TaskCenter execution write context is nil")
	}
	if authorize == nil {
		return domaintask.ErrTypedTaskRequiresOwner
	}
	if err := task.Validate(); err != nil {
		return err
	}
	if !domaintask.IsTaskCenterTyped(task) || domaintask.IsNativePlanningTask(task) || task.TaskCenterExecution.Validate(task) != nil {
		return domaintask.ErrTypedTaskRequiresOwner
	}
	if err := run.Validate(); err != nil {
		return err
	}
	if run.TaskID != task.TaskID {
		return errors.New("TaskCenter Run does not belong to its Task")
	}
	if shared != nil && shared.TaskID != task.TaskID {
		return errors.New("TaskCenter context identity is invalid")
	}
	authorizationShared := domaintask.SharedRoleContext{}
	if shared != nil {
		authorizationShared = *shared
	} else {
		var err error
		authorizationShared, err = s.getContextRaw(ctx, task.TaskID)
		if err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := authorize(ctx, task, run, authorizationShared); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return domaintask.ErrTypedTaskRequiresOwner
	}

	previous, err := s.getTaskRaw(ctx, task.TaskID)
	creating := errors.Is(err, domaintask.ErrNotFound)
	if err != nil && !creating {
		return err
	}
	if creating {
		if task.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning || run.StartReason != domaintask.RunStartReasonFirst || shared == nil {
			return errors.New("first TaskCenter state must contain a running first Run and context")
		}
	} else {
		if !domaintask.IsTaskCenterTyped(previous) || previous.TaskCenterExecution == nil || *previous.TaskCenterExecution != *task.TaskCenterExecution {
			return domaintask.ErrTaskCenterExecutionImmutable
		}
		if shared != nil {
			return errors.New("TaskCenter context may only be written when creating the Task")
		}
	}
	if err := s.saveTask(ctx, task); err != nil {
		return err
	}
	if err := s.saveRun(ctx, run); err != nil {
		return err
	}
	if shared != nil {
		if err := s.touchTask(ctx, task.TaskID); err != nil {
			return err
		}
		if err := s.appendValue(ctx, contextFilename, *shared); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := authorize(ctx, task, run, authorizationShared); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return domaintask.ErrTypedTaskRequiresOwner
	}
	return nil
}

func (s *JSONLStore) WithTaskCenterExecutionFence(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	fn domaintask.TaskCenterExecutionFenceCallback,
) error {
	if s == nil {
		return errors.New("Task store is unavailable")
	}
	if ctx == nil || fn == nil {
		return errors.New("TaskCenter execution fence requires context and callback")
	}
	if err := taskID.Validate(); err != nil {
		return err
	}
	if err := runID.Validate(); err != nil {
		return err
	}
	task, run, _, err := s.readTaskCenterExecutionState(ctx, taskID, runID)
	if err != nil {
		return err
	}
	if !domaintask.IsTaskCenterTyped(task) || domaintask.IsNativePlanningTask(task) {
		return domaintask.ErrTypedTaskRequiresOwner
	}
	return s.withDurableTaskExecutionFence(ctx, taskID, func() error {
		currentTask, currentRun, currentShared, err := s.readTaskCenterExecutionState(ctx, taskID, runID)
		if err != nil {
			return err
		}
		if currentTask.TaskID != task.TaskID || currentRun.RunID != run.RunID || currentRun.TaskID != taskID ||
			currentTask.TaskCenterExecution == nil || task.TaskCenterExecution == nil || *currentTask.TaskCenterExecution != *task.TaskCenterExecution {
			return domaintask.ErrTaskCenterExecutionReceiptForbidden
		}
		return fn(ctx, currentTask, currentRun, currentShared)
	})
}

func (s *JSONLStore) readTaskCenterExecutionState(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID) (domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, error) {
	var task domaintask.Task
	var run domaintask.Run
	var shared domaintask.SharedRoleContext
	err := s.ReadTransaction(ctx, func(store domaintask.Store) error {
		view, ok := store.(*transactionStore)
		if !ok {
			return domaintask.ErrTaskCenterExecutionReceiptUnsupported
		}
		var err error
		task, err = view.getTaskRaw(ctx, taskID)
		if err != nil {
			return err
		}
		run, err = view.getRunRaw(ctx, runID)
		if err != nil {
			return err
		}
		if run.TaskID != task.TaskID || run.RunID != runID {
			return errors.New("TaskCenter Run does not belong to its Task")
		}
		shared, err = view.getContextRaw(ctx, taskID)
		if err != nil {
			return err
		}
		return nil
	})
	return task, run, shared, err
}

func (s *JSONLStore) GetTaskCenterExecutionState(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, authorize domaintask.TaskCenterExecutionAuthorizationCheck) (domaintask.Task, domaintask.Run, error) {
	if ctx == nil || authorize == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTypedTaskRequiresOwner
	}
	var task domaintask.Task
	var run domaintask.Run
	err := s.ReadTransaction(ctx, func(store domaintask.Store) error {
		reader, ok := store.(domaintask.TaskCenterExecutionStateReader)
		if !ok {
			return domaintask.ErrTaskCenterExecutionReceiptUnsupported
		}
		var err error
		task, run, err = reader.GetTaskCenterExecutionState(ctx, taskID, runID, authorize)
		return err
	})
	return task, run, err
}

// RecoverTaskCenterExecutionFence closes only a stale active capability whose
// exact typed result and Goal source are proven by the owner callback. It is
// intentionally separate from the generic fence release API, which hides
// managed-private Task state.
func (s *JSONLStore) RecoverTaskCenterExecutionFence(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, reference domaintask.TaskCenterResultReference, authorize domaintask.TaskCenterExecutionFinishAuthorizationCheck) error {
	if s == nil || ctx == nil || authorize == nil {
		return domaintask.ErrTaskCenterExecutionReceiptAuthorizationRequired
	}
	if err := taskID.Validate(); err != nil {
		return err
	}
	if err := runID.Validate(); err != nil {
		return err
	}
	if reference.TaskID != taskID || reference.RunID != runID {
		return domaintask.ErrTaskCenterExecutionReceiptConflict
	}
	if s.readOnly {
		return errors.New("Task store is read-only")
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

	var snapshot taskExecutionFenceSnapshot
	s.mu.RLock()
	if s.closed || s.batch == nil {
		s.mu.RUnlock()
		return errors.New("Task store batch is unavailable")
	}
	err = s.batch.Read(withTxLabel(ctx, "RecoverTaskCenterExecutionFence", taskID), func() error {
		var readErr error
		snapshot, readErr = readTaskExecutionFences(ctx, s.fenceLogPaths()...)
		return readErr
	})
	s.mu.RUnlock()
	if err != nil {
		if errors.Is(err, jsonlbatch.ErrRecoveryRequired) {
			s.fenceMu.Lock()
			s.fenceRecovery = true
			s.fenceMu.Unlock()
			return errors.Join(ErrTaskExecutionFenceRecoveryRequired, err)
		}
		return err
	}
	status, err := taskFenceStatusForTask(snapshot, taskID)
	if errors.Is(err, ErrTaskExecutionFenceNotFound) || err == nil && status.State == TaskExecutionFenceStateReleased {
		return nil
	}
	if err != nil {
		return err
	}
	generation, err := s.WriterGeneration()
	if err != nil {
		return err
	}
	if status.State != TaskExecutionFenceStateActive || status.AcquireWriterGeneration == 0 || status.AcquireWriterGeneration >= generation {
		return ErrTaskExecutionFenceActive
	}
	task, run, shared, err := s.readTaskCenterExecutionState(ctx, taskID, runID)
	if err != nil {
		return err
	}
	if !domaintask.IsTaskCenterTyped(task) || domaintask.IsNativePlanningTask(task) || task.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning ||
		run.WriterGeneration != status.AcquireWriterGeneration || run.TaskID != taskID || run.RunID != runID || reference.Validate(task, run) != nil {
		return domaintask.ErrTaskCenterExecutionReceiptConflict
	}
	if err := authorize(ctx, task, run, shared, reference); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.persistTaskExecutionFenceRelease(context.WithoutCancel(ctx), taskID, status.FenceID); err != nil {
		return err
	}
	return nil
}

func (s *transactionStore) GetTaskCenterExecutionState(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, authorize domaintask.TaskCenterExecutionAuthorizationCheck) (domaintask.Task, domaintask.Run, error) {
	if ctx == nil || authorize == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTypedTaskRequiresOwner
	}
	task, run, shared, err := s.getTaskCenterExecutionStateRaw(ctx, taskID, runID)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	if !domaintask.IsTaskCenterTyped(task) || domaintask.IsNativePlanningTask(task) {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTypedTaskRequiresOwner
	}
	if err := authorize(ctx, task, run, shared); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return domaintask.Task{}, domaintask.Run{}, ctxErr
		}
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskCenterExecutionReceiptConflict
	}
	s.markOwnerOnlyRead(task)
	if err := ctx.Err(); err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	currentTask, currentRun, currentShared, err := s.getTaskCenterExecutionStateRaw(ctx, taskID, runID)
	if err != nil || currentTask.TaskCenterExecution == nil || *currentTask.TaskCenterExecution != *task.TaskCenterExecution || currentRun.RunID != run.RunID {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskCenterExecutionReceiptConflict
	}
	if err := authorize(ctx, currentTask, currentRun, currentShared); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return domaintask.Task{}, domaintask.Run{}, ctxErr
		}
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskCenterExecutionReceiptConflict
	}
	s.markOwnerOnlyRead(currentTask)
	return currentTask, currentRun, nil
}

func (s *transactionStore) getTaskCenterExecutionStateRaw(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID) (domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, error) {
	task, err := s.getTaskRaw(ctx, taskID)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	run, err := s.getRunRaw(ctx, runID)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	if run.TaskID != task.TaskID || run.RunID != runID {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, errors.New("TaskCenter Run does not belong to its Task")
	}
	shared, err := s.getContextRaw(ctx, taskID)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	return task, run, shared, nil
}

func (s *JSONLStore) LookupTaskCenterExecutionResultReceipt(
	ctx context.Context,
	input domaintask.TaskCenterExecutionResultReceiptReadInput,
	authorize domaintask.TaskCenterExecutionFinishAuthorizationCheck,
) (domaintask.Task, domaintask.Run, domaintask.TaskCenterResultReference, error) {
	if s == nil || ctx == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, errors.New("TaskCenter result receipt store or context is unavailable")
	}
	if authorize == nil || validateTaskRequestHash(input.RequestHash) != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, domaintask.ErrTaskCenterExecutionReceiptAuthorizationRequired
	}
	if err := ctx.Err(); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, err
	}
	task, run, shared, err := s.readTaskCenterExecutionState(ctx, input.TaskID, input.RunID)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, err
	}
	if !domaintask.TaskCenterDraftMatchesStored(input.ExpectedTask, task) {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, domaintask.ErrTaskCenterExecutionReceiptConflict
	}
	if err := input.ResultReference.Validate(task, run); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, domaintask.ErrTaskCenterExecutionReceiptConflict
	}
	if err := authorize(ctx, task, run, shared, input.ResultReference); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, ctxErr
		}
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, domaintask.ErrTaskCenterExecutionReceiptForbidden
	}
	receipt, err := s.lookupTaskOperationRaw(ctx, input.OperationID)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, err
	}
	if receipt.Scope != TaskOperationScopeTask || receipt.TaskID != input.TaskID || receipt.RequestHash != input.RequestHash || !receipt.OwnerOnly {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, domaintask.ErrTaskCenterExecutionReceiptConflict
	}
	var receiptResult struct {
		TaskID          modulecore.TaskID                    `json:"task_id"`
		RunID           modulecore.RunID                     `json:"run_id"`
		ResultReference domaintask.TaskCenterResultReference `json:"result_reference"`
	}
	if err := json.Unmarshal(receipt.Result, &receiptResult); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, domaintask.ErrTaskCenterExecutionReceiptCorrupt
	}
	expectedRunStatus := map[domaintask.Status]domaintask.RunStatus{
		domaintask.StatusSucceeded: domaintask.RunStatusSucceeded,
		domaintask.StatusFailed:    domaintask.RunStatusFailed,
		domaintask.StatusCancelled: domaintask.RunStatusCancelled,
	}[task.Status]
	if receiptResult.TaskID != input.TaskID || receiptResult.RunID != input.RunID || !reflect.DeepEqual(receiptResult.ResultReference, input.ResultReference) {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, domaintask.ErrTaskCenterExecutionReceiptConflict
	}
	if expectedRunStatus == "" || run.Status != expectedRunStatus || run.CompletedAt == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, domaintask.ErrTaskCenterExecutionReceiptCorrupt
	}
	if err := ctx.Err(); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, err
	}
	if err := authorize(ctx, task, run, shared, receiptResult.ResultReference); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, ctxErr
		}
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, domaintask.ErrTaskCenterExecutionReceiptForbidden
	}
	return task, run, receiptResult.ResultReference, nil
}

func (s *JSONLStore) LookupTaskCenterExecutionReceipt(
	ctx context.Context,
	input domaintask.TaskCenterExecutionReceiptReadInput,
	authorize domaintask.TaskCenterExecutionReceiptReadAuthorizationCheck,
) (domaintask.Task, domaintask.Run, error) {
	if s == nil {
		return domaintask.Task{}, domaintask.Run{}, errors.New("Task store is unavailable")
	}
	if ctx == nil {
		return domaintask.Task{}, domaintask.Run{}, errors.New("TaskCenter receipt context is nil")
	}
	if authorize == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskCenterExecutionReceiptAuthorizationRequired
	}
	operationID, requestHash, err := domaintask.BuildTaskCenterExecutionOperationIdentity(input.HandoffKey, input.PreparedRevision, input.Task, input.Shared, input.KindInput)
	if err != nil || operationID != input.OperationID || requestHash != input.RequestHash {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskCenterExecutionReceiptConflict
	}
	if err := ctx.Err(); err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	if err := authorize(ctx, input); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return domaintask.Task{}, domaintask.Run{}, ctxErr
		}
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskCenterExecutionReceiptForbidden
	}
	receipt, err := s.lookupTaskOperationRaw(ctx, input.OperationID)
	if err != nil {
		if errors.Is(err, ErrTaskOperationNotFound) {
			return domaintask.Task{}, domaintask.Run{}, errors.Join(domaintask.ErrTaskCenterExecutionReceiptNotFound, err)
		}
		return domaintask.Task{}, domaintask.Run{}, err
	}
	if effectiveTaskOperationScope(receipt.Scope) != TaskOperationScopeGlobal || receipt.TaskID != "" || !receipt.OwnerOnly || receipt.RequestHash != input.RequestHash {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskCenterExecutionReceiptConflict
	}
	var result struct {
		TaskID modulecore.TaskID `json:"task_id"`
		RunID  modulecore.RunID  `json:"run_id"`
	}
	if err := json.Unmarshal(receipt.Result, &result); err != nil || result.TaskID == "" || result.RunID == "" {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskCenterExecutionReceiptCorrupt
	}
	var task domaintask.Task
	var run domaintask.Run
	var shared domaintask.SharedRoleContext
	err = s.ReadTransaction(ctx, func(store domaintask.Store) error {
		view, ok := store.(*transactionStore)
		if !ok {
			return domaintask.ErrTaskCenterExecutionReceiptUnsupported
		}
		var readErr error
		task, readErr = view.getTaskRaw(ctx, result.TaskID)
		if readErr != nil {
			return readErr
		}
		run, readErr = view.getRunRaw(ctx, result.RunID)
		if readErr != nil {
			return readErr
		}
		shared, readErr = view.getContextRaw(ctx, result.TaskID)
		return readErr
	})
	if err != nil {
		if errors.Is(err, domaintask.ErrNotFound) {
			return domaintask.Task{}, domaintask.Run{}, errors.Join(domaintask.ErrTaskCenterExecutionReceiptCorrupt, err)
		}
		return domaintask.Task{}, domaintask.Run{}, err
	}
	expectedShared := input.Shared
	expectedShared.TaskID = task.TaskID
	if task.Validate() != nil || run.Validate() != nil || !domaintask.TaskCenterDraftMatchesStored(input.Task, task) ||
		run.TaskID != task.TaskID || run.RunID != result.RunID || !reflect.DeepEqual(shared, expectedShared) {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskCenterExecutionReceiptConflict
	}
	if err := ctx.Err(); err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	if err := authorize(ctx, input); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return domaintask.Task{}, domaintask.Run{}, ctxErr
		}
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskCenterExecutionReceiptForbidden
	}
	return task, run, nil
}
