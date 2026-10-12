package taskmanagertest

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	transportpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// ErrInjected is the cause every fault of Closure injects. Tests match it with
// errors.Is, so a swallowed or replaced cause is visible.
var ErrInjected = errors.New("injected fault")

// ErrTerminalRefused is what the stores return for a terminal write when
// ClosureOptions asks for one to be refused.
var ErrTerminalRefused = errors.New("terminal write refused")

// observedStoreWrite is what one Task or Action write took on the production
// host (7 to 11 seconds, 2026-10-12, with about 55,000 Actions on file). A
// terminal write must be allowed at least three times that, the rule the Tool,
// Heartbeat, OPS ingress, and Memory Promotion terminal writes follow.
const observedStoreWrite = 10 * time.Second

// minimumTerminalBudget is the shortest budget a terminal write may be given.
const minimumTerminalBudget = 3 * observedStoreWrite

// ClosureOptions selects the faults of a Closure. The zero value injects none.
type ClosureOptions struct {
	// ActionCreateErr makes the first Action transaction (CreateAction) fail.
	ActionCreateErr error
	// OnActionCreateFail runs when that transaction is refused, before the error
	// is returned, so a test can end the caller's context at the moment the store
	// gives up, as a deadline does in production.
	OnActionCreateFail func()
	// OnActionCreated runs once the Action and its Attempt were stored, so a test
	// can end the caller's context while the work runs.
	OnActionCreated func()
	// ActionTerminalErr makes every Action transaction after the first fail.
	ActionTerminalErr error
	// TaskTerminalErr makes every Task transaction after the first fail. The
	// first one is the admission (CreateAndStartRun).
	TaskTerminalErr error
}

// Closure is a real Task owner and a real Action owner over JSONL stores, with
// the faults of ClosureOptions injected at the stores. It exists to prove what a
// function that has started a Run leaves behind when it fails afterwards.
//
// Its global execution capacity is one, so any Run that is left running makes
// the next admission fail with taskmanager.ErrParallelLimit.
type Closure struct {
	Tasks   *taskmanager.Manager
	Actions *actionmanager.Manager

	taskStore   *budgetTaskStore
	actionStore *budgetActionStore
}

// NewClosure builds the owners. The stores are closed on cleanup.
func NewClosure(t testing.TB, options ClosureOptions) *Closure {
	t.Helper()
	baseTasks, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatalf("create Task store: %v", err)
	}
	t.Cleanup(func() {
		if err := baseTasks.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	baseActions, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatalf("create Action store: %v", err)
	}
	closure := &Closure{
		taskStore:   &budgetTaskStore{Store: baseTasks, terminalErr: options.TaskTerminalErr},
		actionStore: &budgetActionStore{Store: baseActions, createErr: options.ActionCreateErr, onCreateFail: options.OnActionCreateFail, onCreated: options.OnActionCreated, terminalErr: options.ActionTerminalErr},
	}
	closure.Tasks = taskmanager.New(closure.taskStore, taskmanager.ParallelLimits{
		Global: 1, PerModule: 1, CodingTasks: 1, LongResearchTasks: 1, DestructiveTasks: 1,
	})
	closure.Actions = actionmanager.New(closure.actionStore)
	return closure
}

// NewTransport builds a Transport owner over the Closure's Action owner.
func (c *Closure) NewTransport(t testing.TB) *transportmanager.Manager {
	t.Helper()
	store, err := transportpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "transport"))
	if err != nil {
		t.Fatalf("create Transport store: %v", err)
	}
	return transportmanager.New(store, c.Actions)
}

// ReceiptBoundContext returns a context that already carries a Transport
// receipt owner. Binding a second one fails, which makes a function that binds
// its own owner after it created the Action fail at that point.
func (c *Closure) ReceiptBoundContext(t testing.TB) (context.Context, error) {
	t.Helper()
	ctx, err := domaintransport.WithReceiptOwner(context.Background(), c.NewTransport(t))
	if err != nil {
		t.Fatalf("bind the first receipt owner: %v", err)
	}
	// The error a second binding returns, so a test can tell that failure from any other.
	_, bindErr := domaintransport.WithReceiptOwner(ctx, c.NewTransport(t))
	if bindErr == nil {
		t.Fatal("a second receipt owner was accepted; the binding failure cannot be provoked")
	}
	return ctx, bindErr
}

// budgetTaskStore counts Task transactions. The first is the admission; every
// later one is a terminal write, whose budget is recorded and which fails with
// terminalErr when that is set.
type budgetTaskStore struct {
	domaintask.Store
	terminalErr error

	mu        sync.Mutex
	calls     int
	budgets   []time.Duration
	unbounded int
}

func (s *budgetTaskStore) TaskTransaction(ctx context.Context, taskID modulecore.TaskID, fn func(domaintask.Store) error) error {
	s.mu.Lock()
	s.calls++
	terminal := s.calls > 1
	if terminal {
		recordBudget(ctx, &s.budgets, &s.unbounded)
	}
	fail := terminal && s.terminalErr != nil
	s.mu.Unlock()
	if fail {
		return s.terminalErr
	}
	return s.Store.TaskTransaction(ctx, taskID, fn)
}

// budgetActionStore counts Action transactions. The first is CreateAction; every
// later one is a terminal write.
type budgetActionStore struct {
	domainaction.Store
	createErr    error
	onCreateFail func()
	onCreated    func()
	terminalErr  error

	mu        sync.Mutex
	calls     int
	budgets   []time.Duration
	unbounded int
}

func (s *budgetActionStore) Transaction(ctx context.Context, fn func(domainaction.Store) error) error {
	s.mu.Lock()
	s.calls++
	first := s.calls == 1
	if !first {
		recordBudget(ctx, &s.budgets, &s.unbounded)
	}
	var fail error
	switch {
	case first && s.createErr != nil:
		fail = s.createErr
	case !first && s.terminalErr != nil:
		fail = s.terminalErr
	}
	hook := s.onCreateFail
	s.mu.Unlock()
	if fail != nil {
		if first && hook != nil {
			hook()
		}
		return fail
	}
	err := s.Store.Transaction(ctx, fn)
	if err == nil && first && s.onCreated != nil {
		s.onCreated()
	}
	return err
}

func recordBudget(ctx context.Context, budgets *[]time.Duration, unbounded *int) {
	if deadline, ok := ctx.Deadline(); ok {
		*budgets = append(*budgets, time.Until(deadline))
		return
	}
	*unbounded++
}

// OnlyTaskAndRun returns the single Task and its single Run.
func (c *Closure) OnlyTaskAndRun(t testing.TB) (domaintask.Task, domaintask.Run) {
	t.Helper()
	tasks, err := c.Tasks.List(context.Background(), domaintask.Filter{Limit: 10000})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("Tasks=%d err=%v, want exactly the Task the function started", len(tasks), err)
	}
	runs, err := c.Tasks.ListRuns(context.Background(), domaintask.RunFilter{TaskID: tasks[0].TaskID})
	if err != nil || len(runs) != 1 {
		t.Fatalf("Runs=%d err=%v, want exactly one Run", len(runs), err)
	}
	return tasks[0], runs[0]
}

// AssertRunState fails unless the single Task and Run ended in the given states.
func (c *Closure) AssertRunState(t testing.TB, wantTask domaintask.Status, wantRun domaintask.RunStatus) {
	t.Helper()
	task, run := c.OnlyTaskAndRun(t)
	if task.Status != wantTask || run.Status != wantRun {
		t.Fatalf("Task/Run = %s/%s, want %s/%s (a Run that stays running holds its execution slot until the next restart)", task.Status, run.Status, wantTask, wantRun)
	}
}

// AssertSlotFree fails unless a new Run can be admitted, that is, unless no Run
// is holding the execution capacity. It starts a Run, so call it last.
func (c *Closure) AssertSlotFree(t testing.TB) {
	t.Helper()
	probe, probeRun, err := c.Tasks.CreateAndStartRun(context.Background(), domaintask.Task{
		Title:    "slot probe",
		Route:    domaintask.RouteGeneral,
		Assignee: "probe",
	}, domaintask.SharedRoleContext{})
	if errors.Is(err, taskmanager.ErrParallelLimit) {
		t.Fatalf("the execution slot is still held: %v", err)
	}
	if err != nil || probe.Status != domaintask.StatusRunning || probeRun.Status != domaintask.RunStatusRunning {
		t.Fatalf("slot probe task=%#v run=%#v err=%v", probe, probeRun, err)
	}
}

// AssertRunLeftRunning fails unless the single Task and Run are still running:
// the only state a refused terminal write may leave, because the caller was told.
func (c *Closure) AssertRunLeftRunning(t testing.TB) {
	t.Helper()
	task, run := c.OnlyTaskAndRun(t)
	if task.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning {
		t.Fatalf("Task/Run = %s/%s, want running because the terminal write was refused", task.Status, run.Status)
	}
}

// AssertActionLeftOpen fails unless exactly one Action exists and it, with its
// Attempt, is still open: the only state a refused terminal write may leave.
func (c *Closure) AssertActionLeftOpen(t testing.TB) {
	t.Helper()
	actions, err := c.Actions.ListActions(context.Background(), domainaction.Filter{})
	if err != nil || len(actions) != 1 {
		t.Fatalf("Actions=%d err=%v, want exactly one", len(actions), err)
	}
	attempts, err := c.Actions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: actions[0].ActionID})
	if err != nil || len(attempts) != 1 {
		t.Fatalf("Attempts=%d err=%v, want exactly one", len(attempts), err)
	}
	if actions[0].Status != domainaction.StatusOpen || attempts[0].Status != domainaction.AttemptStatusRunning {
		t.Fatalf("Action/Attempt = %s/%s, want open/running because the terminal write was refused", actions[0].Status, attempts[0].Status)
	}
}

// AssertNoAction fails if any Action was created.
func (c *Closure) AssertNoAction(t testing.TB) {
	t.Helper()
	actions, err := c.Actions.ListActions(context.Background(), domainaction.Filter{})
	if err != nil || len(actions) != 0 {
		t.Fatalf("Actions=%d err=%v, want none", len(actions), err)
	}
}

// AssertActionClosed fails unless exactly one Action exists, with one Attempt,
// both in the given terminal states.
func (c *Closure) AssertActionClosed(t testing.TB, wantAction domainaction.Status, wantAttempt domainaction.AttemptStatus) {
	t.Helper()
	actions, err := c.Actions.ListActions(context.Background(), domainaction.Filter{})
	if err != nil || len(actions) != 1 {
		t.Fatalf("Actions=%d err=%v, want exactly one", len(actions), err)
	}
	attempts, err := c.Actions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: actions[0].ActionID})
	if err != nil || len(attempts) != 1 {
		t.Fatalf("Attempts=%d err=%v, want exactly one", len(attempts), err)
	}
	if actions[0].Status != wantAction || attempts[0].Status != wantAttempt {
		t.Fatalf("Action/Attempt = %s/%s, want %s/%s", actions[0].Status, attempts[0].Status, wantAction, wantAttempt)
	}
}

// AssertTerminalWritesBounded fails unless there was at least one terminal write
// to the Task store (and to the Action store when wantActionWrites is true) and
// every one was given a finite budget within the owner's limit
// (taskmanager.StartedRunTerminalWriteTimeout, actionmanager.AttemptTerminalWriteTimeout). Call it before
// AssertSlotFree, whose probe would record an unbounded write.
func (c *Closure) AssertTerminalWritesBounded(t testing.TB, wantActionWrites bool) {
	t.Helper()
	assertBudgets(t, "Task", taskmanager.StartedRunTerminalWriteTimeout, &c.taskStore.mu, c.taskStore.budgets, c.taskStore.unbounded, true)
	assertBudgets(t, "Action", actionmanager.AttemptTerminalWriteTimeout, &c.actionStore.mu, c.actionStore.budgets, c.actionStore.unbounded, wantActionWrites)
}

func assertBudgets(t testing.TB, name string, limit time.Duration, mu *sync.Mutex, budgets []time.Duration, unbounded int, wantWrites bool) {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	if len(budgets) == 0 && unbounded == 0 {
		if wantWrites {
			t.Fatalf("no terminal write to the %s store was recorded", name)
		}
		return
	}
	if unbounded != 0 {
		t.Fatalf("%d terminal write(s) to the %s store had no deadline", unbounded, name)
	}
	for _, budget := range budgets {
		if budget > limit {
			t.Fatalf("%s terminal write budget = %s, want at most %s", name, budget, limit)
		}
		if budget < minimumTerminalBudget {
			t.Fatalf("%s terminal write budget = %s, want at least %s: three times the %s one write took on the production host", name, budget, minimumTerminalBudget, observedStoreWrite)
		}
	}
}

// ContainsText reports whether err's message contains the message of want. It
// matches an error that crossed a layer which wraps with %v.
func ContainsText(err, want error) bool {
	return err != nil && want != nil && strings.Contains(err.Error(), want.Error())
}
