package taskmanager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type taskMeetingOperationStore interface {
	ExecuteIdempotentTaskMeetingOperation(context.Context, string, string, domaintask.TaskMeetingInvocationOperationAuthorizer, func(Store) (json.RawMessage, error)) (json.RawMessage, error)
	LookupTaskMeetingOperationReceipt(context.Context, string, domaintask.TaskMeetingInvocationOperationAuthorizer) (json.RawMessage, string, bool, error)
}

type taskMeetingReceiptLink struct {
	TaskID modulecore.TaskID `json:"task_id"`
	RunID  modulecore.RunID  `json:"run_id"`
}

// EnsureTaskMeetingRootInvocation creates the stable occurrence root. A
// matching receipt is read before admission is re-evaluated; changed frozen
// input conflicts under the same occurrence identity.
func (m *Manager) EnsureTaskMeetingRootInvocation(
	ctx context.Context,
	request domaintask.TaskMeetingInvocationRequest,
	admit domaintask.TaskMeetingInvocationAdmissionCheck,
	authorizeReceipt domaintask.TaskMeetingInvocationOperationAuthorizer,
	authorizeState domaintask.TaskMeetingReadAuthorizationCheck,
) (domaintask.Task, domaintask.Run, domaintask.TaskMeetingRootProof, error) {
	request = cloneTaskMeetingRequest(request)
	if request.Purpose != domaintask.TaskMeetingPurposeRoot {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskMeetingRootProof{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	task, run, err := m.ensureTaskMeetingInvocation(ctx, request, domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, admit, authorizeReceipt, authorizeState)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskMeetingRootProof{}, err
	}
	proof, err := taskMeetingRootProof(task, run)
	return task, run, proof, err
}

// EnsureTaskMeetingRecipientContext creates a durable context owner for one
// actor. The actual root Task/Run is reloaded and checked against the owner
// proof before any context Task is issued.
func (m *Manager) EnsureTaskMeetingRecipientContext(
	ctx context.Context,
	request domaintask.TaskMeetingInvocationRequest,
	rootProof domaintask.TaskMeetingRootProof,
	admit domaintask.TaskMeetingInvocationAdmissionCheck,
	authorizeRoot domaintask.TaskMeetingReadAuthorizationCheck,
	authorizeReceipt domaintask.TaskMeetingInvocationOperationAuthorizer,
	authorizeState domaintask.TaskMeetingReadAuthorizationCheck,
) (domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, error) {
	request = cloneTaskMeetingRequest(request)
	rootProof = cloneTaskMeetingRootProof(rootProof)
	audienceHash, audienceErr := domaintask.TaskMeetingAudienceSHA256(request.Audience)
	if request.Purpose != domaintask.TaskMeetingPurposeRecipientContext || rootProof.Validate() != nil || request.OccurrenceKey != rootProof.OccurrenceKey || request.ActorID == "" ||
		request.ParentTaskID != rootProof.TaskID || request.ParentRunID != rootProof.RunID || request.CoordinatorActorID != rootProof.CoordinatorActorID ||
		request.ConfiguredOwnerUserID != rootProof.ConfiguredOwnerUserID || request.ConfigRevision != rootProof.ConfigRevision || request.RegistrationEvidenceID != rootProof.RegistrationEvidenceID ||
		rootProof.ConfigSHA256 != taskMeetingConfigDigest(request.FrozenConfig) || audienceErr != nil || rootProof.AudienceSHA256 != audienceHash || !sameTaskMeetingAudience(rootProof.Audience, request.Audience) || !sameTaskMeetingTiming(rootProof.Timing, request.Timing) {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, domaintask.ErrTaskMeetingConflict
	}
	if err := m.verifyTaskMeetingRoot(ctx, rootProof, authorizeRoot); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	rootTask, rootRun, _, err := m.readTaskMeetingState(ctx, rootProof.TaskID, rootProof.RunID, authorizeRoot)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	_, _, rootContext, err := m.readTaskMeetingState(ctx, rootTask.TaskID, rootRun.RunID, authorizeRoot)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	task, run, err := m.ensureTaskMeetingInvocation(ctx, request, rootTask, rootRun, rootContext, admit, authorizeReceipt, authorizeState)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	shared, err := m.readTaskMeetingContext(ctx, task, run, authorizeState)
	return task, run, shared, err
}

func (m *Manager) FindTaskMeetingRecipientContext(
	ctx context.Context,
	request domaintask.TaskMeetingInvocationRequest,
	rootProof domaintask.TaskMeetingRootProof,
	authorizeRoot domaintask.TaskMeetingReadAuthorizationCheck,
	authorizeReceipt domaintask.TaskMeetingInvocationOperationAuthorizer,
	authorizeState domaintask.TaskMeetingReadAuthorizationCheck,
) (domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, error) {
	request = cloneTaskMeetingRequest(request)
	rootProof = cloneTaskMeetingRootProof(rootProof)
	audienceHash, audienceErr := domaintask.TaskMeetingAudienceSHA256(request.Audience)
	if request.Purpose != domaintask.TaskMeetingPurposeRecipientContext || rootProof.Validate() != nil || request.ParentTaskID != rootProof.TaskID || request.ParentRunID != rootProof.RunID || audienceErr != nil || rootProof.AudienceSHA256 != audienceHash || rootProof.ConfigSHA256 != taskMeetingConfigDigest(request.FrozenConfig) || request.RegistrationEvidenceID != rootProof.RegistrationEvidenceID || !sameTaskMeetingTiming(rootProof.Timing, request.Timing) {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, domaintask.ErrTaskMeetingConflict
	}
	if err := m.verifyTaskMeetingRoot(ctx, rootProof, authorizeRoot); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	operationID, _, err := domaintask.TaskMeetingInvocationOperationIdentity(request)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	task, run, err := m.readTaskMeetingInvocation(ctx, operationID, request, authorizeReceipt, authorizeState)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	shared, err := m.readTaskMeetingContext(ctx, task, run, authorizeState)
	return task, run, shared, err
}

func (m *Manager) AppendTaskMeetingConsumedMessage(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	ref domaintask.TaskMeetingMessageReference,
	body string,
	authorize domaintask.TaskMeetingMessageReadAuthorizationCheck,
) (domaintask.TaskMeetingConsumedMessage, error) {
	if m == nil || m.store == nil {
		return domaintask.TaskMeetingConsumedMessage{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	consumer, ok := m.store.(domaintask.TaskMeetingMessageConsumer)
	if !ok || consumer == nil {
		return domaintask.TaskMeetingConsumedMessage{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	return consumer.AppendTaskMeetingConsumedMessage(ctx, taskID, runID, ref, body, authorize)
}

func (m *Manager) FindTaskMeetingConsumedMessage(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	ref domaintask.TaskMeetingMessageReference,
	authorize domaintask.TaskMeetingMessageReadAuthorizationCheck,
) (domaintask.TaskMeetingConsumedMessage, error) {
	if m == nil || m.store == nil {
		return domaintask.TaskMeetingConsumedMessage{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	reader, ok := m.store.(domaintask.TaskMeetingMessageConsumptionReader)
	if !ok || reader == nil {
		return domaintask.TaskMeetingConsumedMessage{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	return reader.FindTaskMeetingConsumedMessage(ctx, taskID, runID, ref, authorize)
}

func (m *Manager) SaveTaskMeetingGenerationOutput(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, output json.RawMessage, authorize domaintask.TaskMeetingReadAuthorizationCheck) (domaintask.TaskMeetingGenerationResultReference, error) {
	if m == nil || m.store == nil || authorize == nil {
		return domaintask.TaskMeetingGenerationResultReference{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	owner, ok := m.store.(domaintask.TaskMeetingGenerationOutputStore)
	if !ok || owner == nil {
		return domaintask.TaskMeetingGenerationResultReference{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	return owner.SaveTaskMeetingGenerationOutput(ctx, taskID, runID, append(json.RawMessage(nil), output...), authorize)
}

func (m *Manager) GetTaskMeetingGenerationOutput(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, authorize domaintask.TaskMeetingReadAuthorizationCheck) (json.RawMessage, domaintask.TaskMeetingGenerationResultReference, error) {
	if m == nil || m.store == nil || authorize == nil {
		return nil, domaintask.TaskMeetingGenerationResultReference{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	owner, ok := m.store.(domaintask.TaskMeetingGenerationOutputStore)
	if !ok || owner == nil {
		return nil, domaintask.TaskMeetingGenerationResultReference{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	output, reference, err := owner.GetTaskMeetingGenerationOutput(ctx, taskID, runID, authorize)
	return append(json.RawMessage(nil), output...), reference, err
}

func (m *Manager) GetTaskMeetingGenerationUnknown(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, authorize domaintask.TaskMeetingReadAuthorizationCheck) (domaintask.TaskMeetingGenerationUnknownReference, error) {
	if m == nil || m.store == nil || ctx == nil || authorize == nil {
		return domaintask.TaskMeetingGenerationUnknownReference{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	owner, ok := m.store.(domaintask.TaskMeetingGenerationUnknownStore)
	if !ok || owner == nil {
		return domaintask.TaskMeetingGenerationUnknownReference{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	return owner.GetTaskMeetingGenerationUnknown(ctx, taskID, runID, authorize)
}

// EnsureTaskMeetingGeneration issues one request-specific report or answer
// Task. RequestID participates in the stable operation identity, while config
// revision and all frozen values are hash-bound to the first receipt.
func (m *Manager) EnsureTaskMeetingGeneration(
	ctx context.Context,
	request domaintask.TaskMeetingInvocationRequest,
	contextTaskID modulecore.TaskID,
	contextRunID modulecore.RunID,
	admit domaintask.TaskMeetingInvocationAdmissionCheck,
	authorizeParent domaintask.TaskMeetingReadAuthorizationCheck,
	authorizeReceipt domaintask.TaskMeetingInvocationOperationAuthorizer,
	authorizeState domaintask.TaskMeetingReadAuthorizationCheck,
) (domaintask.Task, domaintask.Run, error) {
	request = cloneTaskMeetingRequest(request)
	if request.Purpose != domaintask.TaskMeetingPurposeReport && request.Purpose != domaintask.TaskMeetingPurposeAnswer || request.ParentTaskID != contextTaskID || request.ParentRunID != contextRunID {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	parent, parentRun, parentContext, err := m.readTaskMeetingState(ctx, contextTaskID, contextRunID, authorizeParent)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	if parent.TaskMeetingInvocation == nil || parent.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeRecipientContext || parent.Assignee != request.ActorID || parent.TaskMeetingInvocation.OccurrenceKey != request.OccurrenceKey || parent.TaskMeetingInvocation.ConfigSHA256 != taskMeetingConfigDigest(request.FrozenConfig) || parent.TaskMeetingInvocation.ConfigRevision != request.ConfigRevision || parent.TaskMeetingInvocation.RegistrationEvidenceID != request.RegistrationEvidenceID || !sameTaskMeetingTiming(parent.TaskMeetingInvocation.Timing, request.Timing) {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskMeetingConflict
	}
	operationID, requestHash, err := domaintask.TaskMeetingInvocationOperationIdentity(request)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	operationStore, ok := m.store.(taskMeetingOperationStore)
	if !ok {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	_, storedHash, found, err := operationStore.LookupTaskMeetingOperationReceipt(ctx, operationID, authorizeReceipt)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	if found {
		if storedHash != requestHash {
			return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskMeetingConflict
		}
		return m.readTaskMeetingInvocation(ctx, operationID, request, authorizeReceipt, authorizeState)
	}
	contextDigest, err := domaintask.TaskMeetingContextSHA256(parentContext)
	if err != nil || contextDigest != request.SourceContextSHA256 || !sameTaskMeetingMessages(parentContext.TaskMeetingConsumedMessages, request.FrozenSourceMessages) {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskMeetingConflict
	}
	return m.ensureTaskMeetingInvocation(ctx, request, parent, parentRun, parentContext, admit, authorizeReceipt, authorizeState)
}

type TaskMeetingGenerationEffect func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, domaintask.SharedRoleContext, json.RawMessage) (json.RawMessage, error)

// ExecuteTaskMeetingGeneration invokes only the registered typed caller while
// the exact generation Task fence is held. Its source context is reloaded and
// reauthorized inside that fence, and must match the frozen accepted prefix.
func (m *Manager) ExecuteTaskMeetingGeneration(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	authorizeState domaintask.TaskMeetingReadAuthorizationCheck,
	authorizeSource domaintask.TaskMeetingReadAuthorizationCheck,
	authorizeEffect domaintask.TaskMeetingReadAuthorizationCheck,
	effect TaskMeetingGenerationEffect,
) (json.RawMessage, error) {
	if m == nil || m.store == nil || ctx == nil || authorizeState == nil || authorizeSource == nil || authorizeEffect == nil || effect == nil {
		return nil, domaintask.ErrTaskMeetingOwnerRequired
	}
	task, _, _, err := m.readTaskMeetingState(ctx, taskID, runID, authorizeState)
	if err != nil {
		return nil, err
	}
	inv := task.TaskMeetingInvocation
	if inv == nil || inv.Purpose != domaintask.TaskMeetingPurposeReport && inv.Purpose != domaintask.TaskMeetingPurposeAnswer || inv.ParentTaskID == "" || inv.ParentRunID == "" {
		return nil, domaintask.ErrTaskMeetingOwnerRequired
	}
	if existing, _, readErr := m.GetTaskMeetingGenerationOutput(ctx, taskID, runID, authorizeState); readErr == nil {
		return existing, nil
	} else if errors.Is(readErr, domaintask.ErrTaskMeetingExecutionUnknown) {
		return nil, domaintask.ErrTaskMeetingExecutionUnknown
	} else if !errors.Is(readErr, domaintask.ErrTaskMeetingNotFound) {
		return nil, readErr
	}
	unknownStore, ok := m.store.(domaintask.TaskMeetingGenerationUnknownStore)
	if !ok || unknownStore == nil {
		return nil, domaintask.ErrTaskMeetingOwnerRequired
	}
	fencer, ok := m.store.(domaintask.TaskMeetingExecutionFencer)
	if !ok {
		return nil, domaintask.ErrTaskMeetingOwnerRequired
	}
	effectReturned := false
	result, executeErr := fencer.WithTaskMeetingExecutionFence(ctx, taskID, runID, authorizeState, func(effectCtx context.Context, current domaintask.Task, currentRun domaintask.Run, currentShared domaintask.SharedRoleContext, input json.RawMessage) (json.RawMessage, error) {
		if current.TaskID != taskID || currentRun.RunID != runID || currentRun.TaskID != taskID || current.TaskMeetingInvocation == nil || !sameTaskMeetingInvocation(*inv, *current.TaskMeetingInvocation) {
			return nil, domaintask.ErrTaskMeetingConflict
		}
		source, sourceRun, sourceShared, err := m.readTaskMeetingState(effectCtx, inv.ParentTaskID, inv.ParentRunID, authorizeSource)
		if err != nil {
			return nil, err
		}
		if source.TaskMeetingInvocation == nil || source.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeRecipientContext || source.Assignee != inv.ActorID || sourceRun.RunID != inv.ParentRunID || source.TaskID != inv.ParentTaskID {
			return nil, domaintask.ErrTaskMeetingConflict
		}
		if !taskMeetingMessagesHavePrefix(sourceShared.TaskMeetingConsumedMessages, inv.FrozenSourceMessages) {
			return nil, domaintask.ErrTaskMeetingConflict
		}
		frozenSource := domaintask.SharedRoleContext{TaskID: source.TaskID, UpdatedAt: sourceShared.UpdatedAt, TaskMeetingConsumedMessages: append([]domaintask.TaskMeetingConsumedMessage(nil), inv.FrozenSourceMessages...)}
		input = append(json.RawMessage(nil), input...)
		currentContext := cloneTaskMeetingShared(currentShared)
		existing, _, readErr := m.GetTaskMeetingGenerationOutput(effectCtx, taskID, runID, authorizeState)
		if readErr == nil {
			return existing, nil
		}
		if !errors.Is(readErr, domaintask.ErrTaskMeetingNotFound) {
			return nil, readErr
		}
		writerGeneration, generationErr := m.store.WriterGeneration()
		if generationErr != nil {
			return nil, generationErr
		}
		if currentRun.WriterGeneration == 0 || currentRun.WriterGeneration != writerGeneration {
			return nil, domaintask.ErrTaskMeetingExecutionUnknown
		}
		if !current.TaskMeetingInvocation.Timing.Ready(m.now()) {
			return nil, domaintask.ErrTaskMeetingNotReady
		}
		if err := authorizeEffect(effectCtx, current, currentRun, currentContext); err != nil {
			return nil, err
		}
		started, err := unknownStore.StartTaskMeetingGenerationEffect(effectCtx, taskID, runID, authorizeState)
		if err != nil {
			return nil, err
		}
		if !started {
			// The prior start receipt is visible only after any prior local
			// execution fence has ended. Its missing output is terminal unknown.
			effectReturned = true
			return nil, domaintask.ErrTaskMeetingExecutionUnknown
		}
		generated, err := effect(effectCtx, current, currentRun, currentContext, frozenSource, input)
		effectReturned = true
		if err != nil {
			return nil, err
		}
		if len(generated) == 0 || len(generated) > domaintask.TaskMeetingMaxFrozenInputBytes || !json.Valid(generated) {
			return nil, domaintask.ErrTaskMeetingConflict
		}
		if _, err := m.SaveTaskMeetingGenerationOutput(effectCtx, taskID, runID, generated, authorizeState); err != nil {
			return nil, err
		}
		return append(json.RawMessage(nil), generated...), nil
	}, func(settleCtx context.Context, current domaintask.Task, currentRun domaintask.Run, _ domaintask.SharedRoleContext) error {
		if !effectReturned {
			return nil
		}
		if _, _, outputErr := m.GetTaskMeetingGenerationOutput(settleCtx, taskID, runID, authorizeState); outputErr == nil || errors.Is(outputErr, domaintask.ErrTaskMeetingExecutionUnknown) {
			return nil
		} else if !errors.Is(outputErr, domaintask.ErrTaskMeetingNotFound) {
			return outputErr
		}
		inv := current.TaskMeetingInvocation
		if inv == nil || current.Status != domaintask.StatusRunning || currentRun.Status != domaintask.RunStatusRunning {
			return domaintask.ErrTaskMeetingConflict
		}
		unknown := domaintask.TaskMeetingGenerationUnknownReference{
			TaskID: taskID, RunID: runID, ReceiptID: domaintask.TaskMeetingGenerationUnknownReceiptID(taskID, runID),
			GenerationRequestID: inv.GenerationRequestID, Purpose: inv.Purpose, ActorID: inv.ActorID, OccurrenceKey: inv.OccurrenceKey,
			ConfigRevision: inv.ConfigRevision, ConfigSHA256: inv.ConfigSHA256, InputSHA256: inv.InputSHA256,
			SourceContextSHA256: inv.SourceContextSHA256, GenerationScope: inv.GenerationScope,
			Reason: domaintask.TaskMeetingGenerationUnknownReasonEffectReturned, EvidenceID: modulecore.NewEvidenceID(), RecordedAt: m.now().UTC(),
		}
		unknownAuthorize := domaintask.TaskMeetingStateAuthorizationCheck(authorizeState)
		if err := unknownStore.RecordTaskMeetingGenerationUnknown(settleCtx, unknown, unknownAuthorize); err != nil {
			// The callback has ended and the effect must never be replayed. Retry
			// only the idempotent owner receipt write so a lost response or a
			// transient storage failure can still terminalize this generation.
			if retryErr := unknownStore.RecordTaskMeetingGenerationUnknown(settleCtx, unknown, unknownAuthorize); retryErr != nil {
				return errors.Join(err, retryErr)
			}
			return nil
		}
		return nil
	})
	if executeErr != nil {
		if existing, _, readErr := m.GetTaskMeetingGenerationOutput(context.WithoutCancel(ctx), taskID, runID, authorizeState); readErr == nil {
			return existing, nil
		} else if errors.Is(readErr, domaintask.ErrTaskMeetingExecutionUnknown) || effectReturned {
			return nil, errors.Join(domaintask.ErrTaskMeetingExecutionUnknown, executeErr)
		}
		return nil, executeErr
	}
	return result, nil
}

// ReadTaskMeetingGenerationResult recovers the exact Task-owner output receipt
// after finish or process restart. It never runs the LLM effect.
func (m *Manager) ReadTaskMeetingGenerationResult(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, authorizeState domaintask.TaskMeetingReadAuthorizationCheck, authorizeSource domaintask.TaskMeetingReadAuthorizationCheck) (json.RawMessage, domaintask.TaskMeetingGenerationResultReference, error) {
	if m == nil || m.store == nil || ctx == nil || authorizeState == nil || authorizeSource == nil {
		return nil, domaintask.TaskMeetingGenerationResultReference{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	task, _, _, err := m.readTaskMeetingState(ctx, taskID, runID, authorizeState)
	if err != nil {
		return nil, domaintask.TaskMeetingGenerationResultReference{}, err
	}
	inv := task.TaskMeetingInvocation
	if inv == nil || inv.Purpose != domaintask.TaskMeetingPurposeReport && inv.Purpose != domaintask.TaskMeetingPurposeAnswer {
		return nil, domaintask.TaskMeetingGenerationResultReference{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	source, sourceRun, sourceShared, err := m.readTaskMeetingState(ctx, inv.ParentTaskID, inv.ParentRunID, authorizeSource)
	if err != nil {
		return nil, domaintask.TaskMeetingGenerationResultReference{}, err
	}
	if source.TaskMeetingInvocation == nil || source.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeRecipientContext || source.Assignee != inv.ActorID || sourceRun.RunID != inv.ParentRunID || !taskMeetingMessagesHavePrefix(sourceShared.TaskMeetingConsumedMessages, inv.FrozenSourceMessages) {
		return nil, domaintask.TaskMeetingGenerationResultReference{}, domaintask.ErrTaskMeetingConflict
	}
	return m.GetTaskMeetingGenerationOutput(ctx, taskID, runID, authorizeState)
}

// FinishTaskMeetingInvocation pins the exact canonical EventStore message
// reference before the Task/Run can become successful. Both external owner
// checks happen outside the Task transaction; the transaction callback below
// compares only the local immutable Task snapshot.
func (m *Manager) FinishTaskMeetingInvocation(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, reference domaintask.TaskMeetingGenerationResultReference, authorize domaintask.TaskMeetingStateAuthorizationCheck, verifyResult domaintask.TaskMeetingGenerationResultCheck) error {
	if m == nil || m.store == nil || ctx == nil || authorize == nil || verifyResult == nil || reference.Validate() != nil {
		return domaintask.ErrTaskMeetingOwnerRequired
	}
	task, run, shared, err := m.readTaskMeetingState(ctx, taskID, runID, domaintask.TaskMeetingReadAuthorizationCheck(authorize))
	if err != nil {
		return err
	}
	if err := authorize(ctx, task, run, shared); err != nil {
		return err
	}
	if task.TaskMeetingInvocation == nil || task.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeReport && task.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeAnswer ||
		reference.ValidateRun(task, run) != nil || task.TaskID != taskID || run.TaskID != taskID || run.RunID != runID {
		return domaintask.ErrTaskMeetingConflict
	}
	_, storedReference, outputErr := m.GetTaskMeetingGenerationOutput(ctx, taskID, runID, domaintask.TaskMeetingReadAuthorizationCheck(authorize))
	if outputErr != nil || storedReference != reference {
		if outputErr != nil {
			return outputErr
		}
		return domaintask.ErrTaskMeetingConflict
	}
	if err := verifyResult(ctx, task, run, shared, reference); err != nil {
		return err
	}
	if task.Status == domaintask.StatusSucceeded && run.Status == domaintask.RunStatusSucceeded {
		if task.TaskMeetingResult == nil || *task.TaskMeetingResult != reference {
			return domaintask.ErrTaskMeetingConflict
		}
		return nil
	}
	if task.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning {
		return domaintask.ErrTaskMeetingConflict
	}
	now := m.now().UTC()
	const summary = "TaskMeeting generation completed"
	closedRun, err := run.Close(domaintask.RunStatusSucceeded, now, summary)
	if err != nil {
		return err
	}
	task.Status, task.Summary, task.UpdatedAt = domaintask.StatusSucceeded, summary, now
	task.TaskMeetingResult = &reference
	finished := now
	task.FinishedAt = &finished
	localCAS := func(_ context.Context, saved domaintask.Task, savedRun domaintask.Run, _ domaintask.SharedRoleContext) error {
		if saved.TaskID != taskID || savedRun.TaskID != taskID || savedRun.RunID != runID || saved.Status != domaintask.StatusSucceeded || savedRun.Status != domaintask.RunStatusSucceeded ||
			saved.TaskMeetingInvocation == nil || !sameTaskMeetingInvocation(*saved.TaskMeetingInvocation, *task.TaskMeetingInvocation) || saved.TaskMeetingResult == nil || *saved.TaskMeetingResult != reference {
			return domaintask.ErrTaskMeetingConflict
		}
		return nil
	}
	if err := m.store.Transaction(ctx, func(tx Store) error {
		writer, ok := tx.(domaintask.TaskMeetingInvocationWriter)
		if !ok {
			return domaintask.ErrTaskMeetingOwnerRequired
		}
		return writer.SaveTaskMeetingInvocationState(ctx, task, closedRun, &shared, localCAS)
	}); err != nil {
		return err
	}
	closed, closedRun, _, err := m.readTaskMeetingState(ctx, taskID, runID, domaintask.TaskMeetingReadAuthorizationCheck(authorize))
	if err != nil {
		return err
	}
	if closed.Status != domaintask.StatusSucceeded || closedRun.Status != domaintask.RunStatusSucceeded || closed.TaskMeetingResult == nil || *closed.TaskMeetingResult != reference {
		return domaintask.ErrTaskMeetingConflict
	}
	return verifyResult(ctx, closed, closedRun, shared, reference)
}

func (m *Manager) RecoverTaskMeetingGenerationFence(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, reference domaintask.TaskMeetingGenerationResultReference, authorize domaintask.TaskMeetingReadAuthorizationCheck, verifyResult domaintask.TaskMeetingGenerationResultCheck) error {
	if m == nil || m.store == nil {
		return domaintask.ErrTaskMeetingOwnerRequired
	}
	recovery, ok := m.store.(domaintask.TaskMeetingExecutionFenceRecovery)
	if !ok || recovery == nil {
		return domaintask.ErrTaskMeetingOwnerRequired
	}
	_, storedReference, err := m.GetTaskMeetingGenerationOutput(ctx, taskID, runID, authorize)
	if err != nil {
		return err
	}
	task, run, shared, err := m.readTaskMeetingState(ctx, taskID, runID, authorize)
	if err != nil {
		return err
	}
	if storedReference != reference || verifyResult == nil {
		return domaintask.ErrTaskMeetingConflict
	}
	if err := verifyResult(ctx, task, run, shared, reference); err != nil {
		return err
	}
	return recovery.RecoverTaskMeetingExecutionFence(ctx, taskID, runID, reference, authorize)
}

// RecoverTaskMeetingUnknownGenerationFence releases a retained fence only
// when the native owner can verify its exact terminal unknown receipt.
func (m *Manager) RecoverTaskMeetingUnknownGenerationFence(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, reference domaintask.TaskMeetingGenerationUnknownReference, authorize domaintask.TaskMeetingReadAuthorizationCheck) error {
	if m == nil || m.store == nil || ctx == nil || authorize == nil || reference.Validate() != nil || reference.TaskID != taskID || reference.RunID != runID {
		return domaintask.ErrTaskMeetingOwnerRequired
	}
	recovery, ok := m.store.(domaintask.TaskMeetingUnknownFenceRecovery)
	if !ok || recovery == nil {
		return domaintask.ErrTaskMeetingOwnerRequired
	}
	unknown, err := m.GetTaskMeetingGenerationUnknown(ctx, taskID, runID, authorize)
	if err != nil {
		return err
	}
	if unknown != reference {
		return domaintask.ErrTaskMeetingConflict
	}
	task, run, _, err := m.readTaskMeetingState(ctx, taskID, runID, authorize)
	if err != nil {
		return err
	}
	if reference.ValidateRun(task, run) != nil || task.Status != domaintask.StatusFailed || run.Status != domaintask.RunStatusFailed || task.TaskMeetingResult != nil {
		return domaintask.ErrTaskMeetingConflict
	}
	return recovery.RecoverTaskMeetingUnknownExecutionFence(ctx, taskID, runID, reference, authorize)
}

// CompleteTaskMeetingRootInvocation closes the root only after the registered
// owner proves the complete occurrence aggregate.
func (m *Manager) CompleteTaskMeetingRootInvocation(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, authorize domaintask.TaskMeetingReadAuthorizationCheck, verifyAggregate domaintask.TaskMeetingAggregateProofCheck) error {
	return m.FinishTaskMeetingControlInvocation(ctx, taskID, runID, domaintask.TaskMeetingPurposeRoot, domaintask.StatusSucceeded, authorize, verifyAggregate)
}

// CompleteTaskMeetingRecipientContext closes one durable actor context only
// after the aggregate owner proves that actor's delivery/report work is done.
func (m *Manager) CompleteTaskMeetingRecipientContext(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, authorize domaintask.TaskMeetingReadAuthorizationCheck, verifyAggregate domaintask.TaskMeetingAggregateProofCheck) error {
	return m.FinishTaskMeetingControlInvocation(ctx, taskID, runID, domaintask.TaskMeetingPurposeRecipientContext, domaintask.StatusSucceeded, authorize, verifyAggregate)
}

func (m *Manager) FailTaskMeetingRootInvocation(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, authorize domaintask.TaskMeetingReadAuthorizationCheck, proveOutcome domaintask.TaskMeetingAggregateProofCheck) error {
	return m.FinishTaskMeetingControlInvocation(ctx, taskID, runID, domaintask.TaskMeetingPurposeRoot, domaintask.StatusFailed, authorize, proveOutcome)
}

func (m *Manager) FailTaskMeetingRecipientContext(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, authorize domaintask.TaskMeetingReadAuthorizationCheck, proveOutcome domaintask.TaskMeetingAggregateProofCheck) error {
	return m.FinishTaskMeetingControlInvocation(ctx, taskID, runID, domaintask.TaskMeetingPurposeRecipientContext, domaintask.StatusFailed, authorize, proveOutcome)
}

// FinishTaskMeetingControlInvocation finalizes root/context control Tasks only
// after an external owner proves the exact terminal aggregate outcome. The
// proof executes outside the Task transaction; its callback in the transaction
// checks only the locally frozen Task/Run snapshot.
func (m *Manager) FinishTaskMeetingControlInvocation(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, purpose domaintask.TaskMeetingPurpose, outcome domaintask.Status, authorize domaintask.TaskMeetingReadAuthorizationCheck, prove domaintask.TaskMeetingAggregateProofCheck) error {
	if m == nil || m.store == nil || ctx == nil || authorize == nil || prove == nil ||
		(purpose != domaintask.TaskMeetingPurposeRoot && purpose != domaintask.TaskMeetingPurposeRecipientContext) ||
		(outcome != domaintask.StatusSucceeded && outcome != domaintask.StatusFailed) {
		return domaintask.ErrTaskMeetingOwnerRequired
	}
	task, run, shared, err := m.readTaskMeetingState(ctx, taskID, runID, authorize)
	if err != nil {
		return err
	}
	if task.TaskMeetingInvocation == nil || task.TaskMeetingInvocation.Purpose != purpose || task.TaskID != taskID || run.TaskID != taskID || run.RunID != runID {
		return domaintask.ErrTaskMeetingConflict
	}
	if err := prove(ctx, task, run, shared, outcome); err != nil {
		return err
	}
	statusSummary := map[domaintask.TaskMeetingPurpose]map[domaintask.Status]string{
		domaintask.TaskMeetingPurposeRoot:             {domaintask.StatusSucceeded: "TaskMeeting occurrence completed", domaintask.StatusFailed: "TaskMeeting occurrence did not complete"},
		domaintask.TaskMeetingPurposeRecipientContext: {domaintask.StatusSucceeded: "TaskMeeting recipient context completed", domaintask.StatusFailed: "TaskMeeting recipient context did not complete"},
	}
	summary := statusSummary[purpose][outcome]
	runOutcome := domaintask.RunStatusSucceeded
	if outcome == domaintask.StatusFailed {
		runOutcome = domaintask.RunStatusFailed
	}
	if task.Status == outcome && run.Status == runOutcome && task.Summary == summary {
		return nil
	}
	if task.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning {
		return domaintask.ErrTaskMeetingConflict
	}
	now := m.now().UTC()
	closedRun, err := run.Close(runOutcome, now, summary)
	if err != nil {
		return err
	}
	task.Status, task.Summary, task.UpdatedAt = outcome, summary, now
	finished := now
	task.FinishedAt = &finished
	localCAS := func(_ context.Context, saved domaintask.Task, savedRun domaintask.Run, _ domaintask.SharedRoleContext) error {
		if saved.TaskID != taskID || savedRun.TaskID != taskID || savedRun.RunID != runID || saved.Status != outcome || savedRun.Status != runOutcome || saved.Summary != summary ||
			saved.TaskMeetingInvocation == nil || !sameTaskMeetingInvocation(*saved.TaskMeetingInvocation, *task.TaskMeetingInvocation) {
			return domaintask.ErrTaskMeetingConflict
		}
		return nil
	}
	if err := m.store.Transaction(ctx, func(tx Store) error {
		writer, ok := tx.(domaintask.TaskMeetingInvocationWriter)
		if !ok {
			return domaintask.ErrTaskMeetingOwnerRequired
		}
		return writer.SaveTaskMeetingInvocationState(ctx, task, closedRun, &shared, localCAS)
	}); err != nil {
		return err
	}
	closed, closedRun, closedShared, err := m.readTaskMeetingState(ctx, taskID, runID, authorize)
	if err != nil {
		return err
	}
	return prove(ctx, closed, closedRun, closedShared, outcome)
}

func (m *Manager) FindTaskMeetingRootInvocation(
	ctx context.Context,
	request domaintask.TaskMeetingInvocationRequest,
	authorizeReceipt domaintask.TaskMeetingInvocationOperationAuthorizer,
	authorizeState domaintask.TaskMeetingReadAuthorizationCheck,
) (domaintask.Task, domaintask.Run, domaintask.TaskMeetingRootProof, error) {
	request = cloneTaskMeetingRequest(request)
	if request.Purpose != domaintask.TaskMeetingPurposeRoot {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskMeetingRootProof{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	operationID, requestHash, err := domaintask.TaskMeetingInvocationOperationIdentity(request)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskMeetingRootProof{}, err
	}
	store, ok := m.store.(taskMeetingOperationStore)
	if !ok {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskMeetingRootProof{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	_, storedHash, found, err := store.LookupTaskMeetingOperationReceipt(ctx, operationID, authorizeReceipt)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskMeetingRootProof{}, err
	}
	if !found {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskMeetingRootProof{}, domaintask.ErrTaskMeetingNotFound
	}
	if storedHash != requestHash {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskMeetingRootProof{}, domaintask.ErrTaskMeetingConflict
	}
	task, run, err := m.readTaskMeetingInvocation(ctx, operationID, request, authorizeReceipt, authorizeState)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskMeetingRootProof{}, err
	}
	proof, err := taskMeetingRootProof(task, run)
	return task, run, proof, err
}

// FindTaskMeetingRootByOccurrence performs receipt-first recovery without
// requiring the caller to rebuild current configuration or frozen input.
func (m *Manager) FindTaskMeetingRootByOccurrence(ctx context.Context, occurrenceKey, coordinatorActorID string, authorizeReceipt domaintask.TaskMeetingInvocationOperationAuthorizer, authorizeState domaintask.TaskMeetingReadAuthorizationCheck) (domaintask.Task, domaintask.Run, domaintask.TaskMeetingRootProof, error) {
	operationID, err := domaintask.TaskMeetingInvocationOperationID(occurrenceKey, domaintask.TaskMeetingPurposeRoot, coordinatorActorID, "")
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskMeetingRootProof{}, err
	}
	task, run, _, err := m.findTaskMeetingInvocationByIdentity(ctx, operationID, occurrenceKey, domaintask.TaskMeetingPurposeRoot, coordinatorActorID, authorizeReceipt, authorizeState)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskMeetingRootProof{}, err
	}
	proof, err := taskMeetingRootProof(task, run)
	return task, run, proof, err
}

// FindTaskMeetingRecipientContextByActor recovers a frozen actor context by
// stable occurrence/actor identity before the caller derives any live prompt.
func (m *Manager) FindTaskMeetingRecipientContextByActor(ctx context.Context, rootProof domaintask.TaskMeetingRootProof, actorID string, authorizeRoot, authorizeState domaintask.TaskMeetingReadAuthorizationCheck, authorizeReceipt domaintask.TaskMeetingInvocationOperationAuthorizer) (domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, error) {
	if rootProof.Validate() != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, domaintask.ErrTaskMeetingConflict
	}
	if err := m.verifyTaskMeetingRoot(ctx, rootProof, authorizeRoot); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	operationID, err := domaintask.TaskMeetingInvocationOperationID(rootProof.OccurrenceKey, domaintask.TaskMeetingPurposeRecipientContext, actorID, "")
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	task, run, shared, err := m.findTaskMeetingInvocationByIdentity(ctx, operationID, rootProof.OccurrenceKey, domaintask.TaskMeetingPurposeRecipientContext, actorID, authorizeReceipt, authorizeState)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	inv := task.TaskMeetingInvocation
	if inv.ParentTaskID != rootProof.TaskID || inv.ParentRunID != rootProof.RunID || inv.CoordinatorActorID != rootProof.CoordinatorActorID || inv.ConfiguredOwnerUserID != rootProof.ConfiguredOwnerUserID || inv.ConfigRevision != rootProof.ConfigRevision || inv.ConfigSHA256 != rootProof.ConfigSHA256 || inv.RegistrationEvidenceID != rootProof.RegistrationEvidenceID || inv.AudienceSHA256 != rootProof.AudienceSHA256 {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, domaintask.ErrTaskMeetingConflict
	}
	return task, run, shared, nil
}

// FindTaskMeetingGenerationInvocation resolves the exact stored frozen input
// using only occurrence, actor, purpose and the coordinator's stable request
// ID. It validates the persisted source-context prefix but never rebuilds it.
func (m *Manager) FindTaskMeetingGenerationInvocation(ctx context.Context, occurrenceKey string, purpose domaintask.TaskMeetingPurpose, actorID string, requestID modulecore.RequestID, authorizeReceipt domaintask.TaskMeetingInvocationOperationAuthorizer, authorizeState, authorizeSource domaintask.TaskMeetingReadAuthorizationCheck) (domaintask.Task, domaintask.Run, error) {
	if purpose != domaintask.TaskMeetingPurposeReport && purpose != domaintask.TaskMeetingPurposeAnswer || authorizeSource == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	operationID, err := domaintask.TaskMeetingInvocationOperationID(occurrenceKey, purpose, actorID, requestID)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	task, run, _, err := m.findTaskMeetingInvocationByIdentity(ctx, operationID, occurrenceKey, purpose, actorID, authorizeReceipt, authorizeState)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	inv := task.TaskMeetingInvocation
	source, sourceRun, sourceContext, err := m.readTaskMeetingState(ctx, inv.ParentTaskID, inv.ParentRunID, authorizeSource)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	if source.TaskMeetingInvocation == nil || source.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeRecipientContext || source.Assignee != actorID || sourceRun.RunID != inv.ParentRunID || !taskMeetingMessagesHavePrefix(sourceContext.TaskMeetingConsumedMessages, inv.FrozenSourceMessages) {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskMeetingConflict
	}
	return task, run, nil
}

func (m *Manager) findTaskMeetingInvocationByIdentity(ctx context.Context, operationID, occurrenceKey string, purpose domaintask.TaskMeetingPurpose, actorID string, authorizeReceipt domaintask.TaskMeetingInvocationOperationAuthorizer, authorizeState domaintask.TaskMeetingReadAuthorizationCheck) (domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, error) {
	if m == nil || m.store == nil || ctx == nil || authorizeReceipt == nil || authorizeState == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	store, ok := m.store.(taskMeetingOperationStore)
	if !ok {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	result, storedHash, found, err := store.LookupTaskMeetingOperationReceipt(ctx, operationID, authorizeReceipt)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	if !found {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, domaintask.ErrTaskMeetingNotFound
	}
	var link taskMeetingReceiptLink
	if json.Unmarshal(result, &link) != nil || link.TaskID.Validate() != nil || link.RunID.Validate() != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, domaintask.ErrTaskMeetingConflict
	}
	task, run, shared, err := m.readTaskMeetingState(ctx, link.TaskID, link.RunID, authorizeState)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	inv := task.TaskMeetingInvocation
	if inv == nil || inv.OccurrenceKey != occurrenceKey || inv.Purpose != purpose || inv.ActorID != actorID {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, domaintask.ErrTaskMeetingConflict
	}
	request := domaintask.TaskMeetingInvocationRequest{
		OccurrenceKey: inv.OccurrenceKey, Purpose: inv.Purpose, CoordinatorActorID: inv.CoordinatorActorID, ActorID: inv.ActorID,
		ConfiguredOwnerUserID: inv.ConfiguredOwnerUserID, ConfigRevision: inv.ConfigRevision, RegistrationEvidenceID: inv.RegistrationEvidenceID,
		FrozenConfig: append(json.RawMessage(nil), inv.FrozenConfig...), Audience: append([]string(nil), inv.Audience...), FrozenInput: append(json.RawMessage(nil), inv.FrozenInput...),
		SourceContextSHA256: inv.SourceContextSHA256, FrozenSourceMessages: append([]domaintask.TaskMeetingConsumedMessage(nil), inv.FrozenSourceMessages...),
		GenerationScope: inv.GenerationScope, GenerationRequestID: inv.GenerationRequestID, Timing: cloneTaskMeetingTiming(inv.Timing), ParentTaskID: inv.ParentTaskID, ParentRunID: inv.ParentRunID,
	}
	actualOperationID, actualHash, hashErr := domaintask.TaskMeetingInvocationOperationIdentity(request)
	if hashErr != nil || actualOperationID != operationID || actualHash != storedHash {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, domaintask.ErrTaskMeetingConflict
	}
	return task, run, shared, nil
}

func (m *Manager) ensureTaskMeetingInvocation(
	ctx context.Context,
	request domaintask.TaskMeetingInvocationRequest,
	parent domaintask.Task,
	parentRun domaintask.Run,
	parentContext domaintask.SharedRoleContext,
	admit domaintask.TaskMeetingInvocationAdmissionCheck,
	authorizeReceipt domaintask.TaskMeetingInvocationOperationAuthorizer,
	authorizeState domaintask.TaskMeetingReadAuthorizationCheck,
) (domaintask.Task, domaintask.Run, error) {
	request = cloneTaskMeetingRequest(request)
	if m == nil || m.store == nil || ctx == nil || admit == nil || authorizeReceipt == nil || authorizeState == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	if err := request.Validate(); err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	operationID, requestHash, err := domaintask.TaskMeetingInvocationOperationIdentity(request)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	store, ok := m.store.(taskMeetingOperationStore)
	if !ok {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	if _, storedHash, found, lookupErr := store.LookupTaskMeetingOperationReceipt(ctx, operationID, authorizeReceipt); lookupErr != nil {
		return domaintask.Task{}, domaintask.Run{}, lookupErr
	} else if found {
		if storedHash != requestHash {
			return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskMeetingConflict
		}
		return m.readTaskMeetingInvocation(ctx, operationID, request, authorizeReceipt, authorizeState)
	}
	if (request.Purpose == domaintask.TaskMeetingPurposeReport || request.Purpose == domaintask.TaskMeetingPurposeAnswer) && !request.Timing.Ready(m.now()) {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskMeetingNotReady
	}
	if err := admit(ctx, request, parent, parentRun, cloneTaskMeetingShared(parentContext)); err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	result, err := store.ExecuteIdempotentTaskMeetingOperation(ctx, operationID, requestHash, authorizeReceipt, func(tx Store) (json.RawMessage, error) {
		writer, ok := tx.(domaintask.TaskMeetingInvocationWriter)
		if !ok {
			return nil, domaintask.ErrTaskMeetingOwnerRequired
		}
		now := m.now().UTC()
		taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
		if (request.Purpose == domaintask.TaskMeetingPurposeReport || request.Purpose == domaintask.TaskMeetingPurposeAnswer) && m.limits.Global > 0 {
			candidateTiming := cloneTaskMeetingTiming(request.Timing)
			candidate := domaintask.Task{TaskID: taskID, Title: taskMeetingTitle(request.Purpose, request.ActorID), Route: domaintask.RouteGeneral, Assignee: request.ActorID, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, ReadOnly: true, CreatedAt: now, UpdatedAt: now, Timing: &candidateTiming}
			admissionManager := &Manager{store: tx, limits: m.limits, expectedCriteriaRevision: m.expectedCriteriaRevision, idIndexed: m.idIndexed, now: m.now}
			allowed, reason, admissionErr := admissionManager.canStartInTransaction(ctx, candidate)
			if admissionErr != nil {
				return nil, admissionErr
			}
			if !allowed {
				return nil, fmt.Errorf("%w: %s", ErrParallelLimit, reason)
			}
		}
		admissionEvidenceID := modulecore.NewEvidenceID()
		for admissionEvidenceID == request.RegistrationEvidenceID {
			admissionEvidenceID = modulecore.NewEvidenceID()
		}
		configHash := taskMeetingConfigDigest(request.FrozenConfig)
		inputDigest := taskMeetingConfigDigest(request.FrozenInput)
		audienceHash, err := domaintask.TaskMeetingAudienceSHA256(request.Audience)
		if err != nil {
			return nil, err
		}
		invocation := &domaintask.TaskMeetingInvocation{
			OccurrenceKey: request.OccurrenceKey, Purpose: request.Purpose, CoordinatorActorID: request.CoordinatorActorID,
			ActorID: request.ActorID, ConfiguredOwnerUserID: request.ConfiguredOwnerUserID, ConfigRevision: request.ConfigRevision,
			ConfigSHA256: configHash, RegistrationEvidenceID: request.RegistrationEvidenceID,
			FrozenConfig: append(json.RawMessage(nil), request.FrozenConfig...), Audience: append([]string(nil), request.Audience...),
			AudienceSHA256: audienceHash, InputSHA256: inputDigest, FrozenInput: append(json.RawMessage(nil), request.FrozenInput...),
			FrozenSourceMessages: append([]domaintask.TaskMeetingConsumedMessage(nil), request.FrozenSourceMessages...),
			AdmissionEvidenceID:  admissionEvidenceID, GenerationRequestID: request.GenerationRequestID, Timing: request.Timing,
			SourceContextSHA256: request.SourceContextSHA256,
			GenerationScope:     request.GenerationScope,
			ParentTaskID:        request.ParentTaskID, ParentRunID: request.ParentRunID,
		}
		started := now
		status, runStatus := domaintask.StatusRunning, domaintask.RunStatusRunning
		task := domaintask.Task{
			TaskID: taskID, Title: taskMeetingTitle(request.Purpose, request.ActorID), Route: domaintask.RouteGeneral,
			OwnerID: request.ActorID, Assignee: request.ActorID, Status: status, Priority: domaintask.PriorityNormal,
			ParentTaskID: request.ParentTaskID, ReadOnly: true, InterruptPolicy: domaintask.InterruptSilent,
			CreatedAt: now, UpdatedAt: now, StartedAt: &started, Timing: taskMeetingTimingPtr(request.Timing),
			TaskMeetingInvocation: invocation,
		}
		generation, err := tx.WriterGeneration()
		if err != nil || generation == 0 {
			return nil, fmt.Errorf("TaskMeeting writer generation unavailable: %w", err)
		}
		run := domaintask.Run{WriterGeneration: generation, RunID: runID, TaskID: taskID, StartReason: domaintask.RunStartReasonFirst, Assignee: request.ActorID, Status: runStatus, StartedAt: now}
		if err := task.Validate(); err != nil {
			return nil, fmt.Errorf("TaskMeeting Task: %w", err)
		}
		if err := run.Validate(); err != nil {
			return nil, fmt.Errorf("TaskMeeting Run: %w", err)
		}
		shared := domaintask.SharedRoleContext{TaskID: taskID, UpdatedAt: now}
		stateAuth := func(_ context.Context, saved domaintask.Task, savedRun domaintask.Run, savedContext domaintask.SharedRoleContext) error {
			if saved.TaskMeetingInvocation == nil || saved.TaskMeetingInvocation.AdmissionEvidenceID != admissionEvidenceID || saved.TaskID != taskID || savedRun.TaskID != taskID || savedRun.RunID != runID || savedContext.TaskID != taskID {
				return domaintask.ErrTaskMeetingConflict
			}
			return nil
		}
		if err := writer.SaveTaskMeetingInvocationState(ctx, task, run, &shared, stateAuth); err != nil {
			return nil, err
		}
		return json.Marshal(taskMeetingReceiptLink{TaskID: taskID, RunID: runID})
	})
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	var link taskMeetingReceiptLink
	if json.Unmarshal(result, &link) != nil || link.TaskID.Validate() != nil || link.RunID.Validate() != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskMeetingConflict
	}
	return m.readTaskMeetingInvocation(ctx, operationID, request, authorizeReceipt, authorizeState)
}

func (m *Manager) readTaskMeetingInvocation(ctx context.Context, operationID string, request domaintask.TaskMeetingInvocationRequest, authorizeReceipt domaintask.TaskMeetingInvocationOperationAuthorizer, authorizeState domaintask.TaskMeetingReadAuthorizationCheck) (domaintask.Task, domaintask.Run, error) {
	store, ok := m.store.(interface {
		LookupTaskMeetingOperationReceipt(context.Context, string, domaintask.TaskMeetingInvocationOperationAuthorizer) (json.RawMessage, string, bool, error)
	})
	if !ok {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	result, storedHash, found, err := store.LookupTaskMeetingOperationReceipt(ctx, operationID, authorizeReceipt)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	_, expectedHash, err := domaintask.TaskMeetingInvocationOperationIdentity(request)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	if !found {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskMeetingNotFound
	}
	if storedHash != expectedHash {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskMeetingConflict
	}
	var link taskMeetingReceiptLink
	if json.Unmarshal(result, &link) != nil || link.TaskID.Validate() != nil || link.RunID.Validate() != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskMeetingConflict
	}
	reader, ok := m.store.(domaintask.TaskMeetingInvocationReader)
	if !ok {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	task, run, _, err := reader.GetTaskMeetingInvocationState(ctx, link.TaskID, link.RunID, authorizeState)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	inv := task.TaskMeetingInvocation
	if inv == nil || inv.OccurrenceKey != request.OccurrenceKey || inv.Purpose != request.Purpose || inv.CoordinatorActorID != request.CoordinatorActorID || inv.ActorID != request.ActorID || inv.ConfiguredOwnerUserID != request.ConfiguredOwnerUserID || inv.ConfigRevision != request.ConfigRevision || inv.GenerationRequestID != request.GenerationRequestID || inv.GenerationScope != request.GenerationScope || inv.ConfigSHA256 != taskMeetingConfigDigest(request.FrozenConfig) || !bytes.Equal(inv.FrozenConfig, request.FrozenConfig) || inv.InputSHA256 != taskMeetingConfigDigest(request.FrozenInput) || !bytes.Equal(inv.FrozenInput, request.FrozenInput) || inv.RegistrationEvidenceID != request.RegistrationEvidenceID || inv.SourceContextSHA256 != request.SourceContextSHA256 || !sameTaskMeetingMessages(inv.FrozenSourceMessages, request.FrozenSourceMessages) || !sameTaskMeetingTiming(inv.Timing, request.Timing) || !sameTaskMeetingAudience(inv.Audience, request.Audience) || inv.ParentTaskID != request.ParentTaskID || inv.ParentRunID != request.ParentRunID {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskMeetingConflict
	}
	return task, run, nil
}

func sameTaskMeetingAudience(left, right []string) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}

func sameTaskMeetingMessages(left, right []domaintask.TaskMeetingConsumedMessage) bool {
	leftJSON, leftErr := json.Marshal(canonicalTaskMeetingMessages(left))
	rightJSON, rightErr := json.Marshal(canonicalTaskMeetingMessages(right))
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func canonicalTaskMeetingMessages(messages []domaintask.TaskMeetingConsumedMessage) []domaintask.TaskMeetingConsumedMessage {
	if messages == nil {
		return []domaintask.TaskMeetingConsumedMessage{}
	}
	return messages
}

func taskMeetingMessagesHavePrefix(current, prefix []domaintask.TaskMeetingConsumedMessage) bool {
	return len(current) >= len(prefix) && sameTaskMeetingMessages(current[:len(prefix)], prefix)
}

func cloneTaskMeetingShared(value domaintask.SharedRoleContext) domaintask.SharedRoleContext {
	value.TaskMeetingConsumedMessages = append([]domaintask.TaskMeetingConsumedMessage(nil), value.TaskMeetingConsumedMessages...)
	return value
}

func (m *Manager) readTaskMeetingState(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, authorize domaintask.TaskMeetingReadAuthorizationCheck) (domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, error) {
	reader, ok := m.store.(domaintask.TaskMeetingInvocationReader)
	if !ok {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	return reader.GetTaskMeetingInvocationState(ctx, taskID, runID, authorize)
}

func (m *Manager) readTaskMeetingContext(ctx context.Context, task domaintask.Task, run domaintask.Run, authorize domaintask.TaskMeetingReadAuthorizationCheck) (domaintask.SharedRoleContext, error) {
	_, _, shared, err := m.readTaskMeetingState(ctx, task.TaskID, run.RunID, authorize)
	return shared, err
}

func (m *Manager) verifyTaskMeetingRoot(ctx context.Context, proof domaintask.TaskMeetingRootProof, authorize domaintask.TaskMeetingReadAuthorizationCheck) error {
	if err := proof.Validate(); err != nil {
		return err
	}
	root, run, _, err := m.readTaskMeetingState(ctx, proof.TaskID, proof.RunID, authorize)
	if err != nil {
		return err
	}
	actual, err := taskMeetingRootProof(root, run)
	if err != nil {
		return err
	}
	if !sameTaskMeetingRootProof(actual, proof) {
		return domaintask.ErrTaskMeetingConflict
	}
	return nil
}

func taskMeetingRootProof(task domaintask.Task, run domaintask.Run) (domaintask.TaskMeetingRootProof, error) {
	inv := task.TaskMeetingInvocation
	if inv == nil || inv.Purpose != domaintask.TaskMeetingPurposeRoot || inv.Validate(task) != nil || run.TaskID != task.TaskID || run.RunID.Validate() != nil {
		return domaintask.TaskMeetingRootProof{}, domaintask.ErrTaskMeetingConflict
	}
	return domaintask.TaskMeetingRootProof{OccurrenceKey: inv.OccurrenceKey, CoordinatorActorID: inv.CoordinatorActorID, ConfiguredOwnerUserID: inv.ConfiguredOwnerUserID, TaskID: task.TaskID, RunID: run.RunID, ConfigRevision: inv.ConfigRevision, ConfigSHA256: inv.ConfigSHA256, RegistrationEvidenceID: inv.RegistrationEvidenceID, Audience: append([]string(nil), inv.Audience...), AudienceSHA256: inv.AudienceSHA256, AdmissionEvidenceID: inv.AdmissionEvidenceID, Timing: inv.Timing}, nil
}

func sameTaskMeetingRootProof(left, right domaintask.TaskMeetingRootProof) bool {
	leftBytes, _ := json.Marshal(left)
	rightBytes, _ := json.Marshal(right)
	return string(leftBytes) == string(rightBytes)
}

func sameTaskMeetingTiming(left, right modulecore.TaskTiming) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func sameTaskMeetingInvocation(left, right domaintask.TaskMeetingInvocation) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func taskMeetingConfigDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
func taskMeetingTitle(purpose domaintask.TaskMeetingPurpose, actor string) string {
	return "Scheduled TaskMeeting " + string(purpose) + " for " + actor
}
func taskMeetingTimingPtr(value modulecore.TaskTiming) *modulecore.TaskTiming {
	copy := cloneTaskMeetingTiming(value)
	return &copy
}

func cloneTaskMeetingRequest(value domaintask.TaskMeetingInvocationRequest) domaintask.TaskMeetingInvocationRequest {
	value.FrozenConfig = append([]byte(nil), value.FrozenConfig...)
	value.FrozenInput = append([]byte(nil), value.FrozenInput...)
	value.FrozenSourceMessages = append([]domaintask.TaskMeetingConsumedMessage(nil), value.FrozenSourceMessages...)
	value.Audience = append([]string(nil), value.Audience...)
	value.Timing = cloneTaskMeetingTiming(value.Timing)
	return value
}

func cloneTaskMeetingRootProof(value domaintask.TaskMeetingRootProof) domaintask.TaskMeetingRootProof {
	value.Audience = append([]string(nil), value.Audience...)
	value.Timing = cloneTaskMeetingTiming(value.Timing)
	return value
}

func cloneTaskMeetingTiming(value modulecore.TaskTiming) modulecore.TaskTiming {
	if value.CompletionCondition != nil {
		condition := *value.CompletionCondition
		value.CompletionCondition = &condition
	}
	value.AllowedWindows = append([]modulecore.TaskTimeWindow(nil), value.AllowedWindows...)
	if value.ScheduledStartAt != nil {
		copied := *value.ScheduledStartAt
		value.ScheduledStartAt = &copied
	}
	if value.NotBefore != nil {
		copied := *value.NotBefore
		value.NotBefore = &copied
	}
	if value.DeadlineAt != nil {
		copied := *value.DeadlineAt
		value.DeadlineAt = &copied
	}
	if value.EstimatedDurationSeconds != nil {
		copied := *value.EstimatedDurationSeconds
		value.EstimatedDurationSeconds = &copied
	}
	if value.Recurrence != nil {
		recurrence := *value.Recurrence
		recurrence.Times = append([]string(nil), recurrence.Times...)
		recurrence.Weekdays = append([]time.Weekday(nil), recurrence.Weekdays...)
		if recurrence.IntervalSeconds != nil {
			copied := *recurrence.IntervalSeconds
			recurrence.IntervalSeconds = &copied
		}
		value.Recurrence = &recurrence
	}
	return value
}

var _ domaintask.TaskMeetingGenerationFinisher = (*Manager)(nil)
