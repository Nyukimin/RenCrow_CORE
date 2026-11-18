package task

import (
	"context"
	"errors"
	"fmt"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func (s *transactionStore) SaveNativePlanningInvocationState(
	ctx context.Context,
	task domaintask.Task,
	run domaintask.Run,
	shared *domaintask.SharedRoleContext,
	authorize domaintask.NativePlanningStateAuthorizationCheck,
) error {
	if ctx == nil || authorize == nil {
		return domaintask.ErrNativePlanningTaskRequiresOwner
	}
	if err := task.Validate(); err != nil {
		return err
	}
	if !domaintask.IsNativePlanningTask(task) || task.NativePlanningInvocation.Validate(task) != nil {
		return domaintask.ErrNativePlanningTaskRequiresOwner
	}
	if err := run.Validate(); err != nil {
		return err
	}
	if run.TaskID != task.TaskID || run.Assignee != task.Assignee {
		return errors.New("native planning Run does not match its issued Task")
	}
	if shared == nil || shared.TaskID != task.TaskID {
		return errors.New("native planning context identity is invalid")
	}
	if err := authorize(ctx, task, run, *shared); err != nil {
		return err
	}
	previous, previousErr := s.getTaskRaw(ctx, task.TaskID)
	if previousErr == nil {
		previousRun, runErr := s.getRunRaw(ctx, run.RunID)
		validFinish := task.Status == domaintask.StatusSucceeded && run.Status == domaintask.RunStatusSucceeded || task.Status == domaintask.StatusFailed && run.Status == domaintask.RunStatusFailed
		if runErr != nil || previous.NativePlanningInvocation == nil || task.NativePlanningInvocation == nil ||
			*previous.NativePlanningInvocation != *task.NativePlanningInvocation || previous.Status != domaintask.StatusRunning ||
			previousRun.Status != domaintask.RunStatusRunning || !validFinish ||
			task.Summary == "" || task.Summary != run.Summary {
			return domaintask.ErrNativePlanningTaskRequiresOwner
		}
	} else if errors.Is(previousErr, domaintask.ErrNotFound) {
		if task.Status != domaintask.StatusRunning || task.StartedAt == nil || run.Status != domaintask.RunStatusRunning || run.StartReason != domaintask.RunStartReasonFirst {
			return domaintask.ErrNativePlanningTaskRequiresOwner
		}
	} else {
		return previousErr
	}
	if err := s.saveTask(ctx, task); err != nil {
		return err
	}
	if err := s.saveRun(ctx, run); err != nil {
		return err
	}
	if errors.Is(previousErr, domaintask.ErrNotFound) {
		if err := s.saveNativePlanningContext(ctx, *shared); err != nil {
			return err
		}
	}
	return nil
}

func (s *transactionStore) saveNativePlanningContext(ctx context.Context, value domaintask.SharedRoleContext) error {
	if err := s.checkWrite(ctx); err != nil {
		return err
	}
	if err := value.TaskID.Validate(); err != nil {
		return err
	}
	if err := s.touchTask(ctx, value.TaskID); err != nil {
		return err
	}
	parent, err := s.getTaskRaw(ctx, value.TaskID)
	if err != nil {
		return err
	}
	if !domaintask.IsNativePlanningTask(parent) {
		return domaintask.ErrNativePlanningTaskRequiresOwner
	}
	return s.appendValue(ctx, contextFilename, value)
}

func (s *transactionStore) GetNativePlanningInvocationState(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	authorizeParent domaintask.NativePlanningParentAuthorizationCheck,
	authorize domaintask.NativePlanningStateAuthorizationCheck,
) (domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, error) {
	if ctx == nil || authorizeParent == nil || authorize == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, domaintask.ErrNativePlanningTaskRequiresOwner
	}
	if err := authorizeParent(ctx); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	task, run, shared, err := s.getNativePlanningInvocationStateRaw(ctx, taskID, runID)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	if !domaintask.IsNativePlanningTask(task) || task.NativePlanningInvocation.Validate(task) != nil || run.TaskID != taskID || run.RunID != runID || shared.TaskID != taskID {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, domaintask.ErrNativePlanningTaskRequiresOwner
	}
	if err := authorize(ctx, task, run, shared); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	if err := ctx.Err(); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	if err := authorizeParent(ctx); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	s.markOwnerOnlyRead(task)
	return task, run, shared, nil
}

func (s *JSONLStore) GetNativePlanningInvocationState(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, authorizeParent domaintask.NativePlanningParentAuthorizationCheck, authorize domaintask.NativePlanningStateAuthorizationCheck) (domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, error) {
	if ctx == nil || authorizeParent == nil || authorize == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, domaintask.ErrNativePlanningTaskRequiresOwner
	}
	if err := authorizeParent(ctx); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	var task domaintask.Task
	var run domaintask.Run
	var shared domaintask.SharedRoleContext
	err := s.ReadTransaction(ctx, func(store domaintask.Store) error {
		tx, ok := store.(*transactionStore)
		if !ok {
			return domaintask.ErrNativePlanningTaskRequiresOwner
		}
		var readErr error
		task, run, shared, readErr = tx.getNativePlanningInvocationStateRaw(ctx, taskID, runID)
		if readErr != nil {
			return readErr
		}
		if !domaintask.IsNativePlanningTask(task) || task.NativePlanningInvocation.Validate(task) != nil || run.TaskID != taskID || run.RunID != runID || shared.TaskID != taskID {
			return domaintask.ErrNativePlanningTaskRequiresOwner
		}
		if authErr := authorize(ctx, task, run, shared); authErr != nil {
			return authErr
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
	if err := authorizeParent(ctx); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	return task, run, shared, nil
}

func (s *transactionStore) getNativePlanningInvocationStateRaw(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID) (domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, error) {
	if s == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, errors.New("Task store is unavailable")
	}
	if err := taskID.Validate(); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
	}
	if err := runID.Validate(); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, err
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
	if run.TaskID != task.TaskID || run.TaskID != taskID {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, fmt.Errorf("native planning Run ownership mismatch")
	}
	return task, run, shared, nil
}

var _ domaintask.NativePlanningInvocationWriter = (*transactionStore)(nil)
var _ domaintask.NativePlanningInvocationStateReader = (*JSONLStore)(nil)
