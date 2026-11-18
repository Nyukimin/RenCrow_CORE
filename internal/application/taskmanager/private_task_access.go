package taskmanager

import (
	"context"
	"errors"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// PrivateTaskReadOwner is the explicit native reader for a Task whose source
// Goal is user-private. The callback must re-read and authorize the exact
// current Goal link on each invocation; it does not grant execution admission.
func (m *Manager) GetTaskAuthorized(ctx context.Context, taskID modulecore.TaskID, authorize domaintask.TaskReadAuthorizationCheck) (domaintask.Task, error) {
	if m == nil || m.store == nil {
		return domaintask.Task{}, errors.New("task manager store is unavailable")
	}
	reader, ok := m.store.(domaintask.PrivateTaskReader)
	if !ok {
		return domaintask.Task{}, domaintask.ErrPrivateTaskAccessUnsupported
	}
	return reader.GetTaskAuthorized(ctx, taskID, authorize)
}

func (m *Manager) ListTasksAuthorized(ctx context.Context, filter domaintask.Filter, authorize domaintask.TaskReadAuthorizationCheck) ([]domaintask.Task, error) {
	if m == nil || m.store == nil {
		return nil, errors.New("task manager store is unavailable")
	}
	reader, ok := m.store.(domaintask.PrivateTaskReader)
	if !ok {
		return nil, domaintask.ErrPrivateTaskAccessUnsupported
	}
	return reader.ListTasksAuthorized(ctx, filter, authorize)
}

func (m *Manager) GetRunAuthorized(ctx context.Context, runID modulecore.RunID, authorize domaintask.TaskReadAuthorizationCheck) (domaintask.Run, error) {
	if m == nil || m.store == nil {
		return domaintask.Run{}, errors.New("task manager store is unavailable")
	}
	reader, ok := m.store.(domaintask.PrivateTaskReader)
	if !ok {
		return domaintask.Run{}, domaintask.ErrPrivateTaskAccessUnsupported
	}
	return reader.GetRunAuthorized(ctx, runID, authorize)
}

func (m *Manager) ListRunsAuthorized(ctx context.Context, filter domaintask.RunFilter, authorize domaintask.TaskReadAuthorizationCheck) ([]domaintask.Run, error) {
	if m == nil || m.store == nil {
		return nil, errors.New("task manager store is unavailable")
	}
	reader, ok := m.store.(domaintask.PrivateTaskReader)
	if !ok {
		return nil, domaintask.ErrPrivateTaskAccessUnsupported
	}
	return reader.ListRunsAuthorized(ctx, filter, authorize)
}

func (m *Manager) GetContextAuthorized(ctx context.Context, taskID modulecore.TaskID, authorize domaintask.TaskReadAuthorizationCheck) (domaintask.SharedRoleContext, error) {
	if m == nil || m.store == nil {
		return domaintask.SharedRoleContext{}, errors.New("task manager store is unavailable")
	}
	reader, ok := m.store.(domaintask.PrivateTaskReader)
	if !ok {
		return domaintask.SharedRoleContext{}, domaintask.ErrPrivateTaskAccessUnsupported
	}
	return reader.GetContextAuthorized(ctx, taskID, authorize)
}

// SaveTaskAuthorized persists owner-bound privacy metadata. Its authorization
// is limited to persistence and cannot start or admit a Task Run.
func (m *Manager) SaveTaskAuthorized(ctx context.Context, task domaintask.Task, authorize domaintask.TaskWriteAuthorizationCheck) error {
	if m == nil || m.store == nil {
		return errors.New("task manager store is unavailable")
	}
	writer, ok := m.store.(domaintask.PrivateTaskWriter)
	if !ok {
		return domaintask.ErrPrivateTaskAccessUnsupported
	}
	return writer.SaveTaskAuthorized(ctx, task, authorize)
}

func (m *Manager) SaveRunAuthorized(ctx context.Context, run domaintask.Run, authorize domaintask.TaskWriteAuthorizationCheck) error {
	if m == nil || m.store == nil {
		return errors.New("task manager store is unavailable")
	}
	writer, ok := m.store.(domaintask.PrivateTaskWriter)
	if !ok {
		return domaintask.ErrPrivateTaskAccessUnsupported
	}
	return writer.SaveRunAuthorized(ctx, run, authorize)
}

func (m *Manager) SaveContextAuthorized(ctx context.Context, shared domaintask.SharedRoleContext, authorize domaintask.TaskWriteAuthorizationCheck) error {
	if m == nil || m.store == nil {
		return errors.New("task manager store is unavailable")
	}
	writer, ok := m.store.(domaintask.PrivateTaskWriter)
	if !ok {
		return domaintask.ErrPrivateTaskAccessUnsupported
	}
	return writer.SaveContextAuthorized(ctx, shared, authorize)
}
