package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	memorypromotionapp "github.com/Nyukimin/RenCrow_CORE/internal/application/memorypromotion"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type memoryPromotionActionRunnerStub struct {
	called        bool
	identityBound bool
	actionBound   bool
	// err, when set, is what the inner run ends with.
	err error
	// idle makes the claim probe answer "nothing to claim". The zero value is the
	// state every lifecycle test below assumes: work is waiting.
	idle bool
	// probeErr, when set, is what the claim probe fails with.
	probeErr error
	probes   int
	// claimsNothing makes RunOne claim nothing (Processed=false) although the
	// probe said there was work: the false positive the probe is allowed to have.
	claimsNothing bool
}

func (s *memoryPromotionActionRunnerStub) HasClaimable(context.Context) (bool, error) {
	s.probes++
	if s.probeErr != nil {
		return false, s.probeErr
	}
	return !s.idle, nil
}

func (s *memoryPromotionActionRunnerStub) RunOne(ctx context.Context) (memorypromotionapp.RunResult, error) {
	s.called = true
	_, identityErr := domainexecution.IdentityFromContext(ctx)
	_, _, s.actionBound = domainexecution.BoundActionAttemptFromContext(ctx)
	s.identityBound = identityErr == nil
	if s.err != nil {
		return memorypromotionapp.RunResult{}, s.err
	}
	if s.claimsNothing {
		return memorypromotionapp.RunResult{}, nil
	}
	return memorypromotionapp.RunResult{Processed: true, MessageCount: 1, CandidateCount: 1}, nil
}

func TestActionMemoryPromotionRunnerCompletesCanonicalLifecycle(t *testing.T) {
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	tasks := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	actions := actionmanager.New(actionStore)
	inner := &memoryPromotionActionRunnerStub{}
	runner, err := newActionMemoryPromotionRunner(inner, actions, tasks, "midori")
	if err != nil {
		t.Fatal(err)
	}

	result, err := runner.RunOne(context.Background())
	if err != nil || !result.Processed || !inner.identityBound || !inner.actionBound {
		t.Fatalf("RunOne result=%#v identity_bound=%v action_bound=%v err=%v", result, inner.identityBound, inner.actionBound, err)
	}
	storedActions, err := actions.ListActions(context.Background(), domainaction.Filter{})
	if err != nil || len(storedActions) != 1 {
		t.Fatalf("actions=%#v err=%v", storedActions, err)
	}
	action := storedActions[0]
	attempts, err := actions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: action.ActionID})
	if err != nil {
		t.Fatal(err)
	}
	task, err := tasks.Get(context.Background(), action.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := tasks.GetRun(context.Background(), action.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if action.Kind != domainaction.KindMemoryPromotion || action.Status != domainaction.StatusSucceeded || len(attempts) != 1 || attempts[0].Status != domainaction.AttemptStatusSucceeded {
		t.Fatalf("Memory Promotion Action lifecycle action=%#v attempts=%#v", action, attempts)
	}
	if task.Status != domaintask.StatusSucceeded || run.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("Memory Promotion Task/Run lifecycle task=%#v run=%#v", task, run)
	}
}

// errMemoryPromotionInjected is the cause every fault below injects. Tests match
// it with errors.Is so a swallowed or replaced cause is visible.
var errMemoryPromotionInjected = errors.New("injected Memory Promotion fault")

// memoryPromotionTaskFaultStore lets the first allowed Task transactions pass
// (CreateAndStartRun is exactly one) and fails the rest when err is set. It
// models a Task store that cannot write the terminal state. It also records
// the budget each later transaction was given, so a test can see that terminal
// writes are bounded.
type memoryPromotionTaskFaultStore struct {
	domaintask.Store
	mu        sync.Mutex
	allowed   int
	calls     int
	err       error
	budgets   []time.Duration
	unbounded int
}

func (s *memoryPromotionTaskFaultStore) TaskTransaction(ctx context.Context, taskID modulecore.TaskID, fn func(domaintask.Store) error) error {
	s.mu.Lock()
	s.calls++
	later := s.calls > s.allowed
	if later {
		if deadline, ok := ctx.Deadline(); ok {
			s.budgets = append(s.budgets, time.Until(deadline))
		} else {
			s.unbounded++
		}
	}
	fail := s.err != nil && later
	s.mu.Unlock()
	if fail {
		return s.err
	}
	return s.Store.TaskTransaction(ctx, taskID, fn)
}

// memoryPromotionActionFaultStore refuses the Action creation transaction. The
// onFail hook runs first so a test can expire the caller context at the moment
// the store gives up, as a deadline does in production.
type memoryPromotionActionFaultStore struct {
	domainaction.Store
	err    error
	onFail func()
}

func (s *memoryPromotionActionFaultStore) Transaction(context.Context, func(domainaction.Store) error) error {
	if s.onFail != nil {
		s.onFail()
	}
	return s.err
}

type memoryPromotionFixtureOptions struct {
	// terminalErr fails every Task transaction after CreateAndStartRun.
	terminalErr error
	// actionFault, when set, makes Action creation fail with this error.
	actionFault *memoryPromotionActionFaultStore
}

type memoryPromotionFixture struct {
	tasks     *taskmanager.Manager
	taskFault *memoryPromotionTaskFaultStore
	actions   *actionmanager.Manager
	inner     *memoryPromotionActionRunnerStub
	runner    memoryPromotionRunner
}

func newMemoryPromotionFixture(t *testing.T, options memoryPromotionFixtureOptions) *memoryPromotionFixture {
	t.Helper()
	baseTasks, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := baseTasks.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	baseActions, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	taskFault := &memoryPromotionTaskFaultStore{Store: baseTasks, allowed: 1, err: options.terminalErr}
	var actionStore domainaction.Store = baseActions
	if options.actionFault != nil {
		options.actionFault.Store = baseActions
		actionStore = options.actionFault
	}
	fixture := &memoryPromotionFixture{
		tasks:     taskmanager.New(taskFault, taskmanager.DefaultParallelLimits()),
		taskFault: taskFault,
		actions:   actionmanager.New(actionStore),
		inner:     &memoryPromotionActionRunnerStub{},
	}
	fixture.runner, err = newActionMemoryPromotionRunner(fixture.inner, fixture.actions, fixture.tasks, "midori")
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

// onlyTaskAndRun returns the single Memory Promotion Task and its Run.
func (f *memoryPromotionFixture) onlyTaskAndRun(t *testing.T) (domaintask.Task, domaintask.Run) {
	t.Helper()
	tasks, err := f.tasks.List(context.Background(), domaintask.Filter{})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%d err=%v, want exactly the Memory Promotion Task", len(tasks), err)
	}
	runs, err := f.tasks.ListRuns(context.Background(), domaintask.RunFilter{TaskID: tasks[0].TaskID})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%d err=%v, want exactly one Run", len(runs), err)
	}
	return tasks[0], runs[0]
}

// assertEarlyFailureClosed checks what an early failure must leave behind: the
// inner runner never ran, no Action exists, the Task and Run are terminal, and
// the single long-research execution slot is free again.
func (f *memoryPromotionFixture) assertEarlyFailureClosed(t *testing.T, wantTask domaintask.Status, wantRun domaintask.RunStatus) {
	t.Helper()
	if f.inner.called {
		t.Fatal("inner runner ran although the execution context could not be prepared")
	}
	actions, err := f.actions.ListActions(context.Background(), domainaction.Filter{})
	if err != nil || len(actions) != 0 {
		t.Fatalf("actions=%d err=%v, want none", len(actions), err)
	}
	task, run := f.onlyTaskAndRun(t)
	if task.Status != wantTask || run.Status != wantRun {
		t.Fatalf("Task/Run after early failure = %s/%s, want %s/%s", task.Status, run.Status, wantTask, wantRun)
	}
	next, nextRun, err := f.tasks.CreateAndStartRun(context.Background(), domaintask.Task{
		Title:    "next Memory Promotion",
		Route:    domaintask.RouteResearch,
		OwnerID:  "midori",
		Assignee: "midori",
	}, domaintask.SharedRoleContext{})
	if errors.Is(err, taskmanager.ErrParallelLimit) {
		t.Fatalf("execution slot is still held after the early failure: %v", err)
	}
	if err != nil || next.Status != domaintask.StatusRunning || nextRun.Status != domaintask.RunStatusRunning {
		t.Fatalf("next run task=%#v run=%#v err=%v", next, nextRun, err)
	}
}

// assertTerminalWritesBounded checks that every Task write after the start was
// given a finite budget of at most taskmanager.StartedRunTerminalWriteTimeout. Call it
// before any other use of the manager, which would record unbounded probes.
func (f *memoryPromotionFixture) assertTerminalWritesBounded(t *testing.T) {
	t.Helper()
	f.taskFault.mu.Lock()
	defer f.taskFault.mu.Unlock()
	if len(f.taskFault.budgets) == 0 {
		t.Fatal("no terminal write was recorded")
	}
	if f.taskFault.unbounded != 0 {
		t.Fatalf("%d terminal write(s) had no deadline", f.taskFault.unbounded)
	}
	for _, budget := range f.taskFault.budgets {
		if budget <= 0 || budget > taskmanager.StartedRunTerminalWriteTimeout {
			t.Fatalf("terminal write budget = %s, want within (0, %s]", budget, taskmanager.StartedRunTerminalWriteTimeout)
		}
	}
}

// assertEarlyFailureLeftRunning checks the only state a failed terminal write
// may leave: the Task stays running (the caller was told, so recovery can act).
func (f *memoryPromotionFixture) assertEarlyFailureLeftRunning(t *testing.T) {
	t.Helper()
	if f.inner.called {
		t.Fatal("inner runner ran although the execution context could not be prepared")
	}
	task, run := f.onlyTaskAndRun(t)
	if task.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning {
		t.Fatalf("Task/Run = %s/%s, want running because the terminal write was refused", task.Status, run.Status)
	}
}

// conflictingIdentityContext carries another execution identity, which makes
// domainexecution.WithIdentity refuse the Task and Run this runner just issued.
func conflictingIdentityContext(t *testing.T) context.Context {
	t.Helper()
	ctx, err := domainexecution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestActionMemoryPromotionRunnerClosesRunWhenIdentityBindingFails(t *testing.T) {
	fixture := newMemoryPromotionFixture(t, memoryPromotionFixtureOptions{})

	_, err := fixture.runner.RunOne(conflictingIdentityContext(t))

	if err == nil {
		t.Fatal("RunOne error = nil, want the identity binding failure")
	}
	fixture.assertEarlyFailureClosed(t, domaintask.StatusFailed, domaintask.RunStatusFailed)
}

func TestActionMemoryPromotionRunnerClosesRunWhenActionCreationFails(t *testing.T) {
	fixture := newMemoryPromotionFixture(t, memoryPromotionFixtureOptions{
		actionFault: &memoryPromotionActionFaultStore{err: errMemoryPromotionInjected},
	})

	_, err := fixture.runner.RunOne(context.Background())

	if !errors.Is(err, errMemoryPromotionInjected) {
		t.Fatalf("RunOne error = %v, want the Action creation cause", err)
	}
	if errors.Is(err, taskmanager.ErrStartedRunNotClosed) {
		t.Fatalf("RunOne error = %v, claims the run was left open although it was closed", err)
	}
	fixture.assertEarlyFailureClosed(t, domaintask.StatusFailed, domaintask.RunStatusFailed)
}

// The production failure: the caller's context ends while the Action store is
// still working, so CreateAction fails with the caller's own error. In
// production the caller is cancelled when a user-facing LLM call takes the idle
// lease (llmBusyTracker.BeginExternal), or it reaches the tick timeout. Closing
// the Run must not depend on that already-ended context.
func TestActionMemoryPromotionRunnerClosesRunWhenCallerContextEndsDuringActionCreation(t *testing.T) {
	cases := []struct {
		name     string
		cause    error
		wantTask domaintask.Status
		wantRun  domaintask.RunStatus
	}{
		{"deadline", context.DeadlineExceeded, domaintask.StatusFailed, domaintask.RunStatusFailed},
		{"cancelled", context.Canceled, domaintask.StatusCancelled, domaintask.RunStatusCancelled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			fixture := newMemoryPromotionFixture(t, memoryPromotionFixtureOptions{
				actionFault: &memoryPromotionActionFaultStore{err: tc.cause, onFail: cancel},
			})

			_, err := fixture.runner.RunOne(ctx)

			if !errors.Is(err, tc.cause) {
				t.Fatalf("RunOne error = %v, want the %s cause", err, tc.name)
			}
			if ctx.Err() == nil {
				t.Fatal("caller context did not end; the test did not exercise the detached close")
			}
			fixture.assertTerminalWritesBounded(t)
			fixture.assertEarlyFailureClosed(t, tc.wantTask, tc.wantRun)
		})
	}
}

func TestActionMemoryPromotionRunnerReturnsCloseFailureAfterIdentityBindingFails(t *testing.T) {
	closeErr := errors.New("terminal write refused")
	fixture := newMemoryPromotionFixture(t, memoryPromotionFixtureOptions{terminalErr: closeErr})

	_, err := fixture.runner.RunOne(conflictingIdentityContext(t))

	if err == nil || !errors.Is(err, closeErr) || !errors.Is(err, taskmanager.ErrStartedRunNotClosed) {
		t.Fatalf("RunOne error = %v, want the failed close reported and marked as not closed", err)
	}
	fixture.assertEarlyFailureLeftRunning(t)
}

func TestActionMemoryPromotionRunnerReturnsBothCausesWhenActionCreationAndCloseFail(t *testing.T) {
	closeErr := errors.New("terminal write refused")
	fixture := newMemoryPromotionFixture(t, memoryPromotionFixtureOptions{
		terminalErr: closeErr,
		actionFault: &memoryPromotionActionFaultStore{err: errMemoryPromotionInjected},
	})

	_, err := fixture.runner.RunOne(context.Background())

	if !errors.Is(err, errMemoryPromotionInjected) || !errors.Is(err, closeErr) || !errors.Is(err, taskmanager.ErrStartedRunNotClosed) {
		t.Fatalf("RunOne error = %v, want the Action creation cause and the close failure, marked as not closed", err)
	}
	fixture.assertEarlyFailureLeftRunning(t)
}

// The run is cancelled while it works (a user-facing LLM call took the idle
// lease), and then the Task store refuses the final write. The Task is still
// running; the error must say so even though it is also a cancellation.
func TestActionMemoryPromotionRunnerMarksRunNotClosedWhenFinalWriteFailsAfterCancellation(t *testing.T) {
	closeErr := errors.New("terminal write refused")
	fixture := newMemoryPromotionFixture(t, memoryPromotionFixtureOptions{terminalErr: closeErr})
	fixture.inner.err = context.Canceled

	_, err := fixture.runner.RunOne(context.Background())

	if !errors.Is(err, context.Canceled) || !errors.Is(err, closeErr) || !errors.Is(err, taskmanager.ErrStartedRunNotClosed) {
		t.Fatalf("RunOne error = %v, want the cancellation and the failed final write, marked as not closed", err)
	}
	task, run := fixture.onlyTaskAndRun(t)
	if task.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning {
		t.Fatalf("Task/Run = %s/%s, want running because the final write was refused", task.Status, run.Status)
	}
}

// assertNothingCreated checks that a RunOne that found nothing to claim left no
// trace in either owner: no Task transaction ran, and no Task, Run or Action exists.
func (f *memoryPromotionFixture) assertNothingCreated(t *testing.T) {
	t.Helper()
	f.taskFault.mu.Lock()
	calls := f.taskFault.calls
	f.taskFault.mu.Unlock()
	if calls != 0 {
		t.Fatalf("%d Task transaction(s) ran although nothing was claimable", calls)
	}
	tasks, err := f.tasks.List(context.Background(), domaintask.Filter{})
	if err != nil || len(tasks) != 0 {
		t.Fatalf("tasks=%d err=%v, want none", len(tasks), err)
	}
	actions, err := f.actions.ListActions(context.Background(), domainaction.Filter{})
	if err != nil || len(actions) != 0 {
		t.Fatalf("actions=%d err=%v, want none", len(actions), err)
	}
}

func TestActionMemoryPromotionRunnerCreatesNothingWhenNothingIsClaimable(t *testing.T) {
	fixture := newMemoryPromotionFixture(t, memoryPromotionFixtureOptions{})
	fixture.inner.idle = true

	result, err := fixture.runner.RunOne(context.Background())

	if err != nil || result.Processed {
		t.Fatalf("RunOne result=%#v err=%v, want an unprocessed run without error", result, err)
	}
	if fixture.inner.called {
		t.Fatal("the inner runner ran although the probe found nothing to claim")
	}
	fixture.assertNothingCreated(t)
}

func TestActionMemoryPromotionRunnerKeepsCreatingTheLifecycleWhenAJobIsClaimable(t *testing.T) {
	fixture := newMemoryPromotionFixture(t, memoryPromotionFixtureOptions{})

	result, err := fixture.runner.RunOne(context.Background())

	if err != nil || !result.Processed || !fixture.inner.identityBound || !fixture.inner.actionBound {
		t.Fatalf("RunOne result=%#v identity_bound=%v action_bound=%v err=%v", result, fixture.inner.identityBound, fixture.inner.actionBound, err)
	}
	if fixture.inner.probes != 1 {
		t.Fatalf("probe ran %d times, want once per RunOne", fixture.inner.probes)
	}
	task, run := fixture.onlyTaskAndRun(t)
	if task.Status != domaintask.StatusSucceeded || run.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("Task/Run = %s/%s, want succeeded", task.Status, run.Status)
	}
}

// The probe may say "work" when the claim then finds none. That costs the old
// behaviour, nothing worse: the Task, Run, Action and Attempt are created and then
// closed by the shared terminal path, and the single long-research slot is free.
func TestActionMemoryPromotionRunnerClosesEverythingWhenTheProbeWasAFalsePositive(t *testing.T) {
	fixture := newMemoryPromotionFixture(t, memoryPromotionFixtureOptions{})
	fixture.inner.claimsNothing = true

	result, err := fixture.runner.RunOne(context.Background())

	if err != nil || result.Processed {
		t.Fatalf("RunOne result=%#v err=%v, want an unprocessed run without error", result, err)
	}
	if !fixture.inner.called {
		t.Fatal("the inner runner did not run although the probe found work")
	}
	task, run := fixture.onlyTaskAndRun(t)
	if task.Status != domaintask.StatusSucceeded || run.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("Task/Run = %s/%s, want succeeded", task.Status, run.Status)
	}
	actions, err := fixture.actions.ListActions(context.Background(), domainaction.Filter{})
	if err != nil || len(actions) != 1 || actions[0].Status != domainaction.StatusSucceeded {
		t.Fatalf("actions=%#v err=%v, want one succeeded Action", actions, err)
	}
	attempts, err := fixture.actions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: actions[0].ActionID})
	if err != nil || len(attempts) != 1 || attempts[0].Status != domainaction.AttemptStatusSucceeded {
		t.Fatalf("attempts=%#v err=%v, want one succeeded Attempt", attempts, err)
	}
	next, nextRun, err := fixture.tasks.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "next Memory Promotion", Route: domaintask.RouteResearch, OwnerID: "midori", Assignee: "midori",
	}, domaintask.SharedRoleContext{})
	if err != nil || next.Status != domaintask.StatusRunning || nextRun.Status != domaintask.RunStatusRunning {
		t.Fatalf("the execution slot is not free after the false positive: task=%#v run=%#v err=%v", next, nextRun, err)
	}
}

func TestActionMemoryPromotionRunnerReturnsTheProbeErrorWithoutCreatingAnything(t *testing.T) {
	probeErr := errors.New("probe failed")
	fixture := newMemoryPromotionFixture(t, memoryPromotionFixtureOptions{})
	fixture.inner.probeErr = probeErr

	result, err := fixture.runner.RunOne(context.Background())

	if !errors.Is(err, probeErr) || result.Processed {
		t.Fatalf("RunOne result=%#v err=%v, want the probe error", result, err)
	}
	if memoryPromotionRunLeftOpen(err) {
		t.Fatalf("RunOne error = %v, claims a run was left open although none was created", err)
	}
	if fixture.inner.called {
		t.Fatal("the inner runner ran although the probe failed")
	}
	fixture.assertNothingCreated(t)
}

// A cancellation reaching the probe must stay a cancellation: the worker does not
// report it as a job failure.
func TestActionMemoryPromotionRunnerKeepsTheCancellationFromTheProbe(t *testing.T) {
	fixture := newMemoryPromotionFixture(t, memoryPromotionFixtureOptions{})
	fixture.inner.probeErr = context.Canceled

	_, err := fixture.runner.RunOne(context.Background())

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOne error = %v, want context.Canceled preserved", err)
	}
	fixture.assertNothingCreated(t)
}

// statsHarness drives memoryPromotionRunStats with a fake clock and captures its lines.
type statsHarness struct {
	stats *memoryPromotionRunStats
	now   time.Time
	lines []string
}

func newStatsHarness() *statsHarness {
	h := &statsHarness{now: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)}
	h.stats = newMemoryPromotionRunStats(
		func() time.Time { return h.now },
		func(format string, args ...any) { h.lines = append(h.lines, fmt.Sprintf(format, args...)) },
	)
	return h
}

func TestMemoryPromotionRunStatsWritesNothingBeforeTheIntervalEnds(t *testing.T) {
	h := newStatsHarness()
	for i := 0; i < 500; i++ {
		h.stats.idle()
		h.now = h.now.Add(time.Second)
	}
	if len(h.lines) != 0 {
		t.Fatalf("lines before the interval = %q, want none", h.lines)
	}
}

func TestMemoryPromotionRunStatsWritesOneSummaryPerIntervalAndResets(t *testing.T) {
	h := newStatsHarness()
	for i := 0; i < 3; i++ {
		h.stats.idle()
	}
	h.stats.worked(true, nil)
	h.stats.worked(false, nil)
	h.stats.probeFailed()
	h.now = h.now.Add(memoryPromotionRunSummaryInterval)

	h.stats.idle()

	want := "Memory ProfilePromotion run summary window=10m0s idle=4 started=2 refused=0 processed=1 probe_errors=1"
	if len(h.lines) != 1 || h.lines[0] != want {
		t.Fatalf("lines = %q, want exactly %q", h.lines, want)
	}

	h.stats.idle()
	if len(h.lines) != 1 {
		t.Fatalf("lines after more runs in the same window = %q, want still one", h.lines)
	}
	h.now = h.now.Add(memoryPromotionRunSummaryInterval)
	h.stats.idle()
	wantNext := "Memory ProfilePromotion run summary window=10m0s idle=2 started=0 refused=0 processed=0 probe_errors=0"
	if len(h.lines) != 2 || h.lines[1] != wantNext {
		t.Fatalf("second summary = %q, want %q (counts restart each window)", h.lines, wantNext)
	}
}

func TestMemoryPromotionRunStatsNeverLogsTaskOrRecordIdentifiers(t *testing.T) {
	h := newStatsHarness()
	h.stats.worked(true, nil)
	h.now = h.now.Add(memoryPromotionRunSummaryInterval)
	h.stats.idle()
	if len(h.lines) != 1 || strings.Contains(h.lines[0], "task_") || strings.Contains(h.lines[0], "run_") {
		t.Fatalf("summary line = %q, want counts only", h.lines)
	}
}

// Through the wrapper: idle runs are counted, one summary line per interval, and
// no line per run.
func TestActionMemoryPromotionRunnerCountsIdleRunsInOneSummaryPerInterval(t *testing.T) {
	fixture := newMemoryPromotionFixture(t, memoryPromotionFixtureOptions{})
	fixture.inner.idle = true
	h := newStatsHarness()
	fixture.runner.(*actionMemoryPromotionRunner).stats = h.stats

	for i := 0; i < 6; i++ {
		if _, err := fixture.runner.RunOne(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.lines) != 0 {
		t.Fatalf("lines for six idle runs = %q, want none", h.lines)
	}
	h.now = h.now.Add(memoryPromotionRunSummaryInterval)
	if _, err := fixture.runner.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := "Memory ProfilePromotion run summary window=10m0s idle=7 started=0 refused=0 processed=0 probe_errors=0"
	if len(h.lines) != 1 || h.lines[0] != want {
		t.Fatalf("lines = %q, want exactly %q", h.lines, want)
	}
	fixture.assertNothingCreated(t)
}

func TestActionMemoryPromotionRunnerCountsStartedAndFailedProbeRuns(t *testing.T) {
	fixture := newMemoryPromotionFixture(t, memoryPromotionFixtureOptions{})
	h := newStatsHarness()
	fixture.runner.(*actionMemoryPromotionRunner).stats = h.stats

	if _, err := fixture.runner.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.inner.probeErr = errors.New("probe failed")
	if _, err := fixture.runner.RunOne(context.Background()); err == nil {
		t.Fatal("RunOne error = nil, want the probe error")
	}
	fixture.inner.probeErr = nil
	fixture.inner.idle = true
	h.now = h.now.Add(memoryPromotionRunSummaryInterval)
	if _, err := fixture.runner.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := "Memory ProfilePromotion run summary window=10m0s idle=1 started=1 refused=0 processed=1 probe_errors=1"
	if len(h.lines) != 1 || h.lines[0] != want {
		t.Fatalf("lines = %q, want exactly %q", h.lines, want)
	}
}

// A refusal for lack of execution capacity opened no subject: it is counted as
// refused, not as started.
func TestMemoryPromotionRunStatsCountsACapacityRefusalSeparately(t *testing.T) {
	h := newStatsHarness()
	h.stats.worked(false, fmt.Errorf("start run: %w", taskmanager.ErrParallelLimit))
	h.stats.worked(true, nil)
	h.now = h.now.Add(memoryPromotionRunSummaryInterval)
	h.stats.idle()

	want := "Memory ProfilePromotion run summary window=10m0s idle=1 started=1 refused=1 processed=1 probe_errors=0"
	if len(h.lines) != 1 || h.lines[0] != want {
		t.Fatalf("lines = %q, want exactly %q", h.lines, want)
	}
}

// Through the wrapper: a run refused for lack of execution capacity counts as
// refused, and the refusal still reaches the worker as ErrParallelLimit so its
// capacity backoff keeps working.
func TestActionMemoryPromotionRunnerCountsACapacityRefusalAsRefused(t *testing.T) {
	saturated := taskmanagertest.NewSaturated(t)
	inner := &memoryPromotionActionRunnerStub{}
	runner, err := newActionMemoryPromotionRunner(inner, newCapacityTestActionOwner(t), saturated.Manager, "midori")
	if err != nil {
		t.Fatal(err)
	}
	h := newStatsHarness()
	runner.(*actionMemoryPromotionRunner).stats = h.stats

	if _, err := runner.RunOne(context.Background()); !errors.Is(err, taskmanager.ErrParallelLimit) {
		t.Fatalf("RunOne error=%v, want ErrParallelLimit", err)
	}
	inner.idle = true
	h.now = h.now.Add(memoryPromotionRunSummaryInterval)
	if _, err := runner.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := "Memory ProfilePromotion run summary window=10m0s idle=1 started=0 refused=1 processed=0 probe_errors=0"
	if len(h.lines) != 1 || h.lines[0] != want {
		t.Fatalf("lines = %q, want exactly %q", h.lines, want)
	}
}
