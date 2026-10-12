package taskmanager

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// storeCalls is what the spy saw. The read session runs its reads on the
// transaction view, so the spy wraps that view too.
type storeCalls struct {
	mu           sync.Mutex
	readSessions int
	taskLists    int
	taskGets     int
	runGets      int
	runFilters   []domaintask.RunFilter
}

type readSessionSpy struct {
	domaintask.Store
	calls *storeCalls
}

func newReadSessionSpy(store domaintask.Store) *readSessionSpy {
	return &readSessionSpy{Store: store, calls: &storeCalls{}}
}

func (s *readSessionSpy) ReadTransaction(ctx context.Context, fn func(domaintask.Store) error) error {
	s.calls.mu.Lock()
	s.calls.readSessions++
	s.calls.mu.Unlock()
	return s.Store.ReadTransaction(ctx, func(tx domaintask.Store) error {
		return fn(&readSessionSpy{Store: tx, calls: s.calls})
	})
}

func (s *readSessionSpy) GetTask(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	s.calls.mu.Lock()
	s.calls.taskGets++
	s.calls.mu.Unlock()
	return s.Store.GetTask(ctx, taskID)
}

func (s *readSessionSpy) GetRun(ctx context.Context, runID modulecore.RunID) (domaintask.Run, error) {
	s.calls.mu.Lock()
	s.calls.runGets++
	s.calls.mu.Unlock()
	return s.Store.GetRun(ctx, runID)
}

func (s *readSessionSpy) ListTasks(ctx context.Context, filter domaintask.Filter) ([]domaintask.Task, error) {
	s.calls.mu.Lock()
	s.calls.taskLists++
	s.calls.mu.Unlock()
	return s.Store.ListTasks(ctx, filter)
}

func (s *readSessionSpy) ListRuns(ctx context.Context, filter domaintask.RunFilter) ([]domaintask.Run, error) {
	s.calls.mu.Lock()
	s.calls.runFilters = append(s.calls.runFilters, filter)
	s.calls.mu.Unlock()
	return s.Store.ListRuns(ctx, filter)
}

func (c *storeCalls) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readSessions, c.taskLists, c.taskGets, c.runGets, c.runFilters = 0, 0, 0, 0, nil
}

// idStoreModes are the three ways a Manager can sit on a store: the default
// store without an ID index, an indexed store the Manager is told about
// (WithIDIndex), and an indexed store the Manager is not told about. All three
// must give the same answers.
var idStoreModes = []struct {
	name    string
	index   bool
	declare bool
}{
	{"default_store", false, false},
	{"id_index_declared", true, true},
	{"id_index_not_declared", true, false},
}

func newModeManager(t *testing.T, index, declare bool) (*Manager, *readSessionSpy) {
	t.Helper()
	store, err := taskpersistence.NewJSONLStoreWithOptions(t.TempDir(), taskpersistence.OpenOptions{Index: index})
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	spy := newReadSessionSpy(store)
	var options []Option
	if declare {
		options = append(options, WithIDIndex())
	}
	return New(spy, DefaultParallelLimits(), options...), spy
}

func TestReadTaskRunsReadsNamedTasksAndTheirRunsInOneSession(t *testing.T) {
	for _, mode := range idStoreModes {
		t.Run(mode.name, func(t *testing.T) {
			ctx := context.Background()
			manager, spy := newModeManager(t, mode.index, mode.declare)

			// Three Tasks, one of which ran twice, plus a bystander that must not be returned.
			first, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: "first", Route: domaintask.RouteGeneral, Assignee: "mio"}, domaintask.SharedRoleContext{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Succeed(ctx, first.TaskID, "done"); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.StartRunWithReason(ctx, first.TaskID, domaintask.RunStartReasonExplicitRerun); err != nil {
				t.Fatal(err)
			}
			second, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: "second", Route: domaintask.RouteResearch, Assignee: "shiro"}, domaintask.SharedRoleContext{})
			if err != nil {
				t.Fatal(err)
			}
			bystander, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: "bystander", Route: domaintask.RouteCHAT, Assignee: "kuro"}, domaintask.SharedRoleContext{})
			if err != nil {
				t.Fatal(err)
			}
			missing := modulecore.NewTaskID()

			spy.calls.reset()
			tasks, runs, err := manager.ReadTaskRuns(ctx, []modulecore.TaskID{first.TaskID, second.TaskID, missing, first.TaskID})
			if err != nil {
				t.Fatal(err)
			}
			calls := spy.calls
			if calls.readSessions != 1 {
				t.Fatalf("ReadTaskRuns used %d read sessions, want exactly one", calls.readSessions)
			}
			if mode.declare {
				// ID-indexed: only the three distinct IDs are read, each by ID.
				if calls.taskLists != 0 || calls.taskGets != 3 || len(calls.runFilters) != 2 {
					t.Fatalf("indexed read: taskLists=%d taskGets=%d runLists=%v, want 0 lists, 3 Task reads, 2 Run reads by TaskID", calls.taskLists, calls.taskGets, calls.runFilters)
				}
				for _, filter := range calls.runFilters {
					if filter.TaskID == "" {
						t.Fatalf("indexed read listed all Runs: %+v", filter)
					}
				}
			} else if calls.taskLists != 1 || calls.taskGets != 0 || len(calls.runFilters) != 1 || calls.runFilters[0] != (domaintask.RunFilter{}) {
				// Without an ID index one pass is the cheapest correct read.
				t.Fatalf("one-pass read: taskLists=%d taskGets=%d runLists=%v, want exactly one pass and no by-ID reads", calls.taskLists, calls.taskGets, calls.runFilters)
			}
			if len(tasks) != 2 || len(runs) != 2 {
				t.Fatalf("tasks=%d runs=%d, want the 2 stored Tasks only (a missing Task is absent)", len(tasks), len(runs))
			}
			if _, present := tasks[missing]; present {
				t.Fatalf("missing Task %s must be absent", missing)
			}
			if _, present := tasks[bystander.TaskID]; present {
				t.Fatal("a Task that was not named was read")
			}
			for _, want := range []modulecore.TaskID{first.TaskID, second.TaskID} {
				wantTask, err := manager.Get(ctx, want)
				if err != nil {
					t.Fatal(err)
				}
				wantRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: want})
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(tasks[want], wantTask) || !reflect.DeepEqual(runs[want], wantRuns) {
					t.Fatalf("Task %s: session result differs from Get/ListRuns by ID\ntask=%+v want=%+v\nruns=%+v want=%+v", want, tasks[want], wantTask, runs[want], wantRuns)
				}
			}
			if len(runs[first.TaskID]) != 2 || len(runs[second.TaskID]) != 1 {
				t.Fatalf("run counts = %d/%d, want 2/1", len(runs[first.TaskID]), len(runs[second.TaskID]))
			}
		})
	}
}

func TestRunResolverResolvesByIDOrFromOnePass(t *testing.T) {
	for _, mode := range idStoreModes {
		t.Run(mode.name, func(t *testing.T) {
			ctx := context.Background()
			manager, spy := newModeManager(t, mode.index, mode.declare)
			var runIDs []modulecore.RunID
			for _, title := range []string{"one", "two", "three"} {
				_, run, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: title, Route: domaintask.RouteCHAT, Assignee: "mio"}, domaintask.SharedRoleContext{})
				if err != nil {
					t.Fatal(err)
				}
				runIDs = append(runIDs, run.RunID)
			}

			spy.calls.reset()
			resolve, err := manager.RunResolver(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, runID := range runIDs {
				want, err := manager.GetRun(ctx, runID)
				if err != nil {
					t.Fatal(err)
				}
				got, err := resolve(runID)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("resolve(%s) = %+v err=%v, want %+v", runID, got, err, want)
				}
			}
			if _, err := resolve(modulecore.NewRunID()); !errors.Is(err, domaintask.ErrNotFound) {
				t.Fatalf("resolving an unknown Run: err = %v, want errors.Is(ErrNotFound)", err)
			}
			if mode.declare {
				// Each resolve is one read of that Run by ID; nothing lists the history.
				if len(spy.calls.runFilters) != 0 {
					t.Fatalf("indexed resolver listed Runs: %+v", spy.calls.runFilters)
				}
			} else if len(spy.calls.runFilters) != 1 || spy.calls.runFilters[0] != (domaintask.RunFilter{}) {
				// One pass up front; every later resolve is answered from it.
				t.Fatalf("default-store resolver listed Runs %+v, want exactly one pass", spy.calls.runFilters)
			}
		})
	}
}

func TestReadTaskRunsRefusesUnboundedAndInvalidRequests(t *testing.T) {
	ctx := context.Background()
	manager, spy := newModeManager(t, false, false)

	ids := make([]modulecore.TaskID, 0, MaxReadTaskRunsIDs+1)
	for i := 0; i <= MaxReadTaskRunsIDs; i++ {
		ids = append(ids, modulecore.NewTaskID())
	}
	if _, _, err := manager.ReadTaskRuns(ctx, ids); err == nil || !strings.Contains(err.Error(), "exceeds the limit") {
		t.Fatalf("over-limit read error = %v", err)
	}
	if _, _, err := manager.ReadTaskRuns(ctx, []modulecore.TaskID{"not-a-task-id"}); err == nil {
		t.Fatal("an invalid TaskID was accepted")
	}
	if spy.calls.readSessions != 0 {
		t.Fatalf("a refused request opened %d read session(s), want none", spy.calls.readSessions)
	}
	tasks, runs, err := manager.ReadTaskRuns(ctx, nil)
	if err != nil || len(tasks) != 0 || len(runs) != 0 {
		t.Fatalf("empty request = tasks=%v runs=%v err=%v", tasks, runs, err)
	}
}
