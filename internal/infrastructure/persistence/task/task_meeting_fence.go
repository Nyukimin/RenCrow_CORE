package task

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type taskMeetingSettlementContextKey struct{}

type taskMeetingSettlementPermit struct {
	store  *JSONLStore
	taskID modulecore.TaskID
	runID  modulecore.RunID
	token  *struct{}
}

func taskMeetingSettlementTask(ctx context.Context, store *JSONLStore, snapshot transactionSnapshot) (modulecore.TaskID, bool) {
	if ctx == nil || store == nil || !snapshot.ownerOnlyAccessed || len(snapshot.touched) != 1 {
		return "", false
	}
	permit, ok := ctx.Value(taskMeetingSettlementContextKey{}).(*taskMeetingSettlementPermit)
	if !ok || permit == nil || permit.store != store || permit.taskID == "" || permit.runID == "" || permit.token == nil {
		return "", false
	}
	if _, touched := snapshot.touched[permit.taskID]; !touched || store.executionFence == nil {
		return "", false
	}
	active := store.executionFence.hasActive(map[modulecore.TaskID]struct{}{permit.taskID: {}}, nil)
	if active != permit.taskID {
		return "", false
	}
	status, found := store.knownActiveTaskFenceStatus(permit.taskID)
	if !found || status.State != TaskExecutionFenceStateActive && status.State != TaskExecutionFenceStateUncertain || status.AcquireWriterGeneration != store.writerGeneration {
		return "", false
	}
	return permit.taskID, true
}

func (s *JSONLStore) WithTaskMeetingExecutionFence(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, authorize domaintask.TaskMeetingReadAuthorizationCheck, effect domaintask.TaskMeetingEffect, settleUnknown domaintask.TaskMeetingEffectSettlement) (json.RawMessage, error) {
	if s == nil || ctx == nil || authorize == nil || effect == nil || settleUnknown == nil {
		return nil, domaintask.ErrTaskMeetingOwnerRequired
	}
	task, run, shared, err := s.readTaskMeetingExecutionState(ctx, taskID, runID)
	if err != nil {
		return nil, err
	}
	if !domaintask.IsTaskMeetingTask(task) || task.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeReport && task.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeAnswer || task.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning || run.WriterGeneration == 0 {
		return nil, domaintask.ErrTaskMeetingOwnerRequired
	}
	if err := authorize(ctx, task, run, shared); err != nil {
		return nil, err
	}
	var result json.RawMessage
	err = s.withDurableTaskExecutionFenceSettlement(ctx, taskID, runID, func() error {
		currentTask, currentRun, shared, err := s.readTaskMeetingExecutionState(ctx, taskID, runID)
		if err != nil {
			return err
		}
		if currentTask.TaskMeetingInvocation == nil || !sameTaskMeetingInvocation(*task.TaskMeetingInvocation, *currentTask.TaskMeetingInvocation) || currentRun.RunID != run.RunID || currentRun.WriterGeneration != run.WriterGeneration || currentTask.Status != domaintask.StatusRunning || currentRun.Status != domaintask.RunStatusRunning {
			return domaintask.ErrTaskMeetingConflict
		}
		if err := authorize(ctx, currentTask, currentRun, shared); err != nil {
			return err
		}
		result, err = effect(ctx, currentTask, currentRun, cloneTransactionContext(shared), append(json.RawMessage(nil), currentTask.TaskMeetingInvocation.FrozenInput...))
		if err != nil {
			return err
		}
		if len(result) == 0 || len(result) > domaintask.TaskMeetingMaxFrozenInputBytes || !json.Valid(result) {
			return errors.New("TaskMeeting effect result must be bounded JSON")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return authorize(ctx, currentTask, currentRun, shared)
	}, func(settleCtx context.Context, callbackErr error) error {
		currentTask, currentRun, currentShared, stateErr := s.readTaskMeetingExecutionState(settleCtx, taskID, runID)
		if stateErr != nil {
			return stateErr
		}
		return settleUnknown(settleCtx, currentTask, currentRun, currentShared)
	})
	return append(json.RawMessage(nil), result...), err
}

func (s *JSONLStore) withDurableTaskExecutionFenceSettlement(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, fn func() error, settle func(context.Context, error) error) error {
	if s == nil || ctx == nil || fn == nil || settle == nil || runID.Validate() != nil {
		return domaintask.ErrTaskMeetingOwnerRequired
	}
	if taskID.Validate() != nil || s.readOnly {
		return domaintask.ErrTaskMeetingOwnerRequired
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
	var settlementErr error
	if callbackErr != nil {
		permit := &taskMeetingSettlementPermit{store: s, taskID: taskID, runID: runID, token: &struct{}{}}
		settlementCtx := context.WithValue(context.WithoutCancel(ctx), taskMeetingSettlementContextKey{}, permit)
		settlementErr = settle(settlementCtx, callbackErr)
	}
	if settlementErr != nil {
		// Keep the durable fence active when the unknown outcome could not be
		// recorded. A later owner may reconcile it; a retry cannot call the effect.
		return errors.Join(callbackErr, settlementErr)
	}
	releaseErr := s.persistTaskExecutionFenceRelease(context.WithoutCancel(ctx), taskID, fenceID)
	return errors.Join(callbackErr, releaseErr)
}

func (s *JSONLStore) readTaskMeetingExecutionState(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID) (domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, error) {
	var task domaintask.Task
	var run domaintask.Run
	var shared domaintask.SharedRoleContext
	err := s.ReadTransaction(ctx, func(store domaintask.Store) error {
		tx, ok := store.(*transactionStore)
		if !ok {
			return domaintask.ErrTaskMeetingOwnerRequired
		}
		var err error
		task, run, shared, err = tx.getTaskMeetingStateRaw(ctx, taskID, runID)
		if err != nil {
			return err
		}
		if !domaintask.IsTaskMeetingTask(task) || task.TaskMeetingInvocation.Validate(task) != nil || domaintask.ValidateTaskMeetingConsumedMessages(task, run, shared) != nil {
			return domaintask.ErrTaskMeetingOwnerRequired
		}
		return nil
	})
	return task, run, shared, err
}

func (s *JSONLStore) TaskMeetingExecutionFenceEvidence(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, authorize domaintask.TaskMeetingReadAuthorizationCheck) (domaintask.TaskMeetingExecutionFenceEvidence, error) {
	if s == nil || ctx == nil || authorize == nil || taskID.Validate() != nil || runID.Validate() != nil {
		return domaintask.TaskMeetingExecutionFenceEvidence{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	task, run, shared, err := s.readTaskMeetingExecutionState(ctx, taskID, runID)
	if err != nil {
		return domaintask.TaskMeetingExecutionFenceEvidence{}, err
	}
	if run.TaskID != taskID || run.RunID != runID {
		return domaintask.TaskMeetingExecutionFenceEvidence{}, domaintask.ErrTaskMeetingConflict
	}
	if err := authorize(ctx, task, run, shared); err != nil {
		return domaintask.TaskMeetingExecutionFenceEvidence{}, err
	}
	if status, active := s.knownActiveTaskFenceStatus(taskID); active {
		return taskMeetingFenceEvidence(status), nil
	}
	releaseAdmission, err := s.lifecycle.admit(ctx)
	if err != nil {
		return domaintask.TaskMeetingExecutionFenceEvidence{}, err
	}
	defer releaseAdmission()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.batch == nil {
		return domaintask.TaskMeetingExecutionFenceEvidence{}, errors.New("Task store is unavailable")
	}
	var status TaskExecutionFenceStatus
	err = s.batch.Read(withTxLabel(ctx, "TaskMeetingExecutionFenceActive", taskID), func() error {
		snapshot, readErr := readTaskExecutionFences(ctx, s.fenceLogPaths()...)
		if readErr != nil {
			return readErr
		}
		var statusErr error
		status, statusErr = taskFenceStatusForTask(snapshot, taskID)
		return statusErr
	})
	if errors.Is(err, ErrTaskExecutionFenceNotFound) {
		return domaintask.TaskMeetingExecutionFenceEvidence{}, nil
	}
	if err != nil {
		return domaintask.TaskMeetingExecutionFenceEvidence{}, err
	}
	return taskMeetingFenceEvidence(status), nil
}

func taskMeetingFenceEvidence(status TaskExecutionFenceStatus) domaintask.TaskMeetingExecutionFenceEvidence {
	return domaintask.TaskMeetingExecutionFenceEvidence{
		FenceID: status.FenceID, AcquireWriterGeneration: status.AcquireWriterGeneration,
		ReleaseWriterGeneration: status.ReleaseWriterGeneration,
		Active:                  status.State == TaskExecutionFenceStateActive || status.State == TaskExecutionFenceStateUncertain,
		RecoveryRequired:        status.RecoveryRequired || status.State == TaskExecutionFenceStateUncertain,
	}
}

func (s *JSONLStore) taskMeetingNowUTC() time.Time {
	if s != nil && s.taskMeetingClock != nil {
		return s.taskMeetingClock().UTC()
	}
	return time.Now().UTC()
}

var _ domaintask.TaskMeetingExecutionFencer = (*JSONLStore)(nil)
var _ domaintask.TaskMeetingExecutionFenceStatusReader = (*JSONLStore)(nil)

// RecoverTaskMeetingExecutionFence releases an old LLM effect capability only
// after the Task owner verifies the exact persisted generation-output receipt.
func (s *JSONLStore) RecoverTaskMeetingExecutionFence(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, reference domaintask.TaskMeetingGenerationResultReference, authorize domaintask.TaskMeetingReadAuthorizationCheck) error {
	if s == nil || ctx == nil || authorize == nil || reference.Validate() != nil {
		return domaintask.ErrTaskMeetingOwnerRequired
	}
	if s.readOnly {
		return errors.New("Task store is read-only")
	}
	if taskID.Validate() != nil || runID.Validate() != nil {
		return domaintask.ErrTaskMeetingConflict
	}
	task, run, shared, err := s.readTaskMeetingExecutionState(ctx, taskID, runID)
	if err != nil {
		return err
	}
	if task.TaskMeetingInvocation == nil || task.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeReport && task.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeAnswer || reference.ValidateRun(task, run) != nil {
		return domaintask.ErrTaskMeetingConflict
	}
	if err := authorize(ctx, task, run, shared); err != nil {
		return err
	}
	_, storedReference, outputErr := s.GetTaskMeetingGenerationOutput(ctx, taskID, runID, authorize)
	if outputErr != nil || storedReference != reference {
		if outputErr != nil {
			return outputErr
		}
		return domaintask.ErrTaskMeetingConflict
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
	err = s.batch.Read(withTxLabel(ctx, "RecoverTaskMeetingExecutionFence", taskID), func() error {
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
	if run.Status != domaintask.RunStatusRunning || task.Status != domaintask.StatusRunning || run.TaskID != taskID || run.RunID != runID || run.WriterGeneration != status.AcquireWriterGeneration {
		return domaintask.ErrTaskMeetingConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.persistTaskExecutionFenceRelease(context.WithoutCancel(ctx), taskID, status.FenceID)
}

// RecoverTaskMeetingUnknownExecutionFence releases a retained effect fence
// only after the callback's terminal unknown receipt and failed Task/Run have
// been durably verified. A STARTED receipt by itself is never sufficient.
func (s *JSONLStore) RecoverTaskMeetingUnknownExecutionFence(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, reference domaintask.TaskMeetingGenerationUnknownReference, authorize domaintask.TaskMeetingReadAuthorizationCheck) error {
	if s == nil || ctx == nil || authorize == nil || reference.Validate() != nil || s.readOnly || taskID.Validate() != nil || runID.Validate() != nil || reference.TaskID != taskID || reference.RunID != runID {
		return domaintask.ErrTaskMeetingOwnerRequired
	}
	task, run, shared, err := s.readTaskMeetingExecutionState(ctx, taskID, runID)
	if err != nil {
		return err
	}
	if task.TaskMeetingInvocation == nil || reference.ValidateRun(task, run) != nil || task.Status != domaintask.StatusFailed || run.Status != domaintask.RunStatusFailed || task.Summary != "TaskMeeting generation failed" || task.TaskMeetingResult != nil {
		return domaintask.ErrTaskMeetingConflict
	}
	if err := authorize(ctx, task, run, shared); err != nil {
		return err
	}
	unknown, err := s.GetTaskMeetingGenerationUnknown(ctx, taskID, runID, authorize)
	if err != nil || !sameTaskMeetingUnknownIdentity(unknown, reference) {
		if err != nil {
			return err
		}
		return domaintask.ErrTaskMeetingConflict
	}
	startedOperationID := domaintask.TaskMeetingGenerationEffectStartedReceiptID(taskID, runID)
	startedPayload, startedHash, startedFound, err := s.LookupTaskMeetingOperationReceipt(ctx, startedOperationID, func(checkCtx context.Context) error {
		current, currentRun, currentShared, readErr := s.readTaskMeetingExecutionState(checkCtx, taskID, runID)
		if readErr != nil {
			return readErr
		}
		if current.TaskMeetingInvocation == nil || !sameTaskMeetingInvocation(*task.TaskMeetingInvocation, *current.TaskMeetingInvocation) || currentRun.RunID != runID {
			return domaintask.ErrTaskMeetingConflict
		}
		return authorize(checkCtx, current, currentRun, currentShared)
	})
	if err != nil {
		return err
	}
	if !startedFound {
		return domaintask.ErrTaskMeetingConflict
	}
	var started taskMeetingGenerationStartedReceipt
	if decodeTaskMeetingReceipt(startedPayload, &started) != nil || started.Reference.ValidateRun(task, run) != nil || startedHash != taskMeetingStartedRequestHash(started.Reference) ||
		started.Reference.GenerationRequestID != reference.GenerationRequestID || started.Reference.Purpose != reference.Purpose || started.Reference.ActorID != reference.ActorID ||
		started.Reference.OccurrenceKey != reference.OccurrenceKey || started.Reference.ConfigRevision != reference.ConfigRevision || started.Reference.ConfigSHA256 != reference.ConfigSHA256 ||
		started.Reference.InputSHA256 != reference.InputSHA256 || started.Reference.SourceContextSHA256 != reference.SourceContextSHA256 || started.Reference.GenerationScope != reference.GenerationScope {
		return domaintask.ErrTaskMeetingConflict
	}

	// Acquiring the local task lease proves no callback is currently running
	// in this process. The exact terminal receipt above supplies the durable
	// ended-effect proof across process restart.
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
	var status TaskExecutionFenceStatus
	s.mu.RLock()
	if s.closed || s.batch == nil {
		s.mu.RUnlock()
		return errors.New("Task store batch is unavailable")
	}
	err = s.batch.Read(withTxLabel(ctx, "RecoverTaskMeetingUnknownExecutionFence", taskID), func() error {
		snapshot, readErr := readTaskExecutionFences(ctx, s.fenceLogPaths()...)
		if readErr != nil {
			return readErr
		}
		var statusErr error
		status, statusErr = taskFenceStatusForTask(snapshot, taskID)
		return statusErr
	})
	s.mu.RUnlock()
	if errors.Is(err, ErrTaskExecutionFenceNotFound) || err == nil && status.State == TaskExecutionFenceStateReleased {
		return nil
	}
	if err != nil {
		if errors.Is(err, jsonlbatch.ErrRecoveryRequired) {
			s.fenceMu.Lock()
			s.fenceRecovery = true
			s.fenceMu.Unlock()
			return errors.Join(ErrTaskExecutionFenceRecoveryRequired, err)
		}
		return err
	}
	if status.State != TaskExecutionFenceStateActive && status.State != TaskExecutionFenceStateUncertain {
		return ErrTaskExecutionFenceActive
	}
	generation, err := s.WriterGeneration()
	if err != nil {
		return err
	}
	if status.AcquireWriterGeneration == 0 || status.AcquireWriterGeneration > generation || run.WriterGeneration != status.AcquireWriterGeneration {
		return domaintask.ErrTaskMeetingConflict
	}
	return s.persistTaskExecutionFenceRelease(context.WithoutCancel(ctx), taskID, status.FenceID)
}

var _ domaintask.TaskMeetingExecutionFenceRecovery = (*JSONLStore)(nil)
var _ domaintask.TaskMeetingUnknownFenceRecovery = (*JSONLStore)(nil)
