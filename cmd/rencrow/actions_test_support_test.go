package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
)

// Helpers of the tests that run the Action store both ways (with the snapshot and
// without it): a spy that records every Action filter that reaches the store, and
// a restarted Task store with an Action store holding the history of the first
// process.

// listSpyStore records every Action filter that reaches the store, at the top
// level and inside transactions.
type listSpyStore struct {
	domainaction.Store
	mu      sync.Mutex
	filters []domainaction.Filter
}

func (s *listSpyStore) record(f domainaction.Filter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filters = append(s.filters, f)
}

func (s *listSpyStore) seen() []domainaction.Filter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domainaction.Filter(nil), s.filters...)
}

func (s *listSpyStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filters = nil
}

func (s *listSpyStore) ListActions(ctx context.Context, f domainaction.Filter) ([]domainaction.Action, error) {
	s.record(f)
	return s.Store.ListActions(ctx, f)
}

func (s *listSpyStore) Transaction(ctx context.Context, fn func(domainaction.Store) error) error {
	return s.Store.Transaction(ctx, func(tx domainaction.Store) error { return fn(&listSpyTx{Store: tx, spy: s}) })
}

func (s *listSpyStore) ReadTransaction(ctx context.Context, fn func(domainaction.Store) error) error {
	return s.Store.ReadTransaction(ctx, func(tx domainaction.Store) error { return fn(&listSpyTx{Store: tx, spy: s}) })
}

type listSpyTx struct {
	domainaction.Store
	spy *listSpyStore
}

func (t *listSpyTx) ListActions(ctx context.Context, f domainaction.Filter) ([]domainaction.Action, error) {
	t.spy.record(f)
	return t.Store.ListActions(ctx, f)
}

// wholeHistory reports the filters that select the history without a bound a
// restart recovery may rely on: no Run, no Task, no Limit, no Kind with a Name, and
// not the open Actions.
func wholeHistory(filters []domainaction.Filter) []domainaction.Filter {
	var out []domainaction.Filter
	for _, f := range filters {
		switch {
		case f.RunID != "" || f.TaskID != "" || f.Limit > 0:
		case f.Kind != "" && f.Name != "":
		case f.Status == domainaction.StatusOpen && f.Kind == "" && f.Name == "":
		default:
			out = append(out, f)
		}
	}
	return out
}

var actionStoreModes = []struct {
	name  string
	index bool
}{{"full read", false}, {"snapshot", true}}

func openSpiedActionManager(t *testing.T, root string, index bool) (*actionmanager.Manager, *listSpyStore) {
	t.Helper()
	store, err := actionpersistence.NewJSONLStoreWithOptions(root, actionpersistence.OpenOptions{Index: index})
	if err != nil {
		t.Fatal(err)
	}
	spy := &listSpyStore{Store: store}
	return actionmanager.New(spy), spy
}

// restartScenario is a Task store that has been restarted (so the Runs its first
// writer left running are stale) and an Action store holding the history of the
// first process.
type restartScenario struct {
	runtime *restartedTaskRuntime
	actions *actionmanager.Manager
	spy     *listSpyStore
	tasks   *taskmanager.Manager
}

func newRestartScenario(t *testing.T, index bool) *restartScenario {
	t.Helper()
	runtime := newRestartedTaskRuntime(t)
	actions, spy := openSpiedActionManager(t, filepath.Join(t.TempDir(), "actions"), index)
	runtime.actions = actions
	return &restartScenario{runtime: runtime, actions: actions, spy: spy}
}

func (s *restartScenario) restart(t *testing.T) {
	t.Helper()
	s.tasks = s.runtime.restart(t)
}

// history adds n finished Actions on finished Runs: the part of the store a
// restart has nothing to do with.
func (s *restartScenario) history(t *testing.T, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		task, run := newRunningTaskRun(t, s.runtime.firstTasks, fmt.Sprintf("history %d", i), domaintask.RouteGeneral, "mio")
		action, attempt, err := s.actions.CreateAction(ctx, actionmanager.CreateInput{TaskID: task.TaskID, RunID: run.RunID, Kind: domainaction.KindMemoryPromotion, Name: "memory_promotion"})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.actions.CompleteAttempt(ctx, action.ActionID, attempt.AttemptID, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "done"); err != nil {
			t.Fatal(err)
		}
		if err := s.runtime.firstTasks.CloseStartedRun(ctx, task.TaskID, run.RunID, "mio", taskmanager.RunOutcome{Status: domaintask.StatusSucceeded, Summary: "done"}, nil); err != nil {
			t.Fatal(err)
		}
	}
}
