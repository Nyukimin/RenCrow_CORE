package taskmanager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type TaskCenterExecutionAdmissionCheck = domaintask.TaskCenterExecutionAdmissionCheck

type TaskCenterExecutionInputAdmissionCheck = domaintask.TaskCenterExecutionInputAdmissionCheck

type TaskCenterKindEffect = domaintask.TaskCenterKindEffect

const TaskCenterMovieCatalogLookupKind = "movie.catalog.lookup"

const taskCenterExecutionResultMaxBytes = 48 << 10

type globalTaskCenterExecutionStore interface {
	ExecuteIdempotentGlobalTaskOperation(context.Context, string, string, func(Store) (json.RawMessage, error)) (json.RawMessage, error)
}

type taskCenterExecutionResultStore interface {
	ExecuteIdempotentTaskOperation(context.Context, modulecore.TaskID, string, string, func(Store) (json.RawMessage, error)) (json.RawMessage, error)
}

// EnsureTaskCenterExecution creates a typed native Task, its exact frozen
// context, its first Run, and a replay receipt in one Task owner transaction.
// It does not dispatch the kind or perform an external effect.
func (m *Manager) EnsureTaskCenterExecution(
	ctx context.Context,
	handoffKey modulecore.RequestID,
	preparedRevision uint64,
	draft domaintask.Task,
	shared domaintask.SharedRoleContext,
	kindInput json.RawMessage,
	admit TaskCenterExecutionAdmissionCheck,
	receiptRead domaintask.TaskCenterExecutionReceiptReadAuthorizationCheck,
) (domaintask.Task, domaintask.Run, error) {
	if m == nil || m.store == nil {
		return domaintask.Task{}, domaintask.Run{}, errors.New("task manager store is unavailable")
	}
	if ctx == nil {
		return domaintask.Task{}, domaintask.Run{}, errors.New("TaskCenter execution context is nil")
	}
	if admit == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTypedTaskRequiresOwner
	}
	if receiptRead == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskCenterExecutionReceiptAuthorizationRequired
	}
	operationID, requestHash, err := domaintask.BuildTaskCenterExecutionOperationIdentity(handoffKey, preparedRevision, draft, shared, kindInput)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	if !domaintask.IsTaskCenterTyped(draft) || draft.NativePlanningInvocation != nil || draft.TaskCenterExecution.KindID != TaskCenterMovieCatalogLookupKind {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskCenterExecutionReceiptUnsupported
	}
	if err := draft.ValidateStartTiming(m.now()); err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	if err := admit(ctx, draft, shared, kindInput); err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	store, ok := m.store.(globalTaskCenterExecutionStore)
	if !ok {
		return domaintask.Task{}, domaintask.Run{}, ErrGlobalTaskOperationUnsupported
	}
	result, err := store.ExecuteIdempotentGlobalTaskOperation(ctx, operationID, requestHash, func(tx Store) (json.RawMessage, error) {
		if tx == nil {
			return nil, errors.New("TaskCenter transaction store is unavailable")
		}
		started := draft
		now := m.now()
		if started.TaskID == "" {
			started.TaskID = modulecore.NewTaskID()
		}
		started.ApplyDefaults(now)
		started.Status = domaintask.StatusRunning
		started.StartedAt = &now
		started.FinishedAt = nil
		started.UpdatedAt = now
		if err := started.ValidateStartTiming(now); err != nil {
			return nil, err
		}
		generation, err := tx.WriterGeneration()
		if err != nil {
			return nil, fmt.Errorf("Task writer ownership unavailable: %w", err)
		}
		if generation == 0 {
			return nil, errors.New("Task writer generation is required")
		}
		run := domaintask.Run{
			WriterGeneration: generation, RunID: modulecore.NewRunID(), TaskID: started.TaskID,
			StartReason: domaintask.RunStartReasonFirst, Assignee: started.Assignee,
			Status: domaintask.RunStatusRunning, StartedAt: now,
		}
		writer, ok := tx.(domaintask.TaskCenterExecutionWriter)
		if !ok {
			return nil, domaintask.ErrTaskCenterExecutionReceiptUnsupported
		}
		stateAuthorization := func(checkCtx context.Context, task domaintask.Task, run domaintask.Run, storedShared domaintask.SharedRoleContext) error {
			if storedShared.TaskID != task.TaskID || run.TaskID != task.TaskID {
				return domaintask.ErrTypedTaskRequiresOwner
			}
			if err := admit(checkCtx, task, storedShared, kindInput); err != nil {
				return err
			}
			if run.TaskID != task.TaskID || run.Status != domaintask.RunStatusRunning || run.StartReason != domaintask.RunStartReasonFirst {
				return domaintask.ErrTypedTaskRequiresOwner
			}
			return nil
		}
		storedContext := shared
		storedContext.TaskID = started.TaskID
		if err := writer.SaveTaskCenterExecutionState(ctx, started, run, &storedContext, stateAuthorization); err != nil {
			return nil, err
		}
		if !canonicalActivityActor(started.Assignee) {
			return nil, domaintask.ErrCurrentActivityInvalid
		}
		activityManager := m.withStore(tx)
		if _, err := activityManager.SetCurrentActivityInTransaction(ctx, started.TaskID, domaintask.ActivityMetadata{
			Kind: domaintask.ActivityKindWork, Label: strings.TrimSpace(started.Title), Phase: "active",
			Participants: []domaintask.ActivityParticipant{{ActorID: started.Assignee}},
		}); err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			TaskID modulecore.TaskID `json:"task_id"`
			RunID  modulecore.RunID  `json:"run_id"`
		}{TaskID: started.TaskID, RunID: run.RunID})
	})
	if err != nil {
		recoveredTask, recoveredRun, recoveryErr := m.FindTaskCenterExecutionReceipt(ctx, handoffKey, preparedRevision, draft, shared, kindInput, receiptRead)
		if recoveryErr == nil {
			return recoveredTask, recoveredRun, nil
		}
		return domaintask.Task{}, domaintask.Run{}, err
	}
	var linked struct {
		TaskID modulecore.TaskID `json:"task_id"`
		RunID  modulecore.RunID  `json:"run_id"`
	}
	if err := json.Unmarshal(result, &linked); err != nil || linked.TaskID == "" || linked.RunID == "" {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskCenterExecutionReceiptCorrupt
	}
	if receiptRead == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskCenterExecutionReceiptAuthorizationRequired
	}
	return m.FindTaskCenterExecutionReceipt(ctx, handoffKey, preparedRevision, draft, shared, kindInput, receiptRead)
}

// FindTaskCenterExecutionReceipt performs read-only recovery of the exact
// frozen handoff operation. Its callback authorizes source scope and handoff
// reconciliation, not new execution admission.
func (m *Manager) FindTaskCenterExecutionReceipt(
	ctx context.Context,
	handoffKey modulecore.RequestID,
	preparedRevision uint64,
	expected domaintask.Task,
	shared domaintask.SharedRoleContext,
	kindInput json.RawMessage,
	authorize domaintask.TaskCenterExecutionReceiptReadAuthorizationCheck,
) (domaintask.Task, domaintask.Run, error) {
	if m == nil || m.store == nil {
		return domaintask.Task{}, domaintask.Run{}, errors.New("task manager store is unavailable")
	}
	if ctx == nil {
		return domaintask.Task{}, domaintask.Run{}, errors.New("TaskCenter receipt context is nil")
	}
	if authorize == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskCenterExecutionReceiptAuthorizationRequired
	}
	input, err := domaintask.NewTaskCenterExecutionReceiptReadInput(handoffKey, preparedRevision, expected, shared, kindInput)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	reader, ok := m.store.(domaintask.TaskCenterExecutionReceiptReader)
	if !ok {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskCenterExecutionReceiptUnsupported
	}
	task, run, err := reader.LookupTaskCenterExecutionReceipt(ctx, input, authorize)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, err
	}
	if !domaintask.TaskCenterDraftMatchesStored(expected, task) || run.TaskID != task.TaskID || run.RunID == "" {
		return domaintask.Task{}, domaintask.Run{}, domaintask.ErrTaskCenterExecutionReceiptConflict
	}
	return task, run, nil
}

// ExecuteTaskCenterKind runs the only currently supported native TaskCenter
// effect, movie.catalog.lookup, under the Task owner's durable execution
// fence. The admission callback must freshly read the exact Goal and return
// the frozen typed input. The handler is the registered kind owner.
func (m *Manager) ExecuteTaskCenterKind(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	actorID string,
	admit TaskCenterExecutionInputAdmissionCheck,
	effect TaskCenterKindEffect,
) (json.RawMessage, error) {
	if m == nil || m.store == nil {
		return nil, errors.New("task manager store is unavailable")
	}
	if ctx == nil {
		return nil, errors.New("TaskCenter effect context is nil")
	}
	if admit == nil || effect == nil {
		return nil, domaintask.ErrTypedTaskRequiresOwner
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fencer, ok := m.store.(domaintask.TaskCenterExecutionFencer)
	if !ok {
		return nil, domaintask.ErrTaskCenterExecutionReceiptUnsupported
	}
	var output json.RawMessage
	err := fencer.WithTaskCenterExecutionFence(ctx, taskID, runID, func(effectCtx context.Context, task domaintask.Task, run domaintask.Run, shared domaintask.SharedRoleContext) error {
		if task.TaskCenterExecution == nil || task.TaskCenterExecution.KindID != TaskCenterMovieCatalogLookupKind {
			return domaintask.ErrTaskCenterExecutionReceiptUnsupported
		}
		if err := task.Validate(); err != nil {
			return fmt.Errorf("typed Task is invalid: %w", err)
		}
		if err := run.Validate(); err != nil {
			return fmt.Errorf("typed Run is invalid: %w", err)
		}
		if task.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning || run.CompletedAt != nil ||
			task.TaskID != taskID || run.RunID != runID || run.TaskID != taskID ||
			strings.TrimSpace(actorID) == "" || task.Assignee != actorID || run.Assignee != actorID {
			return ErrRunConflict
		}
		generation, err := m.store.WriterGeneration()
		if err != nil {
			return fmt.Errorf("Task writer ownership unavailable: %w", err)
		}
		if generation == 0 || run.WriterGeneration != generation {
			return fmt.Errorf("%w: TaskCenter Run writer generation is stale", ErrRunConflict)
		}
		if err := task.ValidateStartTiming(m.now()); err != nil {
			return err
		}
		input, err := admit(effectCtx, task, run, shared)
		if err != nil {
			return err
		}
		digest, err := domaintask.TaskCenterKindInputSHA256(input)
		if err != nil || digest != task.TaskCenterExecution.KindInputSHA256 {
			return domaintask.ErrTaskCenterExecutionReceiptForbidden
		}
		if err := effectCtx.Err(); err != nil {
			return err
		}
		result, err := effect(effectCtx, append(json.RawMessage(nil), input...))
		if err != nil {
			return err
		}
		if len(result) == 0 || len(result) > taskCenterExecutionResultMaxBytes || !json.Valid(result) {
			return errors.New("TaskCenter kind owner returned invalid or oversized JSON result")
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, result); err != nil {
			return err
		}
		if err := effectCtx.Err(); err != nil {
			return err
		}
		output = append(json.RawMessage(nil), compact.Bytes()...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), output...), nil
}

// FinishTaskCenterExecution persists the terminal Task/Run pair and an exact
// reference to the canonical typed-result Artifact in one Task-owner
// transaction. Its callback must prove the exact canonical Artifact and
// current Goal source link before closure. This terminal bookkeeping may
// close a still-running old-generation Run after restart; it never relabels
// the Run generation. New effects remain fenced to the current generation.
func (m *Manager) FinishTaskCenterExecution(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	expected domaintask.Task,
	actorID string,
	status domaintask.Status,
	summary string,
	resultReference domaintask.TaskCenterResultReference,
	authorize domaintask.TaskCenterExecutionFinishAuthorizationCheck,
) (domaintask.Task, domaintask.Run, domaintask.TaskCenterResultReference, error) {
	if m == nil || m.store == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, errors.New("task manager store is unavailable")
	}
	if ctx == nil || authorize == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, domaintask.ErrTypedTaskRequiresOwner
	}
	if err := taskID.Validate(); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, err
	}
	if err := runID.Validate(); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, err
	}
	if expected.TaskCenterExecution == nil || expected.TaskCenterExecution.KindID != TaskCenterMovieCatalogLookupKind {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, domaintask.ErrTaskCenterExecutionReceiptUnsupported
	}
	expectedArtifactID, artifactIDErr := domaintask.TaskCenterResultArtifactID(taskID, runID)
	if resultReference.TaskID != taskID || resultReference.RunID != runID || resultReference.SourceGoalID != expected.GoalID ||
		resultReference.WorkstreamID != expected.WorkstreamID || expected.TaskCenterExecution == nil ||
		resultReference.KindID != expected.TaskCenterExecution.KindID || resultReference.KindInputSHA256 != expected.TaskCenterExecution.KindInputSHA256 ||
		artifactIDErr != nil || resultReference.ArtifactID != expectedArtifactID {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, domaintask.ErrTaskCenterExecutionReceiptConflict
	}
	if status != domaintask.StatusSucceeded && status != domaintask.StatusFailed && status != domaintask.StatusCancelled {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, errors.New("TaskCenter result requires succeeded, failed, or cancelled status")
	}
	if strings.TrimSpace(actorID) == "" {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, errors.New("actor_id is required")
	}
	operationID := fmt.Sprintf("taskcenter-finish:%s:%s", taskID, runID)
	requestValue := struct {
		TaskID  modulecore.TaskID                    `json:"task_id"`
		RunID   modulecore.RunID                     `json:"run_id"`
		Status  domaintask.Status                    `json:"status"`
		Summary string                               `json:"summary"`
		Result  domaintask.TaskCenterResultReference `json:"result_reference"`
	}{taskID, runID, status, strings.TrimSpace(summary), resultReference}
	requestJSON, err := json.Marshal(requestValue)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, err
	}
	requestDigest := sha256.Sum256(requestJSON)
	requestHash := hex.EncodeToString(requestDigest[:])
	receiptResult, err := json.Marshal(struct {
		TaskID          modulecore.TaskID                    `json:"task_id"`
		RunID           modulecore.RunID                     `json:"run_id"`
		ResultReference domaintask.TaskCenterResultReference `json:"result_reference"`
	}{taskID, runID, resultReference})
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, err
	}
	store, ok := m.store.(taskCenterExecutionResultStore)
	if !ok {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, domaintask.ErrTaskCenterExecutionReceiptUnsupported
	}
	finishCheck := func(checkCtx context.Context, task domaintask.Task, run domaintask.Run, shared domaintask.SharedRoleContext) error {
		if err := resultReference.Validate(task, run); err != nil {
			return err
		}
		return authorize(checkCtx, task, run, shared, resultReference)
	}
	recoveryAuthorize := func(checkCtx context.Context, task domaintask.Task, run domaintask.Run, shared domaintask.SharedRoleContext, reference domaintask.TaskCenterResultReference) error {
		if reference != resultReference {
			return domaintask.ErrTaskCenterExecutionReceiptConflict
		}
		return finishCheck(checkCtx, task, run, shared)
	}
	recovery, ok := m.store.(domaintask.TaskCenterExecutionFenceRecovery)
	if !ok {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, domaintask.ErrTaskCenterExecutionReceiptUnsupported
	}
	if err := recovery.RecoverTaskCenterExecutionFence(ctx, taskID, runID, resultReference, recoveryAuthorize); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, err
	}
	_, err = store.ExecuteIdempotentTaskOperation(ctx, taskID, operationID, requestHash, func(tx Store) (json.RawMessage, error) {
		reader, ok := tx.(domaintask.TaskCenterExecutionStateReader)
		if !ok {
			return nil, domaintask.ErrTaskCenterExecutionReceiptUnsupported
		}
		task, run, err := reader.GetTaskCenterExecutionState(ctx, taskID, runID, func(checkCtx context.Context, task domaintask.Task, run domaintask.Run, shared domaintask.SharedRoleContext) error {
			return finishCheck(checkCtx, task, run, shared)
		})
		if err != nil {
			return nil, err
		}
		if !domaintask.TaskCenterDraftMatchesStored(expected, task) || task.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning ||
			task.TaskID != taskID || run.RunID != runID || task.Assignee != actorID || run.Assignee != actorID {
			return nil, ErrRunConflict
		}
		generation, err := tx.WriterGeneration()
		if err != nil || generation == 0 || run.WriterGeneration == 0 || run.WriterGeneration > generation {
			return nil, fmt.Errorf("%w: TaskCenter Run writer generation is not owned by this TaskStore", ErrRunConflict)
		}
		runStatus, ok := runStatusForTaskStatus(status)
		if !ok {
			return nil, errors.New("TaskCenter terminal status has no canonical Run status")
		}
		activityManager := m.withStore(tx)
		task, err = activityManager.PrepareTerminalActivityTransition(ctx, task)
		if err != nil {
			return nil, err
		}
		now := m.now()
		closedRun, err := run.Close(runStatus, now, strings.TrimSpace(summary))
		if err != nil {
			return nil, err
		}
		task.Status, task.Summary, task.WaitingReason, task.UpdatedAt = status, strings.TrimSpace(summary), "", now
		finished := now
		task.FinishedAt = &finished
		if err := task.Validate(); err != nil {
			return nil, err
		}
		writer, ok := tx.(domaintask.TaskCenterExecutionWriter)
		if !ok {
			return nil, domaintask.ErrTaskCenterExecutionReceiptUnsupported
		}
		if err := writer.SaveTaskCenterExecutionState(ctx, task, closedRun, nil, finishCheck); err != nil {
			return nil, err
		}
		return append(json.RawMessage(nil), receiptResult...), nil
	})
	// On an ambiguous commit or an owner-only receipt replay, reconcile the
	// exact result from the Task owner instead of rerunning the kind effect.
	task, run, storedReference, recoverErr := m.FindTaskCenterExecutionResultReceipt(ctx, taskID, runID, expected, operationID, requestHash, resultReference, authorize)
	if recoverErr == nil {
		return task, run, storedReference, nil
	}
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, err
	}
	return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, recoverErr
}

func (m *Manager) FindTaskCenterExecutionResultReceipt(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	expected domaintask.Task,
	operationID string,
	requestHash string,
	expectedReference domaintask.TaskCenterResultReference,
	authorize domaintask.TaskCenterExecutionFinishAuthorizationCheck,
) (domaintask.Task, domaintask.Run, domaintask.TaskCenterResultReference, error) {
	if m == nil || m.store == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, errors.New("task manager store is unavailable")
	}
	if ctx == nil || authorize == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, domaintask.ErrTaskCenterExecutionReceiptAuthorizationRequired
	}
	reader, ok := m.store.(domaintask.TaskCenterExecutionResultReceiptReader)
	if !ok {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, domaintask.ErrTaskCenterExecutionReceiptUnsupported
	}
	task, run, reference, err := reader.LookupTaskCenterExecutionResultReceipt(ctx, domaintask.TaskCenterExecutionResultReceiptReadInput{
		OperationID: operationID, RequestHash: requestHash, TaskID: taskID, RunID: runID,
		ResultReference: expectedReference, ExpectedTask: expected,
	}, authorize)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, err
	}
	if !domaintask.TaskCenterDraftMatchesStored(expected, task) || run.TaskID != taskID || run.RunID != runID ||
		reference != expectedReference || reference.Validate(task, run) != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.TaskCenterResultReference{}, domaintask.ErrTaskCenterExecutionReceiptConflict
	}
	return task, run, reference, nil
}
