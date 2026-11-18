package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type taskMeetingGenerationStartedReceipt struct {
	Reference domaintask.TaskMeetingGenerationStartedReference `json:"reference"`
}

type taskMeetingGenerationUnknownReceipt struct {
	Reference domaintask.TaskMeetingGenerationUnknownReference `json:"reference"`
}

func (s *JSONLStore) StartTaskMeetingGenerationEffect(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, authorize domaintask.TaskMeetingReadAuthorizationCheck) (bool, error) {
	if s == nil || ctx == nil || authorize == nil || taskID.Validate() != nil || runID.Validate() != nil {
		return false, domaintask.ErrTaskMeetingOwnerRequired
	}
	task, run, _, err := s.GetTaskMeetingInvocationState(ctx, taskID, runID, authorize)
	if err != nil {
		return false, err
	}
	inv := task.TaskMeetingInvocation
	if task.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning || inv == nil ||
		(inv.Purpose != domaintask.TaskMeetingPurposeReport && inv.Purpose != domaintask.TaskMeetingPurposeAnswer) {
		return false, domaintask.ErrTaskMeetingConflict
	}
	writerGeneration, err := s.WriterGeneration()
	if err != nil {
		return false, err
	}
	if run.WriterGeneration == 0 || run.WriterGeneration != writerGeneration {
		return false, domaintask.ErrTaskMeetingExecutionUnknown
	}
	started := domaintask.TaskMeetingGenerationStartedReference{
		TaskID: taskID, RunID: runID, ReceiptID: domaintask.TaskMeetingGenerationEffectStartedReceiptID(taskID, runID),
		GenerationRequestID: inv.GenerationRequestID, Purpose: inv.Purpose, ActorID: inv.ActorID, OccurrenceKey: inv.OccurrenceKey,
		ConfigRevision: inv.ConfigRevision, ConfigSHA256: inv.ConfigSHA256, InputSHA256: inv.InputSHA256,
		SourceContextSHA256: inv.SourceContextSHA256, GenerationScope: inv.GenerationScope,
		EvidenceID: modulecore.NewEvidenceID(), StartedAt: s.taskMeetingNowUTC(),
	}
	if err := started.ValidateRun(task, run); err != nil {
		return false, err
	}
	requestHash := taskMeetingStartedRequestHash(started)
	authorizeOperation := func(checkCtx context.Context) error {
		current, currentRun, _, readErr := s.GetTaskMeetingInvocationState(checkCtx, taskID, runID, authorize)
		if readErr != nil {
			return readErr
		}
		if current.Status != domaintask.StatusRunning || currentRun.Status != domaintask.RunStatusRunning ||
			current.TaskMeetingInvocation == nil || !sameTaskMeetingInvocation(*inv, *current.TaskMeetingInvocation) ||
			currentRun.RunID != runID || currentRun.WriterGeneration != run.WriterGeneration {
			return domaintask.ErrTaskMeetingConflict
		}
		return nil
	}
	payload, err := s.ExecuteIdempotentTaskMeetingOperation(ctx, started.ReceiptID, requestHash, authorizeOperation, func(store domaintask.Store) (json.RawMessage, error) {
		tx, ok := store.(*transactionStore)
		if !ok {
			return nil, domaintask.ErrTaskMeetingOwnerRequired
		}
		current, currentRun, _, readErr := tx.getTaskMeetingStateRaw(ctx, taskID, runID)
		if readErr != nil {
			return nil, readErr
		}
		if current.Status != domaintask.StatusRunning || currentRun.Status != domaintask.RunStatusRunning ||
			current.TaskMeetingInvocation == nil || !sameTaskMeetingInvocation(*inv, *current.TaskMeetingInvocation) ||
			currentRun.WriterGeneration != run.WriterGeneration {
			return nil, domaintask.ErrTaskMeetingConflict
		}
		tx.markOwnerOnlyRead(current)
		return json.Marshal(taskMeetingGenerationStartedReceipt{Reference: started})
	})
	if err != nil {
		return false, err
	}
	var receipt taskMeetingGenerationStartedReceipt
	if err := decodeTaskMeetingReceipt(payload, &receipt); err != nil || receipt.Reference.ValidateRun(task, run) != nil {
		return false, domaintask.ErrTaskMeetingConflict
	}
	if !sameTaskMeetingStartedIdentity(receipt.Reference, started) {
		return false, domaintask.ErrTaskMeetingConflict
	}
	return receipt.Reference.EvidenceID == started.EvidenceID, nil
}

func (s *JSONLStore) RecordTaskMeetingGenerationUnknown(ctx context.Context, reference domaintask.TaskMeetingGenerationUnknownReference, authorize domaintask.TaskMeetingStateAuthorizationCheck) error {
	if s == nil || ctx == nil || authorize == nil || reference.Validate() != nil || !taskMeetingSettlementPermitMatches(ctx, s, reference.TaskID, reference.RunID) {
		return domaintask.ErrTaskMeetingOwnerRequired
	}
	if existing, err := s.GetTaskMeetingGenerationUnknown(ctx, reference.TaskID, reference.RunID, domaintask.TaskMeetingReadAuthorizationCheck(authorize)); err == nil {
		if sameTaskMeetingUnknownIdentity(existing, reference) {
			return nil
		}
		return domaintask.ErrTaskMeetingConflict
	} else if !errors.Is(err, domaintask.ErrTaskMeetingNotFound) {
		return err
	}
	task, run, _, err := s.GetTaskMeetingInvocationState(ctx, reference.TaskID, reference.RunID, domaintask.TaskMeetingReadAuthorizationCheck(authorize))
	if err != nil {
		return err
	}
	if task.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning || reference.ValidateRun(task, run) != nil || task.TaskMeetingResult != nil {
		return domaintask.ErrTaskMeetingConflict
	}
	if _, _, outputErr := s.GetTaskMeetingGenerationOutput(ctx, reference.TaskID, reference.RunID, domaintask.TaskMeetingReadAuthorizationCheck(authorize)); outputErr == nil {
		return domaintask.ErrTaskMeetingConflict
	} else if !errors.Is(outputErr, domaintask.ErrTaskMeetingNotFound) {
		return outputErr
	}
	fence, active := s.knownActiveTaskFenceStatus(reference.TaskID)
	writerGeneration, err := s.WriterGeneration()
	if err != nil {
		return err
	}
	if !active || fence.AcquireWriterGeneration != writerGeneration || run.WriterGeneration != writerGeneration {
		return domaintask.ErrTaskMeetingExecutionUnknown
	}
	requestHash := taskMeetingUnknownRequestHash(reference)
	authorizeOperation := func(checkCtx context.Context) error {
		current, currentRun, currentShared, readErr := s.GetTaskMeetingInvocationState(checkCtx, reference.TaskID, reference.RunID, domaintask.TaskMeetingReadAuthorizationCheck(authorize))
		if readErr != nil {
			return readErr
		}
		if current.TaskMeetingInvocation == nil || !sameTaskMeetingInvocation(*task.TaskMeetingInvocation, *current.TaskMeetingInvocation) ||
			currentRun.RunID != run.RunID || currentRun.TaskID != task.TaskID || reference.ValidateRun(current, currentRun) != nil {
			return domaintask.ErrTaskMeetingConflict
		}
		if current.Status == domaintask.StatusRunning && currentRun.Status == domaintask.RunStatusRunning ||
			current.Status == domaintask.StatusFailed && currentRun.Status == domaintask.RunStatusFailed && current.Summary == "TaskMeeting generation failed" {
			return authorize(checkCtx, current, currentRun, currentShared)
		}
		return domaintask.ErrTaskMeetingConflict
	}
	payload, err := s.ExecuteIdempotentTaskMeetingOperation(ctx, reference.ReceiptID, requestHash, authorizeOperation, func(store domaintask.Store) (json.RawMessage, error) {
		tx, ok := store.(*transactionStore)
		if !ok {
			return nil, domaintask.ErrTaskMeetingOwnerRequired
		}
		current, currentRun, currentShared, readErr := tx.getTaskMeetingStateRaw(ctx, reference.TaskID, reference.RunID)
		if readErr != nil {
			return nil, readErr
		}
		tx.markOwnerOnlyRead(current)
		if current.Status != domaintask.StatusRunning || currentRun.Status != domaintask.RunStatusRunning || reference.ValidateRun(current, currentRun) != nil || current.TaskMeetingResult != nil {
			return nil, domaintask.ErrTaskMeetingConflict
		}
		closedAt := reference.RecordedAt.UTC()
		const summary = "TaskMeeting generation failed"
		closedRun, closeErr := currentRun.Close(domaintask.RunStatusFailed, closedAt, summary)
		if closeErr != nil {
			return nil, closeErr
		}
		current.Status, current.Summary, current.UpdatedAt = domaintask.StatusFailed, summary, closedAt
		finished := closedAt
		current.FinishedAt = &finished
		writer, ok := store.(domaintask.TaskMeetingInvocationWriter)
		if !ok {
			return nil, domaintask.ErrTaskMeetingOwnerRequired
		}
		localCAS := func(_ context.Context, saved domaintask.Task, savedRun domaintask.Run, _ domaintask.SharedRoleContext) error {
			if saved.TaskID != reference.TaskID || savedRun.TaskID != reference.TaskID || savedRun.RunID != reference.RunID ||
				saved.Status != domaintask.StatusFailed || savedRun.Status != domaintask.RunStatusFailed || saved.Summary != summary ||
				saved.TaskMeetingInvocation == nil || !sameTaskMeetingInvocation(*task.TaskMeetingInvocation, *saved.TaskMeetingInvocation) || saved.TaskMeetingResult != nil {
				return domaintask.ErrTaskMeetingConflict
			}
			return nil
		}
		if err := writer.SaveTaskMeetingInvocationState(ctx, current, closedRun, &currentShared, localCAS); err != nil {
			return nil, err
		}
		return json.Marshal(taskMeetingGenerationUnknownReceipt{Reference: reference})
	})
	if err != nil {
		return err
	}
	var receipt taskMeetingGenerationUnknownReceipt
	if err := decodeTaskMeetingReceipt(payload, &receipt); err != nil || !sameTaskMeetingUnknownIdentity(receipt.Reference, reference) {
		return domaintask.ErrTaskMeetingConflict
	}
	return nil
}

func (s *JSONLStore) GetTaskMeetingGenerationUnknown(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, authorize domaintask.TaskMeetingReadAuthorizationCheck) (domaintask.TaskMeetingGenerationUnknownReference, error) {
	if s == nil || ctx == nil || authorize == nil || taskID.Validate() != nil || runID.Validate() != nil {
		return domaintask.TaskMeetingGenerationUnknownReference{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	task, run, _, err := s.GetTaskMeetingInvocationState(ctx, taskID, runID, authorize)
	if err != nil {
		return domaintask.TaskMeetingGenerationUnknownReference{}, err
	}
	if task.TaskMeetingInvocation == nil || (task.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeReport && task.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeAnswer) {
		return domaintask.TaskMeetingGenerationUnknownReference{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	operationID := domaintask.TaskMeetingGenerationUnknownReceiptID(taskID, runID)
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
	payload, gotHash, found, err := s.LookupTaskMeetingOperationReceipt(ctx, operationID, authorizeOperation)
	if err != nil {
		return domaintask.TaskMeetingGenerationUnknownReference{}, err
	}
	if !found {
		return domaintask.TaskMeetingGenerationUnknownReference{}, domaintask.ErrTaskMeetingNotFound
	}
	var receipt taskMeetingGenerationUnknownReceipt
	if err := decodeTaskMeetingReceipt(payload, &receipt); err != nil || receipt.Reference.ValidateRun(task, run) != nil ||
		task.Status != domaintask.StatusFailed || run.Status != domaintask.RunStatusFailed || task.TaskMeetingResult != nil {
		return domaintask.TaskMeetingGenerationUnknownReference{}, ErrTaskOperationReceiptCorrupt
	}
	if gotHash != taskMeetingUnknownRequestHash(receipt.Reference) {
		return domaintask.TaskMeetingGenerationUnknownReference{}, ErrTaskOperationReceiptCorrupt
	}
	return receipt.Reference, nil
}

func taskMeetingReceiptHash(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func taskMeetingStartedRequestHash(reference domaintask.TaskMeetingGenerationStartedReference) string {
	reference.EvidenceID = ""
	reference.StartedAt = time.Time{}
	encoded, _ := json.Marshal(reference)
	return taskMeetingReceiptHash(encoded)
}

func taskMeetingUnknownRequestHash(reference domaintask.TaskMeetingGenerationUnknownReference) string {
	reference.EvidenceID = ""
	reference.RecordedAt = time.Time{}
	encoded, _ := json.Marshal(reference)
	return taskMeetingReceiptHash(encoded)
}

func sameTaskMeetingUnknownIdentity(left, right domaintask.TaskMeetingGenerationUnknownReference) bool {
	left.EvidenceID, right.EvidenceID = "", ""
	left.RecordedAt, right.RecordedAt = time.Time{}, time.Time{}
	return left == right
}

func decodeTaskMeetingReceipt(payload json.RawMessage, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("TaskMeeting receipt has trailing JSON")
		}
		return err
	}
	return nil
}

func sameTaskMeetingStartedIdentity(left, right domaintask.TaskMeetingGenerationStartedReference) bool {
	left.EvidenceID, right.EvidenceID = "", ""
	left.StartedAt, right.StartedAt = time.Time{}, time.Time{}
	return left == right
}

func taskMeetingSettlementPermitMatches(ctx context.Context, store *JSONLStore, taskID modulecore.TaskID, runID modulecore.RunID) bool {
	permit, ok := ctx.Value(taskMeetingSettlementContextKey{}).(*taskMeetingSettlementPermit)
	return ok && permit != nil && permit.store == store && permit.taskID == taskID && permit.runID == runID && permit.token != nil
}

var _ domaintask.TaskMeetingGenerationUnknownStore = (*JSONLStore)(nil)
