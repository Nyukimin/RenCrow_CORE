package task

import (
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

type taskMeetingGenerationOutputReceipt struct {
	Reference domaintask.TaskMeetingGenerationResultReference `json:"reference"`
	Output    json.RawMessage                                 `json:"output"`
}

// SaveTaskMeetingGenerationOutput persists canonical typed output in an
// owner-only Task operation receipt. A first write requires the current
// generation fence; an exact receipt replay is read-only and returns its
// original evidence/time.
func (s *JSONLStore) SaveTaskMeetingGenerationOutput(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, output json.RawMessage, authorize domaintask.TaskMeetingReadAuthorizationCheck) (domaintask.TaskMeetingGenerationResultReference, error) {
	if s == nil || ctx == nil || authorize == nil || taskID.Validate() != nil || runID.Validate() != nil || len(output) == 0 || len(output) > domaintask.TaskMeetingMaxFrozenInputBytes || !json.Valid(output) {
		return domaintask.TaskMeetingGenerationResultReference{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	task, run, _, err := s.GetTaskMeetingInvocationState(ctx, taskID, runID, authorize)
	if err != nil {
		return domaintask.TaskMeetingGenerationResultReference{}, err
	}
	if task.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning || task.TaskMeetingInvocation == nil ||
		(task.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeReport && task.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeAnswer) {
		return domaintask.TaskMeetingGenerationResultReference{}, domaintask.ErrTaskMeetingConflict
	}
	operationID, requestHash, outputSHA := taskMeetingOutputIdentity(task, run, output)
	authorizeOperation := func(checkCtx context.Context) error {
		current, currentRun, _, readErr := s.GetTaskMeetingInvocationState(checkCtx, taskID, runID, authorize)
		if readErr != nil {
			return readErr
		}
		if current.TaskMeetingInvocation == nil || !sameTaskMeetingInvocation(*task.TaskMeetingInvocation, *current.TaskMeetingInvocation) || current.TaskID != taskID || currentRun.RunID != runID ||
			!(current.Status == domaintask.StatusRunning && currentRun.Status == domaintask.RunStatusRunning || current.Status == domaintask.StatusSucceeded && currentRun.Status == domaintask.RunStatusSucceeded) {
			return domaintask.ErrTaskMeetingConflict
		}
		return nil
	}
	result, err := s.ExecuteIdempotentTaskMeetingOperation(ctx, operationID, requestHash, authorizeOperation, func(store domaintask.Store) (json.RawMessage, error) {
		tx, ok := store.(*transactionStore)
		if !ok {
			return nil, domaintask.ErrTaskMeetingOwnerRequired
		}
		current, currentRun, _, readErr := tx.getTaskMeetingStateRaw(ctx, taskID, runID)
		if readErr != nil {
			return nil, readErr
		}
		if current.Status != domaintask.StatusRunning || currentRun.Status != domaintask.RunStatusRunning || current.TaskMeetingInvocation == nil ||
			!sameTaskMeetingInvocation(*task.TaskMeetingInvocation, *current.TaskMeetingInvocation) || currentRun.WriterGeneration == 0 {
			return nil, domaintask.ErrTaskMeetingConflict
		}
		fence, active := s.knownActiveTaskFenceStatus(taskID)
		writerGeneration, generationErr := tx.WriterGeneration()
		if generationErr != nil {
			return nil, generationErr
		}
		if !active || fence.State != TaskExecutionFenceStateUncertain || fence.AcquireWriterGeneration != currentRun.WriterGeneration || fence.AcquireWriterGeneration != writerGeneration {
			return nil, ErrTaskExecutionFenceActive
		}
		inv := current.TaskMeetingInvocation
		reference := domaintask.TaskMeetingGenerationResultReference{
			TaskID: taskID, RunID: runID, ReceiptID: operationID, GenerationRequestID: inv.GenerationRequestID, Purpose: inv.Purpose, ActorID: inv.ActorID,
			OccurrenceKey: inv.OccurrenceKey, ConfigRevision: inv.ConfigRevision, ConfigSHA256: inv.ConfigSHA256, InputSHA256: inv.InputSHA256,
			SourceContextSHA256: inv.SourceContextSHA256, GenerationScope: inv.GenerationScope, ResultSHA256: outputSHA,
			ReceiptEvidenceID: modulecore.NewEvidenceID(), RecordedAt: s.taskMeetingNowUTC(),
		}
		if err := reference.ValidateRun(current, currentRun); err != nil {
			return nil, err
		}
		tx.markOwnerOnlyRead(current)
		return json.Marshal(taskMeetingGenerationOutputReceipt{Reference: reference, Output: append(json.RawMessage(nil), output...)})
	})
	if err != nil {
		return domaintask.TaskMeetingGenerationResultReference{}, err
	}
	receipt, err := decodeTaskMeetingOutputReceipt(result)
	if err != nil || receipt.Reference.ValidateRun(task, run) != nil || receipt.Reference.ResultSHA256 != outputSHA || string(receipt.Output) != string(output) {
		return domaintask.TaskMeetingGenerationResultReference{}, domaintask.ErrTaskMeetingConflict
	}
	return receipt.Reference, nil
}

// GetTaskMeetingGenerationOutput returns only the exact owner-only output
// receipt bound to the requested typed generation Task/Run.
func (s *JSONLStore) GetTaskMeetingGenerationOutput(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, authorize domaintask.TaskMeetingReadAuthorizationCheck) (json.RawMessage, domaintask.TaskMeetingGenerationResultReference, error) {
	if s == nil || ctx == nil || authorize == nil || taskID.Validate() != nil || runID.Validate() != nil {
		return nil, domaintask.TaskMeetingGenerationResultReference{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	task, run, _, err := s.GetTaskMeetingInvocationState(ctx, taskID, runID, authorize)
	if err != nil {
		return nil, domaintask.TaskMeetingGenerationResultReference{}, err
	}
	if task.TaskMeetingInvocation == nil || (task.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeReport && task.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeAnswer) {
		return nil, domaintask.TaskMeetingGenerationResultReference{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	if _, unknownErr := s.GetTaskMeetingGenerationUnknown(ctx, taskID, runID, authorize); unknownErr == nil {
		return nil, domaintask.TaskMeetingGenerationResultReference{}, domaintask.ErrTaskMeetingExecutionUnknown
	} else if !errors.Is(unknownErr, domaintask.ErrTaskMeetingNotFound) {
		return nil, domaintask.TaskMeetingGenerationResultReference{}, unknownErr
	}
	operationID := taskMeetingOutputOperationID(taskID, runID)
	authorizeOperation := func(checkCtx context.Context) error {
		current, currentRun, _, readErr := s.GetTaskMeetingInvocationState(checkCtx, taskID, runID, authorize)
		if readErr != nil {
			return readErr
		}
		if current.TaskMeetingInvocation == nil || !sameTaskMeetingInvocation(*task.TaskMeetingInvocation, *current.TaskMeetingInvocation) || currentRun.RunID != runID {
			return domaintask.ErrTaskMeetingConflict
		}
		return nil
	}
	payload, _, found, err := s.LookupTaskMeetingOperationReceipt(ctx, operationID, authorizeOperation)
	if err != nil {
		return nil, domaintask.TaskMeetingGenerationResultReference{}, err
	}
	if !found {
		return nil, domaintask.TaskMeetingGenerationResultReference{}, domaintask.ErrTaskMeetingNotFound
	}
	receipt, err := decodeTaskMeetingOutputReceipt(payload)
	if err != nil || receipt.Reference.ValidateRun(task, run) != nil || taskMeetingOutputSHA(receipt.Output) != receipt.Reference.ResultSHA256 {
		return nil, domaintask.TaskMeetingGenerationResultReference{}, ErrTaskOperationReceiptCorrupt
	}
	if task.TaskMeetingResult != nil && *task.TaskMeetingResult != receipt.Reference {
		return nil, domaintask.TaskMeetingGenerationResultReference{}, domaintask.ErrTaskMeetingConflict
	}
	return append(json.RawMessage(nil), receipt.Output...), receipt.Reference, nil
}

func taskMeetingOutputOperationID(taskID modulecore.TaskID, runID modulecore.RunID) string {
	return domaintask.TaskMeetingGenerationOutputReceiptID(taskID, runID)
}

func taskMeetingOutputIdentity(task domaintask.Task, run domaintask.Run, output json.RawMessage) (string, string, string) {
	outputSHA := taskMeetingOutputSHA(output)
	inv := task.TaskMeetingInvocation
	parts := []string{string(task.TaskID), string(run.RunID), string(inv.GenerationRequestID), string(inv.Purpose), inv.ActorID, inv.OccurrenceKey, fmt.Sprint(inv.ConfigRevision), inv.ConfigSHA256, inv.InputSHA256, inv.SourceContextSHA256, outputSHA}
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return taskMeetingOutputOperationID(task.TaskID, run.RunID), hex.EncodeToString(hash[:]), outputSHA
}

func taskMeetingOutputSHA(output json.RawMessage) string {
	sum := sha256.Sum256(output)
	return hex.EncodeToString(sum[:])
}

func decodeTaskMeetingOutputReceipt(payload json.RawMessage) (taskMeetingGenerationOutputReceipt, error) {
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var receipt taskMeetingGenerationOutputReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return taskMeetingGenerationOutputReceipt{}, err
	}
	if receipt.Reference.Validate() != nil || len(receipt.Output) == 0 || len(receipt.Output) > domaintask.TaskMeetingMaxFrozenInputBytes || !json.Valid(receipt.Output) {
		return taskMeetingGenerationOutputReceipt{}, errors.New("TaskMeeting generation output receipt is invalid")
	}
	return receipt, nil
}

var _ domaintask.TaskMeetingGenerationOutputStore = (*JSONLStore)(nil)
