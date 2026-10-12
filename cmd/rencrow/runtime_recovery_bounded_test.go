package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// The restart recoveries must not enumerate the whole Task store: on the
// production store that decodes tens of thousands of Runs at every start, and
// with the ID index it is the one remaining caller that would. F-1 narrows the
// orphan Run scan to running Runs and F-2 resolves each Action's Run by ID. The
// spy records only the reads the recoveries issue directly (Task transactions
// use the inner view), so an unbounded list shows up as a recorded call.

type listingSpyTaskStore struct {
	domaintask.Store
	mu         sync.Mutex
	runFilters []domaintask.RunFilter
	taskLists  int
	getRuns    []modulecore.RunID
}

func (s *listingSpyTaskStore) ListRuns(ctx context.Context, filter domaintask.RunFilter) ([]domaintask.Run, error) {
	s.mu.Lock()
	s.runFilters = append(s.runFilters, filter)
	s.mu.Unlock()
	return s.Store.ListRuns(ctx, filter)
}

func (s *listingSpyTaskStore) ListTasks(ctx context.Context, filter domaintask.Filter) ([]domaintask.Task, error) {
	s.mu.Lock()
	s.taskLists++
	s.mu.Unlock()
	return s.Store.ListTasks(ctx, filter)
}

func (s *listingSpyTaskStore) GetRun(ctx context.Context, runID modulecore.RunID) (domaintask.Run, error) {
	s.mu.Lock()
	s.getRuns = append(s.getRuns, runID)
	s.mu.Unlock()
	return s.Store.GetRun(ctx, runID)
}

// restartedSpyRuntime opens the Task store twice over the same root so the
// second open holds a newer writer generation, as a restarted process does.
type restartedSpyRuntime struct {
	first   *taskmanager.Manager
	actions *actionmanager.Manager
	second  *taskmanager.Manager
	spy     *listingSpyTaskStore
	reopen  func(t *testing.T)
}

// newRestartedSpyRuntime builds the pre-restart runtime. After reopen, the
// restarted Task owner is ID-indexed (the store is opened with the index and
// the Manager declares it, as the runtime does for task_store.index.enabled) or
// the default store without one.
func newRestartedSpyRuntime(t *testing.T, indexed bool) *restartedSpyRuntime {
	t.Helper()
	root := t.TempDir()
	taskRoot := filepath.Join(root, "tasks")
	firstStore, err := taskpersistence.NewJSONLStore(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(root, "actions"))
	if err != nil {
		t.Fatal(err)
	}
	runtime := &restartedSpyRuntime{
		first:   taskmanager.New(firstStore, taskmanager.DefaultParallelLimits()),
		actions: actionmanager.New(actionStore),
	}
	runtime.reopen = func(t *testing.T) {
		t.Helper()
		if err := firstStore.Close(); err != nil {
			t.Fatal(err)
		}
		secondStore, err := taskpersistence.NewJSONLStoreWithOptions(taskRoot, taskpersistence.OpenOptions{Index: indexed})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = secondStore.Close() })
		runtime.spy = &listingSpyTaskStore{Store: secondStore}
		var options []taskmanager.Option
		if indexed {
			options = append(options, taskmanager.WithIDIndex())
		}
		runtime.second = taskmanager.New(runtime.spy, taskmanager.DefaultParallelLimits(), options...)
	}
	return runtime
}

func (r *restartedSpyRuntime) startRun(t *testing.T, title string) (domaintask.Task, domaintask.Run) {
	t.Helper()
	return newRunningTaskRun(t, r.first, title, domaintask.RouteGeneral, "mio")
}

func (r *restartedSpyRuntime) finishedTask(t *testing.T, title string) (domaintask.Task, domaintask.Run) {
	t.Helper()
	task, run := r.startRun(t, title)
	if _, err := r.first.Succeed(context.Background(), task.TaskID, "done"); err != nil {
		t.Fatal(err)
	}
	return task, run
}

func (r *restartedSpyRuntime) terminalAction(t *testing.T, task domaintask.Task, run domaintask.Run, status domainaction.Status) domainaction.Action {
	t.Helper()
	ctx := context.Background()
	action, attempt, err := r.actions.CreateAction(ctx, actionmanager.CreateInput{
		TaskID: task.TaskID, RunID: run.RunID, Kind: domainaction.KindLLM, Name: "llm.worker.generate",
	})
	if err != nil {
		t.Fatal(err)
	}
	attemptStatus := domainaction.AttemptStatusSucceeded
	if status == domainaction.StatusCancelled {
		attemptStatus = domainaction.AttemptStatusCancelled
	}
	if _, _, err := r.actions.CompleteAttempt(ctx, action.ActionID, attempt.AttemptID, attemptStatus, status, "completed"); err != nil {
		t.Fatal(err)
	}
	return action
}

func TestRecoverOrphanTaskRunsAfterRestartListsOnlyRunningRuns(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		t.Run(indexModeName(indexed), func(t *testing.T) { testRecoverOrphanTaskRunsListsOnlyRunningRuns(t, indexed) })
	}
}

func indexModeName(indexed bool) string {
	if indexed {
		return "id_index"
	}
	return "default_store"
}

func testRecoverOrphanTaskRunsListsOnlyRunningRuns(t *testing.T, indexed bool) {
	ctx := context.Background()
	runtime := newRestartedSpyRuntime(t, indexed)
	var finished []domaintask.Run
	for _, title := range []string{"history one", "history two", "history three"} {
		_, run := runtime.finishedTask(t, title)
		finished = append(finished, run)
	}
	staleTask, staleRun := runtime.startRun(t, "stale worker")
	runtime.reopen(t)

	recovered, err := recoverOrphanTaskRunsAfterRestart(ctx, runtime.actions, runtime.second)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("recovered=%d, want 1 (only the stale running Run)", recovered)
	}

	for _, filter := range runtime.spy.runFilters {
		if filter.Status != domaintask.RunStatusRunning {
			t.Fatalf("orphan Run recovery listed Runs with filter %+v; it must read only running Runs", filter)
		}
	}
	if len(runtime.spy.runFilters) == 0 {
		t.Fatal("orphan Run recovery never listed running Runs")
	}

	stale, err := runtime.second.Get(ctx, staleTask.TaskID)
	if err != nil || stale.Status != domaintask.StatusFailed {
		t.Fatalf("stale Task = %+v err=%v, want failed", stale, err)
	}
	if run, err := runtime.second.GetRun(ctx, staleRun.RunID); err != nil || run.Status != domaintask.RunStatusFailed {
		t.Fatalf("stale Run = %+v err=%v, want failed", run, err)
	}
	for _, finishedRun := range finished {
		run, err := runtime.second.GetRun(ctx, finishedRun.RunID)
		if err != nil || run.Status != domaintask.RunStatusSucceeded {
			t.Fatalf("finished Run %s = %+v err=%v, want it left succeeded", finishedRun.RunID, run, err)
		}
	}
}

func TestRecoverActionRunsAfterRestartResolvesRunsWithoutAMapOfTheHistory(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		t.Run(indexModeName(indexed), func(t *testing.T) { testRecoverActionRunsResolvesRuns(t, indexed) })
	}
}

func testRecoverActionRunsResolvesRuns(t *testing.T, indexed bool) {
	ctx := context.Background()
	runtime := newRestartedSpyRuntime(t, indexed)

	// One stale running Run explained by two terminal Actions, and one Run that
	// already finished before the restart.
	staleTask, staleRun := runtime.startRun(t, "stale with two actions")
	runtime.terminalAction(t, staleTask, staleRun, domainaction.StatusSucceeded)
	runtime.terminalAction(t, staleTask, staleRun, domainaction.StatusCancelled)
	doneTask, doneRun := runtime.finishedTask(t, "already finished")
	runtime.terminalAction(t, doneTask, doneRun, domainaction.StatusSucceeded)
	runtime.reopen(t)

	recovered, err := recoverActionRunsAfterRestart(ctx, runtime.actions, runtime.second)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("recovered=%d, want 1 (the stale Run is closed once)", recovered)
	}
	if indexed {
		// With the ID index each Action's Run is read by ID; nothing lists the history.
		if len(runtime.spy.runFilters) != 0 || len(runtime.spy.getRuns) != 3 {
			t.Fatalf("indexed recovery listed Runs %+v and read %d Runs by ID, want no list and one read per Action", runtime.spy.runFilters, len(runtime.spy.getRuns))
		}
	} else {
		// Without it reading Runs one by one would fold the whole history for each
		// Action, so the Task owner makes exactly one pass and no by-ID reads.
		if len(runtime.spy.runFilters) != 1 || runtime.spy.runFilters[0] != (domaintask.RunFilter{}) || len(runtime.spy.getRuns) != 0 {
			t.Fatalf("default-store recovery listed Runs %+v and read %d Runs by ID, want one pass and no by-ID reads", runtime.spy.runFilters, len(runtime.spy.getRuns))
		}
	}

	staleRecord, err := runtime.second.Get(ctx, staleTask.TaskID)
	if err != nil || staleRecord.Status != domaintask.StatusSucceeded {
		t.Fatalf("stale Task = %+v err=%v, want succeeded from its first terminal Action", staleRecord, err)
	}
	if run, err := runtime.second.GetRun(ctx, staleRun.RunID); err != nil || run.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("stale Run = %+v err=%v, want succeeded", run, err)
	}
	if run, err := runtime.second.GetRun(ctx, doneRun.RunID); err != nil || run.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("finished Run = %+v err=%v, want it left succeeded", run, err)
	}
}

// A reference to a Run that no Task owns is data corruption: the recovery
// keeps failing on it, as it did when it held every Run in a map, and the
// error still matches ErrNotFound.
func TestRecoverActionRunsAfterRestartFailsOnActionWithoutRun(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		t.Run(indexModeName(indexed), func(t *testing.T) { testRecoverActionRunsFailsOnActionWithoutRun(t, indexed) })
	}
}

func testRecoverActionRunsFailsOnActionWithoutRun(t *testing.T, indexed bool) {
	ctx := context.Background()
	runtime := newRestartedSpyRuntime(t, indexed)
	dangling := domaintask.Task{TaskID: modulecore.NewTaskID()}
	danglingRun := domaintask.Run{RunID: modulecore.NewRunID(), TaskID: dangling.TaskID}
	action := runtime.terminalAction(t, dangling, danglingRun, domainaction.StatusSucceeded)
	runtime.reopen(t)

	recovered, err := recoverActionRunsAfterRestart(ctx, runtime.actions, runtime.second)
	if err == nil {
		t.Fatal("an Action whose Run does not exist was accepted")
	}
	if !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("error = %v, want errors.Is(ErrNotFound)", err)
	}
	if !strings.Contains(err.Error(), string(action.ActionID)) || !strings.Contains(err.Error(), string(danglingRun.RunID)) {
		t.Fatalf("error %q does not name the Action and Run", err)
	}
	if recovered != 0 {
		t.Fatalf("recovered=%d, want 0", recovered)
	}
}
