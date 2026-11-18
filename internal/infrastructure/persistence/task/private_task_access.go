package task

import (
	"context"
	"errors"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func (s *JSONLStore) GetTaskAuthorized(ctx context.Context, taskID modulecore.TaskID, authorize domaintask.TaskReadAuthorizationCheck) (domaintask.Task, error) {
	var result domaintask.Task
	err := s.ReadTransaction(ctx, func(tx domaintask.Store) error {
		reader, ok := tx.(domaintask.PrivateTaskReader)
		if !ok {
			return domaintask.ErrPrivateTaskAccessUnsupported
		}
		var err error
		result, err = reader.GetTaskAuthorized(ctx, taskID, authorize)
		return err
	})
	return result, err
}

func (s *JSONLStore) ListTasksAuthorized(ctx context.Context, filter domaintask.Filter, authorize domaintask.TaskReadAuthorizationCheck) ([]domaintask.Task, error) {
	var result []domaintask.Task
	err := s.ReadTransaction(ctx, func(tx domaintask.Store) error {
		reader, ok := tx.(domaintask.PrivateTaskReader)
		if !ok {
			return domaintask.ErrPrivateTaskAccessUnsupported
		}
		var err error
		result, err = reader.ListTasksAuthorized(ctx, filter, authorize)
		return err
	})
	return result, err
}

func (s *JSONLStore) GetRunAuthorized(ctx context.Context, runID modulecore.RunID, authorize domaintask.TaskReadAuthorizationCheck) (domaintask.Run, error) {
	var result domaintask.Run
	err := s.ReadTransaction(ctx, func(tx domaintask.Store) error {
		reader, ok := tx.(domaintask.PrivateTaskReader)
		if !ok {
			return domaintask.ErrPrivateTaskAccessUnsupported
		}
		var err error
		result, err = reader.GetRunAuthorized(ctx, runID, authorize)
		return err
	})
	return result, err
}

func (s *JSONLStore) ListRunsAuthorized(ctx context.Context, filter domaintask.RunFilter, authorize domaintask.TaskReadAuthorizationCheck) ([]domaintask.Run, error) {
	var result []domaintask.Run
	err := s.ReadTransaction(ctx, func(tx domaintask.Store) error {
		reader, ok := tx.(domaintask.PrivateTaskReader)
		if !ok {
			return domaintask.ErrPrivateTaskAccessUnsupported
		}
		var err error
		result, err = reader.ListRunsAuthorized(ctx, filter, authorize)
		return err
	})
	return result, err
}

func (s *JSONLStore) GetContextAuthorized(ctx context.Context, taskID modulecore.TaskID, authorize domaintask.TaskReadAuthorizationCheck) (domaintask.SharedRoleContext, error) {
	var result domaintask.SharedRoleContext
	err := s.ReadTransaction(ctx, func(tx domaintask.Store) error {
		reader, ok := tx.(domaintask.PrivateTaskReader)
		if !ok {
			return domaintask.ErrPrivateTaskAccessUnsupported
		}
		var err error
		result, err = reader.GetContextAuthorized(ctx, taskID, authorize)
		return err
	})
	return result, err
}

// SaveTaskAuthorized is a persistence owner port only. It authorizes durable
// record persistence; it does not authorize Task execution or external effects.
func (s *JSONLStore) SaveTaskAuthorized(ctx context.Context, value domaintask.Task, authorize domaintask.TaskWriteAuthorizationCheck) error {
	if s == nil {
		return errors.New("task store is nil")
	}
	return s.Transaction(ctx, func(tx domaintask.Store) error {
		writer, ok := tx.(domaintask.PrivateTaskWriter)
		if !ok {
			return domaintask.ErrPrivateTaskAccessUnsupported
		}
		return writer.SaveTaskAuthorized(ctx, value, authorize)
	})
}

func (s *JSONLStore) SaveRunAuthorized(ctx context.Context, value domaintask.Run, authorize domaintask.TaskWriteAuthorizationCheck) error {
	if s == nil {
		return errors.New("task store is nil")
	}
	return s.Transaction(ctx, func(tx domaintask.Store) error {
		writer, ok := tx.(domaintask.PrivateTaskWriter)
		if !ok {
			return domaintask.ErrPrivateTaskAccessUnsupported
		}
		return writer.SaveRunAuthorized(ctx, value, authorize)
	})
}

func (s *JSONLStore) SaveContextAuthorized(ctx context.Context, value domaintask.SharedRoleContext, authorize domaintask.TaskWriteAuthorizationCheck) error {
	if s == nil {
		return errors.New("task store is nil")
	}
	return s.Transaction(ctx, func(tx domaintask.Store) error {
		writer, ok := tx.(domaintask.PrivateTaskWriter)
		if !ok {
			return domaintask.ErrPrivateTaskAccessUnsupported
		}
		return writer.SaveContextAuthorized(ctx, value, authorize)
	})
}

func (s *transactionStore) GetTaskAuthorized(ctx context.Context, taskID modulecore.TaskID, authorize domaintask.TaskReadAuthorizationCheck) (domaintask.Task, error) {
	if authorize == nil {
		return domaintask.Task{}, domaintask.ErrPrivateTaskAccessRequired
	}
	task, err := s.getTaskRaw(ctx, taskID)
	if err != nil {
		return domaintask.Task{}, err
	}
	if domaintask.IsOwnerManagedTask(task) {
		return domaintask.Task{}, domaintask.ErrTypedTaskRequiresOwner
	}
	if err := authorizeTask(ctx, task, domaintask.TaskReadResourceTask, "", authorize); err != nil {
		return domaintask.Task{}, hidePrivateTaskReadError(err)
	}
	s.markOwnerOnlyRead(task)
	return task, nil
}

func (s *transactionStore) ListTasksAuthorized(ctx context.Context, filter domaintask.Filter, authorize domaintask.TaskReadAuthorizationCheck) ([]domaintask.Task, error) {
	if authorize == nil {
		return nil, domaintask.ErrPrivateTaskAccessRequired
	}
	items, err := s.loadTasks(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]domaintask.Task, 0, len(items))
	for _, item := range items {
		if domaintask.IsOwnerManagedTask(item) {
			continue
		}
		if !taskMatchesFilter(item, filter) {
			continue
		}
		if item.PrivateAccess != nil {
			err := authorizeTask(ctx, item, domaintask.TaskReadResourceTask, "", authorize)
			if errors.Is(err, domaintask.ErrPrivateTaskAccessForbidden) {
				continue
			}
			if err != nil {
				return nil, err
			}
			s.markOwnerOnlyRead(item)
		}
		filtered = append(filtered, item)
	}
	// Recheck every private source before returning the projection. This avoids
	// returning a result after a Goal owner has revoked or changed its scope.
	for _, item := range filtered {
		if item.PrivateAccess != nil {
			if err := authorizeTask(ctx, item, domaintask.TaskReadResourceTask, "", authorize); err != nil {
				return nil, err
			}
			s.markOwnerOnlyRead(item)
		}
	}
	if filter.Limit > 0 && len(filtered) > filter.Limit {
		filtered = filtered[:filter.Limit]
	}
	return filtered, nil
}

func (s *transactionStore) GetRunAuthorized(ctx context.Context, runID modulecore.RunID, authorize domaintask.TaskReadAuthorizationCheck) (domaintask.Run, error) {
	if authorize == nil {
		return domaintask.Run{}, domaintask.ErrPrivateTaskAccessRequired
	}
	taskID, err := s.getRunTaskIDRaw(ctx, runID)
	if err != nil {
		return domaintask.Run{}, err
	}
	task, err := s.getTaskRaw(ctx, taskID)
	if err != nil {
		return domaintask.Run{}, domaintask.ErrNotFound
	}
	if domaintask.IsOwnerManagedTask(task) {
		return domaintask.Run{}, domaintask.ErrTypedTaskRequiresOwner
	}
	if err := authorizeTask(ctx, task, domaintask.TaskReadResourceRun, runID, authorize); err != nil {
		return domaintask.Run{}, hidePrivateTaskReadError(err)
	}
	s.markOwnerOnlyRead(task)
	var run domaintask.Run
	run, err = s.getRunRaw(ctx, runID)
	if err != nil {
		return domaintask.Run{}, err
	}
	if run.TaskID != task.TaskID {
		return domaintask.Run{}, domaintask.ErrNotFound
	}
	if err := authorizeTask(ctx, task, domaintask.TaskReadResourceRun, runID, authorize); err != nil {
		return domaintask.Run{}, hidePrivateTaskReadError(err)
	}
	s.markOwnerOnlyRead(task)
	return run, nil
}

func (s *transactionStore) ListRunsAuthorized(ctx context.Context, filter domaintask.RunFilter, authorize domaintask.TaskReadAuthorizationCheck) ([]domaintask.Run, error) {
	if authorize == nil {
		return nil, domaintask.ErrPrivateTaskAccessRequired
	}
	runs, err := s.loadRuns(ctx)
	if err != nil {
		return nil, err
	}
	tasks, err := s.loadTasks(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateRunOwners(runs, tasks); err != nil {
		return nil, err
	}
	byID := make(map[modulecore.TaskID]domaintask.Task, len(tasks))
	for _, task := range tasks {
		byID[task.TaskID] = task
	}
	filtered := make([]domaintask.Run, 0, len(runs))
	for _, run := range runs {
		if filter.TaskID != "" && run.TaskID != filter.TaskID || filter.Status != "" && run.Status != filter.Status {
			continue
		}
		task, ok := byID[run.TaskID]
		if !ok {
			continue
		}
		if domaintask.IsOwnerManagedTask(task) {
			continue
		}
		if task.PrivateAccess != nil {
			if err := authorizeTask(ctx, task, domaintask.TaskReadResourceRun, run.RunID, authorize); err != nil {
				if errors.Is(err, domaintask.ErrPrivateTaskAccessForbidden) {
					continue
				}
				return nil, err
			}
			s.markOwnerOnlyRead(task)
		}
		filtered = append(filtered, run)
	}
	for _, run := range filtered {
		task := byID[run.TaskID]
		if task.PrivateAccess != nil {
			if err := authorizeTask(ctx, task, domaintask.TaskReadResourceRun, run.RunID, authorize); err != nil {
				return nil, err
			}
			s.markOwnerOnlyRead(task)
		}
	}
	if filter.Limit > 0 && len(filtered) > filter.Limit {
		filtered = filtered[:filter.Limit]
	}
	return filtered, nil
}

func (s *transactionStore) GetContextAuthorized(ctx context.Context, taskID modulecore.TaskID, authorize domaintask.TaskReadAuthorizationCheck) (domaintask.SharedRoleContext, error) {
	if authorize == nil {
		return domaintask.SharedRoleContext{}, domaintask.ErrPrivateTaskAccessRequired
	}
	task, err := s.getTaskRaw(ctx, taskID)
	if err != nil {
		return domaintask.SharedRoleContext{}, err
	}
	if domaintask.IsOwnerManagedTask(task) {
		return domaintask.SharedRoleContext{}, domaintask.ErrTypedTaskRequiresOwner
	}
	if err := authorizeTask(ctx, task, domaintask.TaskReadResourceContext, "", authorize); err != nil {
		return domaintask.SharedRoleContext{}, hidePrivateTaskReadError(err)
	}
	s.markOwnerOnlyRead(task)
	value, err := s.getContextRaw(ctx, taskID)
	if err != nil {
		return domaintask.SharedRoleContext{}, err
	}
	if err := authorizeTask(ctx, task, domaintask.TaskReadResourceContext, "", authorize); err != nil {
		return domaintask.SharedRoleContext{}, hidePrivateTaskReadError(err)
	}
	s.markOwnerOnlyRead(task)
	return value, nil
}

func (s *transactionStore) markOwnerOnlyRead(task domaintask.Task) {
	if s == nil || s.state == nil || task.PrivateAccess == nil && !domaintask.IsOwnerManagedTask(task) {
		return
	}
	s.state.mu.Lock()
	s.state.ownerOnlyAccessed = true
	s.state.mu.Unlock()
}

func authorizeTask(ctx context.Context, task domaintask.Task, resource domaintask.TaskReadResource, runID modulecore.RunID, authorize domaintask.TaskReadAuthorizationCheck) error {
	if task.PrivateAccess == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("private Task read context is nil")
	}
	if authorize == nil {
		return domaintask.ErrPrivateTaskAccessRequired
	}
	input, err := domaintask.TaskReadInput(task, resource, runID)
	if err != nil {
		return domaintask.ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := authorize(ctx, input); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return domaintask.ErrPrivateTaskAccessForbidden
	}
	return ctx.Err()
}

func (s *transactionStore) getTaskRaw(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	if err := s.check(ctx); err != nil {
		return domaintask.Task{}, err
	}
	if err := taskID.Validate(); err != nil {
		return domaintask.Task{}, err
	}
	if s.parent.idx != nil {
		return s.idxGetTaskRaw(taskID)
	}
	items, err := s.loadTasks(ctx)
	if err != nil {
		return domaintask.Task{}, err
	}
	for _, item := range items {
		if item.TaskID == taskID {
			return item, nil
		}
	}
	return domaintask.Task{}, domaintask.ErrNotFound
}

func (s *transactionStore) getRunRaw(ctx context.Context, runID modulecore.RunID) (domaintask.Run, error) {
	if err := s.check(ctx); err != nil {
		return domaintask.Run{}, err
	}
	if err := runID.Validate(); err != nil {
		return domaintask.Run{}, err
	}
	if s.parent.idx != nil {
		return s.idxGetRunRaw(runID)
	}
	runs, err := s.loadRuns(ctx)
	if err != nil {
		return domaintask.Run{}, err
	}
	tasks, err := s.loadTasks(ctx)
	if err != nil {
		return domaintask.Run{}, err
	}
	if err := validateRunOwners(runs, tasks); err != nil {
		return domaintask.Run{}, err
	}
	for _, run := range runs {
		if run.RunID == runID {
			return run, nil
		}
	}
	return domaintask.Run{}, domaintask.ErrNotFound
}

func (s *transactionStore) getRunTaskIDRaw(ctx context.Context, runID modulecore.RunID) (modulecore.TaskID, error) {
	if err := s.check(ctx); err != nil {
		return "", err
	}
	if err := runID.Validate(); err != nil {
		return "", err
	}
	if s.parent.idx != nil {
		return s.idxRunTaskID(runID)
	}
	runs, err := s.loadRuns(ctx)
	if err != nil {
		return "", err
	}
	for _, run := range runs {
		if run.RunID == runID {
			return run.TaskID, nil
		}
	}
	return "", domaintask.ErrNotFound
}

func (s *transactionStore) getContextRaw(ctx context.Context, taskID modulecore.TaskID) (domaintask.SharedRoleContext, error) {
	if err := s.check(ctx); err != nil {
		return domaintask.SharedRoleContext{}, err
	}
	if err := taskID.Validate(); err != nil {
		return domaintask.SharedRoleContext{}, err
	}
	if s.parent.idx != nil {
		return s.idxGetContextRaw(taskID)
	}
	items, err := readJSONLLinesWithPending[domaintask.SharedRoleContext](ctx, s.parent.contextPath, s.pendingBytes(contextFilename))
	if err != nil {
		return domaintask.SharedRoleContext{}, err
	}
	for index := len(items) - 1; index >= 0; index-- {
		if items[index].TaskID == taskID {
			return items[index], nil
		}
	}
	return domaintask.SharedRoleContext{}, domaintask.ErrNotFound
}

var _ domaintask.PrivateTaskReader = (*transactionStore)(nil)
var _ domaintask.PrivateTaskWriter = (*transactionStore)(nil)

func hidePrivateTaskReadError(err error) error {
	if errors.Is(err, domaintask.ErrPrivateTaskAccessForbidden) || errors.Is(err, domaintask.ErrPrivateTaskAccessInvalid) || errors.Is(err, domaintask.ErrNotFound) {
		return domaintask.ErrNotFound
	}
	return err
}

func samePrivateTaskAccess(left, right domaintask.Task) bool {
	if left.TaskID != right.TaskID || left.WorkstreamID != right.WorkstreamID || left.GoalID != right.GoalID ||
		left.PrivateAccess == nil || right.PrivateAccess == nil {
		return false
	}
	return *left.PrivateAccess == *right.PrivateAccess
}
