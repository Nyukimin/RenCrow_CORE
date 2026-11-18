package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func (s *transactionStore) SaveTaskMeetingInvocationState(ctx context.Context, value domaintask.Task, run domaintask.Run, shared *domaintask.SharedRoleContext, authorize domaintask.TaskMeetingStateAuthorizationCheck) error {
	if ctx == nil || authorize == nil || shared == nil || value.TaskMeetingInvocation == nil || value.TaskMeetingInvocation.Validate(value) != nil {
		return domaintask.ErrTaskMeetingOwnerRequired
	}
	if err := value.Validate(); err != nil {
		return err
	}
	if err := run.Validate(); err != nil {
		return err
	}
	if run.TaskID != value.TaskID || run.Assignee != value.Assignee || shared.TaskID != value.TaskID {
		return domaintask.ErrTaskMeetingConflict
	}
	if err := domaintask.ValidateTaskMeetingConsumedMessages(value, run, *shared); err != nil {
		return err
	}
	if err := authorize(ctx, value, run, *shared); err != nil {
		return err
	}
	previous, previousErr := s.getTaskRaw(ctx, value.TaskID)
	if previousErr == nil {
		previousRun, runErr := s.getRunRaw(ctx, run.RunID)
		validFinish := value.Status == domaintask.StatusSucceeded && run.Status == domaintask.RunStatusSucceeded || value.Status == domaintask.StatusFailed && run.Status == domaintask.RunStatusFailed
		switch value.TaskMeetingInvocation.Purpose {
		case domaintask.TaskMeetingPurposeRoot:
			validFinish = value.Status == domaintask.StatusSucceeded && run.Status == domaintask.RunStatusSucceeded && value.Summary == "TaskMeeting occurrence completed" || value.Status == domaintask.StatusFailed && run.Status == domaintask.RunStatusFailed && value.Summary == "TaskMeeting occurrence did not complete"
		case domaintask.TaskMeetingPurposeRecipientContext:
			validFinish = value.Status == domaintask.StatusSucceeded && run.Status == domaintask.RunStatusSucceeded && value.Summary == "TaskMeeting recipient context completed" || value.Status == domaintask.StatusFailed && run.Status == domaintask.RunStatusFailed && value.Summary == "TaskMeeting recipient context did not complete"
		case domaintask.TaskMeetingPurposeReport, domaintask.TaskMeetingPurposeAnswer:
			validFinish = value.Status == domaintask.StatusSucceeded && run.Status == domaintask.RunStatusSucceeded && value.Summary == "TaskMeeting generation completed" || value.Status == domaintask.StatusFailed && run.Status == domaintask.RunStatusFailed && value.Summary == "TaskMeeting generation failed"
		}
		if runErr != nil || previous.TaskMeetingInvocation == nil || value.TaskMeetingInvocation == nil || !sameTaskMeetingInvocation(*previous.TaskMeetingInvocation, *value.TaskMeetingInvocation) ||
			previous.Status != domaintask.StatusRunning || previousRun.Status != domaintask.RunStatusRunning || !validFinish || value.Summary == "" || value.Summary != run.Summary ||
			previousRun.RunID != run.RunID || previous.TaskID != value.TaskID || value.TaskID != run.TaskID {
			return domaintask.ErrTaskMeetingConflict
		}
	} else if !errors.Is(previousErr, domaintask.ErrNotFound) {
		return previousErr
	} else {
		if value.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning {
			return domaintask.ErrTaskMeetingConflict
		}
	}
	if err := s.saveTask(ctx, value); err != nil {
		return err
	}
	if err := s.saveRun(ctx, run); err != nil {
		return err
	}
	if errors.Is(previousErr, domaintask.ErrNotFound) {
		if err := s.saveTaskMeetingContext(ctx, *shared); err != nil {
			return err
		}
	}
	return nil
}

func (s *transactionStore) saveTaskMeetingContext(ctx context.Context, value domaintask.SharedRoleContext) error {
	if err := s.checkWrite(ctx); err != nil {
		return err
	}
	if err := value.TaskID.Validate(); err != nil {
		return err
	}
	if err := s.touchTask(ctx, value.TaskID); err != nil {
		return err
	}
	task, err := s.getTaskRaw(ctx, value.TaskID)
	if err != nil {
		return err
	}
	if !domaintask.IsTaskMeetingTask(task) || task.TaskMeetingInvocation.Validate(task) != nil {
		return domaintask.ErrTaskMeetingOwnerRequired
	}
	return s.appendValue(ctx, contextFilename, value)
}

func (s *JSONLStore) GetTaskMeetingInvocationState(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, authorize domaintask.TaskMeetingReadAuthorizationCheck) (domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, error) {
	if ctx == nil || authorize == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, domaintask.ErrTaskMeetingOwnerRequired
	}
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
		if err := authorize(ctx, task, run, shared); err != nil {
			return err
		}
		tx.markOwnerOnlyRead(task)
		return nil
	})
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	if err := ctx.Err(); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	if err := authorize(ctx, task, run, shared); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	return cloneTransactionTask(task), run, cloneTransactionContext(shared), nil
}

func (s *transactionStore) getTaskMeetingStateRaw(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID) (domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, error) {
	if taskID.Validate() != nil || runID.Validate() != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, domaintask.ErrTaskMeetingNotFound
	}
	task, err := s.getTaskRaw(ctx, taskID)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	run, err := s.getRunRaw(ctx, runID)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	shared, err := s.getContextRaw(ctx, taskID)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	if run.TaskID != taskID || task.TaskID != taskID || shared.TaskID != taskID {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, domaintask.ErrTaskMeetingConflict
	}
	return cloneTransactionTask(task), run, cloneTransactionContext(shared), nil
}

func (s *transactionStore) AppendTaskMeetingConsumedMessage(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, ref domaintask.TaskMeetingMessageReference, body string, authorize domaintask.TaskMeetingMessageReadAuthorizationCheck) (domaintask.TaskMeetingConsumedMessage, error) {
	if ctx == nil || authorize == nil || len(body) > domaintask.TaskMeetingMaxFrozenInputBytes || ref.Validate() != nil {
		return domaintask.TaskMeetingConsumedMessage{}, domaintask.ErrTaskMeetingOwnerRequired
	}
	bodyHash := sha256.Sum256([]byte(body))
	if hex.EncodeToString(bodyHash[:]) != ref.BodySHA256 {
		return domaintask.TaskMeetingConsumedMessage{}, domaintask.ErrTaskMeetingConflict
	}
	task, run, shared, err := s.getTaskMeetingStateRaw(ctx, taskID, runID)
	if err != nil {
		return domaintask.TaskMeetingConsumedMessage{}, err
	}
	inv := task.TaskMeetingInvocation
	if inv == nil || inv.Purpose != domaintask.TaskMeetingPurposeRecipientContext || inv.ActorID == "" || inv.OccurrenceKey != ref.OccurrenceKey || inv.ConfigRevision != ref.ConfigRevision || inv.ConfigSHA256 != ref.ConfigSHA256 || inv.AudienceSHA256 != ref.AudienceSHA256 {
		return domaintask.TaskMeetingConsumedMessage{}, domaintask.ErrTaskMeetingConflict
	}
	if err := authorize(ctx, task, run, shared, ref); err != nil {
		return domaintask.TaskMeetingConsumedMessage{}, err
	}
	for _, existing := range shared.TaskMeetingConsumedMessages {
		if existing.ActorID == inv.ActorID && existing.Reference.MessageID == ref.MessageID {
			if !sameTaskMeetingReference(existing.Reference, ref) {
				return domaintask.TaskMeetingConsumedMessage{}, domaintask.ErrTaskMeetingConflict
			}
			return existing, nil
		}
	}
	if len(shared.TaskMeetingConsumedMessages) >= domaintask.TaskMeetingMaxConsumedMessages {
		return domaintask.TaskMeetingConsumedMessage{}, domaintask.ErrTaskMeetingConflict
	}
	contextMaterial, err := json.Marshal(struct {
		Previous  []domaintask.TaskMeetingConsumedMessage `json:"previous"`
		Reference domaintask.TaskMeetingMessageReference  `json:"reference"`
	}{shared.TaskMeetingConsumedMessages, ref})
	if err != nil {
		return domaintask.TaskMeetingConsumedMessage{}, err
	}
	contextHash := sha256.Sum256(contextMaterial)
	proof := domaintask.TaskMeetingConsumedMessage{Reference: ref, ActorID: inv.ActorID, ConsumerTaskID: taskID, ConsumerRunID: runID, ContextSHA256: hex.EncodeToString(contextHash[:]), AcceptedAt: s.parent.taskMeetingNowUTC(), EvidenceID: modulecore.NewEvidenceID()}
	if err := proof.Validate(task, run); err != nil {
		return domaintask.TaskMeetingConsumedMessage{}, err
	}
	shared.TaskMeetingConsumedMessages = append(shared.TaskMeetingConsumedMessages, proof)
	shared.UpdatedAt = proof.AcceptedAt
	if err := domaintask.ValidateTaskMeetingConsumedMessages(task, run, shared); err != nil {
		return domaintask.TaskMeetingConsumedMessage{}, err
	}
	if err := authorize(ctx, task, run, shared, ref); err != nil {
		return domaintask.TaskMeetingConsumedMessage{}, err
	}
	if err := s.saveTaskMeetingContext(ctx, shared); err != nil {
		return domaintask.TaskMeetingConsumedMessage{}, err
	}
	return proof, nil
}

func (s *JSONLStore) AppendTaskMeetingConsumedMessage(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, ref domaintask.TaskMeetingMessageReference, body string, authorize domaintask.TaskMeetingMessageReadAuthorizationCheck) (domaintask.TaskMeetingConsumedMessage, error) {
	var result domaintask.TaskMeetingConsumedMessage
	if s == nil {
		return result, domaintask.ErrTaskMeetingOwnerRequired
	}
	err := s.Transaction(ctx, func(store domaintask.Store) error {
		tx, ok := store.(*transactionStore)
		if !ok {
			return domaintask.ErrTaskMeetingOwnerRequired
		}
		var err error
		result, err = tx.AppendTaskMeetingConsumedMessage(ctx, taskID, runID, ref, body, authorize)
		return err
	})
	return result, err
}

func (s *JSONLStore) FindTaskMeetingConsumedMessage(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, ref domaintask.TaskMeetingMessageReference, authorize domaintask.TaskMeetingMessageReadAuthorizationCheck) (domaintask.TaskMeetingConsumedMessage, error) {
	task, run, shared, err := s.GetTaskMeetingInvocationState(ctx, taskID, runID, func(checkCtx context.Context, task domaintask.Task, run domaintask.Run, shared domaintask.SharedRoleContext) error {
		return authorize(checkCtx, task, run, shared, ref)
	})
	if err != nil {
		return domaintask.TaskMeetingConsumedMessage{}, err
	}
	for _, proof := range shared.TaskMeetingConsumedMessages {
		if proof.ActorID == task.TaskMeetingInvocation.ActorID && sameTaskMeetingReference(proof.Reference, ref) {
			if err := proof.Validate(task, run); err != nil {
				return domaintask.TaskMeetingConsumedMessage{}, err
			}
			return proof, nil
		}
	}
	return domaintask.TaskMeetingConsumedMessage{}, domaintask.ErrTaskMeetingNotFound
}

func sameTaskMeetingReference(left, right domaintask.TaskMeetingMessageReference) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
}

func sameTaskMeetingInvocation(left, right domaintask.TaskMeetingInvocation) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
}

var _ domaintask.TaskMeetingInvocationWriter = (*transactionStore)(nil)
var _ domaintask.TaskMeetingInvocationReader = (*JSONLStore)(nil)
var _ domaintask.TaskMeetingMessageConsumer = (*JSONLStore)(nil)
var _ domaintask.TaskMeetingMessageConsumptionReader = (*JSONLStore)(nil)

func (s *transactionStore) CountTaskMeetingActiveGenerations(ctx context.Context) (int, error) {
	tasks, err := s.loadTasks(ctx)
	if err != nil {
		return 0, err
	}
	runs, err := s.loadRuns(ctx)
	if err != nil {
		return 0, err
	}
	latestRuns, err := latestTransactionRuns(runs)
	if err != nil {
		return 0, err
	}
	running := make(map[modulecore.TaskID]bool, len(latestRuns))
	for _, run := range latestRuns {
		if run.Status == domaintask.RunStatusRunning {
			running[run.TaskID] = true
		}
	}
	count := 0
	for _, task := range tasks {
		if task.Status == domaintask.StatusRunning && task.TaskMeetingInvocation != nil && (task.TaskMeetingInvocation.Purpose == domaintask.TaskMeetingPurposeReport || task.TaskMeetingInvocation.Purpose == domaintask.TaskMeetingPurposeAnswer) && running[task.TaskID] {
			count++
		}
	}
	return count, nil
}

var _ domaintask.TaskMeetingActiveGenerationCounter = (*transactionStore)(nil)
var _ = fmt.Sprintf
