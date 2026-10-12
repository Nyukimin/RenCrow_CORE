package taskmanager

import (
	"context"
	"errors"
	"fmt"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// MaxReadTaskRunsIDs bounds one ReadTaskRuns session. A caller that needs more
// Tasks reads them in several sessions; it never turns the call into a scan of
// the whole store.
const MaxReadTaskRunsIDs = 1000

// ReadTaskRuns reads the named Tasks and every Run each of them issued from
// ONE read transaction, so all of the answers come from the same committed
// snapshot. It is the bounded replacement for listing every Task and every Run
// and joining them by ID in the caller.
//
// With an ID-indexed store (WithIDIndex) the work is proportional to
// len(taskIDs), not to the size of the history. Without one it is a single
// pass over the history inside the same transaction, which is what the callers
// did themselves before; reading the IDs one by one would be far slower there.
//
// A Task that is not stored is absent from both maps; whether that is an error
// is the caller's decision, as it is for Get. The Runs of a Task are in the
// order ListRuns(TaskID) returns them. Duplicate IDs are read once. More than
// MaxReadTaskRunsIDs distinct IDs, or an invalid ID, is an error and nothing is
// read.
func (m *Manager) ReadTaskRuns(ctx context.Context, taskIDs []modulecore.TaskID) (map[modulecore.TaskID]domaintask.Task, map[modulecore.TaskID][]domaintask.Run, error) {
	if m == nil || m.store == nil {
		return nil, nil, errors.New("task manager store is unavailable")
	}
	if ctx == nil {
		return nil, nil, errors.New("task read context is nil")
	}
	distinct := make([]modulecore.TaskID, 0, len(taskIDs))
	seen := make(map[modulecore.TaskID]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		if err := taskID.Validate(); err != nil {
			return nil, nil, fmt.Errorf("task_id is invalid: %w", err)
		}
		if _, duplicate := seen[taskID]; duplicate {
			continue
		}
		seen[taskID] = struct{}{}
		distinct = append(distinct, taskID)
	}
	if len(distinct) > MaxReadTaskRunsIDs {
		return nil, nil, fmt.Errorf("read of %d tasks exceeds the limit of %d", len(distinct), MaxReadTaskRunsIDs)
	}

	tasks := make(map[modulecore.TaskID]domaintask.Task, len(distinct))
	runs := make(map[modulecore.TaskID][]domaintask.Run, len(distinct))
	err := m.readTransaction(ctx, func(txManager *Manager) error {
		if txManager.idIndexed {
			return readTaskRunsByID(ctx, txManager.store, distinct, tasks, runs)
		}
		return readTaskRunsInOnePass(ctx, txManager.store, distinct, seen, tasks, runs)
	})
	if err != nil {
		return nil, nil, err
	}
	return tasks, runs, nil
}

func readTaskRunsByID(ctx context.Context, store Store, taskIDs []modulecore.TaskID, tasks map[modulecore.TaskID]domaintask.Task, runs map[modulecore.TaskID][]domaintask.Run) error {
	for _, taskID := range taskIDs {
		task, err := store.GetTask(ctx, taskID)
		if err != nil {
			if errors.Is(err, domaintask.ErrNotFound) {
				continue
			}
			return fmt.Errorf("read task %s: %w", taskID, err)
		}
		taskRuns, err := store.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
		if err != nil {
			return fmt.Errorf("read runs of task %s: %w", taskID, err)
		}
		tasks[taskID] = task
		runs[taskID] = taskRuns
	}
	return nil
}

// readTaskRunsInOnePass is the path for a store without an ID index: it lists
// the whole history once and keeps only the requested Tasks. It exists because
// the store cannot answer by ID cheaply; remove it when the index is the default.
func readTaskRunsInOnePass(ctx context.Context, store Store, taskIDs []modulecore.TaskID, wanted map[modulecore.TaskID]struct{}, tasks map[modulecore.TaskID]domaintask.Task, runs map[modulecore.TaskID][]domaintask.Run) error {
	if len(taskIDs) == 0 {
		return nil
	}
	allTasks, err := store.ListTasks(ctx, domaintask.Filter{})
	if err != nil {
		return fmt.Errorf("list tasks for bounded read: %w", err)
	}
	for _, task := range allTasks {
		if _, ok := wanted[task.TaskID]; ok {
			tasks[task.TaskID] = task
		}
	}
	if len(tasks) == 0 {
		return nil
	}
	allRuns, err := store.ListRuns(ctx, domaintask.RunFilter{})
	if err != nil {
		return fmt.Errorf("list runs for bounded read: %w", err)
	}
	for taskID := range tasks {
		runs[taskID] = nil
	}
	for _, run := range allRuns {
		if _, ok := tasks[run.TaskID]; ok {
			runs[run.TaskID] = append(runs[run.TaskID], run)
		}
	}
	return nil
}

// RunResolver returns a function that resolves Runs by ID for a caller that
// resolves many of them in one pass (startup recovery). The function reports
// ErrNotFound for a Run that is not stored.
//
// With an ID-indexed store (WithIDIndex) each call reads that one Run. Without
// one, the whole Run history is read ONCE up front and the calls are answered
// from that snapshot, because resolving N Runs one by one would cost N full
// passes. The snapshot is the same data the callers used to hold themselves; a
// caller that acts on a Run re-validates it inside its own transaction.
func (m *Manager) RunResolver(ctx context.Context) (func(modulecore.RunID) (domaintask.Run, error), error) {
	if m == nil || m.store == nil {
		return nil, errors.New("task manager store is unavailable")
	}
	if ctx == nil {
		return nil, errors.New("task run resolver context is nil")
	}
	if m.idIndexed {
		return func(runID modulecore.RunID) (domaintask.Run, error) { return m.store.GetRun(ctx, runID) }, nil
	}
	all, err := m.store.ListRuns(ctx, domaintask.RunFilter{})
	if err != nil {
		return nil, fmt.Errorf("list runs for run resolver: %w", err)
	}
	byID := make(map[modulecore.RunID]domaintask.Run, len(all))
	for _, run := range all {
		byID[run.RunID] = run
	}
	return func(runID modulecore.RunID) (domaintask.Run, error) {
		run, ok := byID[runID]
		if !ok {
			return domaintask.Run{}, domaintask.ErrNotFound
		}
		return run, nil
	}, nil
}
