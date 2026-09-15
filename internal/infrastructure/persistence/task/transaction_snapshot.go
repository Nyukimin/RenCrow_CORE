package task

import (
	"context"
	"errors"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// transactionTaskRunBase is the committed Task/Run view for one transaction.
// It is never returned to a caller. The transaction state owns it until the
// transaction ends, and nested read views share the same immutable base.
type transactionTaskRunBase struct {
	tasks          []domaintask.Task
	tasksErr       error
	runs           []domaintask.Run
	runsErr        error
	runRecordCount int
}

// transactionTaskRunView is the current read-your-writes view derived from a
// committed base and the transaction's pending Task/Run appends. Its records
// are internal copies; callers receive another defensive copy.
type transactionTaskRunView struct {
	tasks    []domaintask.Task
	tasksErr error
	runs     []domaintask.Run
	runsErr  error
}

func (s *transactionStore) taskRunView(ctx context.Context) (*transactionTaskRunView, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	if s.state == nil {
		return nil, errTaskTransactionExpired
	}

	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !s.state.active {
		return nil, errTaskTransactionExpired
	}
	if !s.state.baseLoaded {
		base, err := loadTransactionTaskRunBase(ctx, s.parent)
		if err != nil {
			return nil, err
		}
		s.state.base = base
		s.state.baseLoaded = true
	}
	if s.state.view != nil && s.state.viewVersion == s.state.pendingVersion {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return s.state.view, nil
	}

	view := deriveTransactionTaskRunView(
		ctx,
		s.state.base,
		s.state.pending[stateFilename],
		s.state.pending[runFilename],
	)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A context error is a property of this read attempt, not of the
	// transaction's immutable data. Do not retain it as a cached view so a
	// caller that deliberately retries with a live context keeps the prior
	// behavior.
	if !isTransactionContextError(view.tasksErr) && !isTransactionContextError(view.runsErr) {
		s.state.view = view
		s.state.viewVersion = s.state.pendingVersion
	}
	return view, nil
}

func loadTransactionTaskRunBase(ctx context.Context, parent *JSONLStore) (*transactionTaskRunBase, error) {
	if parent == nil {
		return nil, errors.New("task store is unavailable")
	}
	tasks, tasksErr := readJSONLLines[domaintask.Task](ctx, parent.statePath)
	if isTransactionContextError(tasksErr) {
		return nil, tasksErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runs, runsErr := readJSONLLines[domaintask.Run](ctx, parent.runPath)
	if isTransactionContextError(runsErr) {
		return nil, runsErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	base := &transactionTaskRunBase{tasksErr: tasksErr, runsErr: runsErr, runRecordCount: len(runs)}
	if tasksErr == nil {
		base.tasks, base.tasksErr = latestTransactionTasks(tasks)
	}
	if runsErr == nil {
		base.runs, base.runsErr = latestTransactionRuns(runs)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return base, nil
}

func deriveTransactionTaskRunView(ctx context.Context, base *transactionTaskRunBase, pendingTasks, pendingRuns []byte) *transactionTaskRunView {
	view := &transactionTaskRunView{}
	if base == nil {
		view.tasksErr = errors.New("task transaction snapshot is unavailable")
		view.runsErr = view.tasksErr
		return view
	}
	view.tasks, view.tasksErr = overlayTransactionTasks(ctx, base, pendingTasks)
	view.runs, view.runsErr = overlayTransactionRuns(ctx, base, pendingRuns)
	return view
}

func latestTransactionTasks(records []domaintask.Task) ([]domaintask.Task, error) {
	result, err := foldTaskRecords(records)
	if err != nil {
		return nil, err
	}
	return cloneTransactionTasks(result), nil
}

func latestTransactionRuns(records []domaintask.Run) ([]domaintask.Run, error) {
	result, err := foldRunRecords(records, 0)
	if err != nil {
		return nil, err
	}
	return cloneTransactionRuns(result), nil
}

func overlayTransactionTasks(ctx context.Context, base *transactionTaskRunBase, pending []byte) ([]domaintask.Task, error) {
	if base.tasksErr != nil {
		return nil, base.tasksErr
	}
	records := make([]domaintask.Task, 0, len(base.tasks))
	records = append(records, base.tasks...)
	if len(pending) > 0 {
		additional, err := readJSONLBytes[domaintask.Task](ctx, pending)
		if err != nil {
			return nil, err
		}
		records = append(records, additional...)
	}
	return latestTransactionTasks(records)
}

func overlayTransactionRuns(ctx context.Context, base *transactionTaskRunBase, pending []byte) ([]domaintask.Run, error) {
	if base.runsErr != nil {
		return nil, base.runsErr
	}
	fold := newRunRecordFoldFromLatest(base.runs)
	if len(pending) > 0 {
		additional, err := readJSONLBytes[domaintask.Run](ctx, pending)
		if err != nil {
			return nil, err
		}
		for index, item := range additional {
			recordIndex := base.runRecordCount + index
			if err := fold.apply(recordIndex, item); err != nil {
				return nil, err
			}
		}
	}
	return cloneTransactionRuns(fold.records()), nil
}

func isTransactionContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func cloneTransactionTask(value domaintask.Task) domaintask.Task {
	value.CoderRoles = append([]string(nil), value.CoderRoles...)
	value.DependencyTaskIDs = append([]modulecore.TaskID(nil), value.DependencyTaskIDs...)
	value.NextActions = append([]string(nil), value.NextActions...)
	value.Evidence = append([]string(nil), value.Evidence...)
	value.Artifacts = append([]string(nil), value.Artifacts...)
	value.StartedAt = cloneTransactionTime(value.StartedAt)
	value.FinishedAt = cloneTransactionTime(value.FinishedAt)
	return value
}

func cloneTransactionRun(value domaintask.Run) domaintask.Run {
	value.CompletedAt = cloneTransactionTime(value.CompletedAt)
	return value
}

func cloneTransactionTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneTransactionTasks(values []domaintask.Task) []domaintask.Task {
	result := make([]domaintask.Task, len(values))
	for index, value := range values {
		result[index] = cloneTransactionTask(value)
	}
	return result
}

func cloneTransactionRuns(values []domaintask.Run) []domaintask.Run {
	result := make([]domaintask.Run, len(values))
	for index, value := range values {
		result[index] = cloneTransactionRun(value)
	}
	return result
}
