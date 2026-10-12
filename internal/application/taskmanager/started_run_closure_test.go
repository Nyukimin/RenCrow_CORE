package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Failure knowledge: docs/調査/*Run開始後の早期失敗でRunを閉じない_FailureKnowledge.md.

// The classification is a table, held here once. The behavioral tests of each
// Run-starting function do not repeat it.
func TestRunOutcomeOfClassifiesHowTheWorkEnded(t *testing.T) {
	other := errors.New("some failure")
	cases := []struct {
		name        string
		err         error
		wantStatus  domaintask.Status
		wantSummary string
	}{
		{"success", nil, domaintask.StatusSucceeded, "done"},
		{"cancellation", context.Canceled, domaintask.StatusCancelled, context.Canceled.Error()},
		{"wrapped cancellation", fmt.Errorf("call: %w", context.Canceled), domaintask.StatusCancelled, "call: context canceled"},
		{"expired deadline", context.DeadlineExceeded, domaintask.StatusFailed, context.DeadlineExceeded.Error()},
		{"other error", other, domaintask.StatusFailed, other.Error()},
		{"cancellation together with a deadline", errors.Join(context.Canceled, context.DeadlineExceeded), domaintask.StatusCancelled, "context canceled\ncontext deadline exceeded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RunOutcomeOf(tc.err, "done")
			if got.Status != tc.wantStatus || got.Summary != tc.wantSummary {
				t.Fatalf("RunOutcomeOf(%v) = %+v, want %s %q", tc.err, got, tc.wantStatus, tc.wantSummary)
			}
		})
	}
}

// terminalWriteProbe wraps a Task store. The first transaction is the admission;
// each later one is a terminal write, which is recorded and, when err is set,
// refused.
type terminalWriteProbe struct {
	domaintask.Store
	err error

	mu        sync.Mutex
	calls     int
	budgets   []time.Duration
	unbounded int
}

func (s *terminalWriteProbe) TaskTransaction(ctx context.Context, taskID modulecore.TaskID, fn func(domaintask.Store) error) error {
	s.mu.Lock()
	s.calls++
	later := s.calls > 1
	if later {
		if deadline, ok := ctx.Deadline(); ok {
			s.budgets = append(s.budgets, time.Until(deadline))
		} else {
			s.unbounded++
		}
	}
	fail := later && s.err != nil
	s.mu.Unlock()
	if fail {
		return s.err
	}
	return s.Store.TaskTransaction(ctx, taskID, fn)
}

func startRunForClosure(t *testing.T, refuse error) (*Manager, *terminalWriteProbe, domaintask.Task, domaintask.Run) {
	t.Helper()
	base, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, base)
	probe := &terminalWriteProbe{Store: base, err: refuse}
	manager := New(probe, DefaultParallelLimits())
	task, run, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "close", Route: domaintask.RouteGeneral, Assignee: "shiro",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	return manager, probe, task, run
}

func TestCloseStartedRunClosesWithTheClassifiedOutcome(t *testing.T) {
	cases := []struct {
		name     string
		cause    error
		wantTask domaintask.Status
		wantRun  domaintask.RunStatus
	}{
		{"success", nil, domaintask.StatusSucceeded, domaintask.RunStatusSucceeded},
		{"cancellation", context.Canceled, domaintask.StatusCancelled, domaintask.RunStatusCancelled},
		{"deadline", context.DeadlineExceeded, domaintask.StatusFailed, domaintask.RunStatusFailed},
		{"other error", errors.New("boom"), domaintask.StatusFailed, domaintask.RunStatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manager, _, task, run := startRunForClosure(t, nil)

			err := manager.CloseStartedRun(context.Background(), task.TaskID, run.RunID, "shiro", RunOutcomeOf(tc.cause, "done"), tc.cause)

			if err != tc.cause {
				t.Fatalf("CloseStartedRun = %v, want the cause itself (%v)", err, tc.cause)
			}
			gotTask, err := manager.Get(context.Background(), task.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			gotRun, err := manager.GetRun(context.Background(), run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if gotTask.Status != tc.wantTask || gotRun.Status != tc.wantRun {
				t.Fatalf("Task/Run = %s/%s, want %s/%s", gotTask.Status, gotRun.Status, tc.wantTask, tc.wantRun)
			}
		})
	}
}

// The caller's context has ended, as when a cancelled or expired tick fails the
// work: the write must not inherit that, and it must have a finite budget.
func TestCloseStartedRunIsDetachedFromTheCallerAndBounded(t *testing.T) {
	manager, probe, task, run := startRunForClosure(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := manager.CloseStartedRun(ctx, task.TaskID, run.RunID, "shiro", RunOutcomeOf(context.Canceled, ""), context.Canceled)

	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrStartedRunNotClosed) {
		t.Fatalf("CloseStartedRun = %v, want the cancellation alone", err)
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if len(probe.budgets) != 1 || probe.unbounded != 0 {
		t.Fatalf("terminal writes: %d bounded, %d unbounded, want exactly one bounded", len(probe.budgets), probe.unbounded)
	}
	if budget := probe.budgets[0]; budget <= 0 || budget > StartedRunTerminalWriteTimeout {
		t.Fatalf("terminal write budget = %s, want within (0, %s]", budget, StartedRunTerminalWriteTimeout)
	}
	if StartedRunTerminalWriteTimeout != 60*time.Second {
		t.Fatalf("StartedRunTerminalWriteTimeout = %s, want the 60s of the other terminal writes", StartedRunTerminalWriteTimeout)
	}
}

func TestCloseStartedRunReturnsAFailedWriteWithItsCause(t *testing.T) {
	refused := errors.New("terminal write refused")
	for _, tc := range []struct {
		name  string
		cause error
	}{
		{"with a cause", context.Canceled},
		{"without a cause", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, _, task, run := startRunForClosure(t, refused)

			err := manager.CloseStartedRun(context.Background(), task.TaskID, run.RunID, "shiro", RunOutcomeOf(tc.cause, ""), tc.cause)

			if !errors.Is(err, refused) || !errors.Is(err, ErrStartedRunNotClosed) {
				t.Fatalf("CloseStartedRun = %v, want the refused write marked as not closed", err)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("CloseStartedRun = %v, lost the cause %v", err, tc.cause)
			}
			got, getErr := manager.GetRun(context.Background(), run.RunID)
			if getErr != nil || got.Status != domaintask.RunStatusRunning {
				t.Fatalf("Run = %#v err=%v, want it still running because the write was refused", got, getErr)
			}
		})
	}
}

func TestCloseStartedRunRefusesAnOutcomeThatIsNotTerminal(t *testing.T) {
	manager, probe, task, run := startRunForClosure(t, nil)

	err := manager.CloseStartedRun(context.Background(), task.TaskID, run.RunID, "shiro", RunOutcome{Status: domaintask.StatusWaiting}, nil)

	if !errors.Is(err, ErrStartedRunNotClosed) {
		t.Fatalf("CloseStartedRun = %v, want the refusal marked as not closed", err)
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.calls != 1 {
		t.Fatalf("Task transactions = %d, want only the admission: nothing is written for a non-terminal outcome", probe.calls)
	}
}

func TestAbortStartedRunClosesARunWhoseWorkNeverBegan(t *testing.T) {
	cases := []struct {
		name     string
		cause    error
		wantTask domaintask.Status
		wantRun  domaintask.RunStatus
	}{
		{"error", errors.New("create action refused"), domaintask.StatusFailed, domaintask.RunStatusFailed},
		{"deadline", context.DeadlineExceeded, domaintask.StatusFailed, domaintask.RunStatusFailed},
		{"cancellation", context.Canceled, domaintask.StatusCancelled, domaintask.RunStatusCancelled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manager, _, task, run := startRunForClosure(t, nil)

			err := manager.AbortStartedRun(context.Background(), task.TaskID, run.RunID, "shiro", tc.cause)

			if err != tc.cause {
				t.Fatalf("AbortStartedRun = %v, want the cause itself (%v)", err, tc.cause)
			}
			gotTask, getErr := manager.Get(context.Background(), task.TaskID)
			gotRun, runErr := manager.GetRun(context.Background(), run.RunID)
			if getErr != nil || runErr != nil {
				t.Fatalf("get task err=%v run err=%v", getErr, runErr)
			}
			if gotTask.Status != tc.wantTask || gotRun.Status != tc.wantRun {
				t.Fatalf("Task/Run = %s/%s, want %s/%s", gotTask.Status, gotRun.Status, tc.wantTask, tc.wantRun)
			}
		})
	}
}

func TestAbortStartedRunReturnsAFailedWriteWithItsCause(t *testing.T) {
	refused := errors.New("terminal write refused")
	cause := errors.New("create action refused")
	manager, _, task, run := startRunForClosure(t, refused)

	err := manager.AbortStartedRun(context.Background(), task.TaskID, run.RunID, "shiro", cause)

	if !errors.Is(err, cause) || !errors.Is(err, refused) || !errors.Is(err, ErrStartedRunNotClosed) {
		t.Fatalf("AbortStartedRun = %v, want the cause and the refused write, marked as not closed", err)
	}
}

// Aborting without a cause would record a success for work that never began, so
// it is refused as a failure instead.
func TestAbortStartedRunWithoutACauseIsAFailure(t *testing.T) {
	manager, _, task, run := startRunForClosure(t, nil)

	err := manager.AbortStartedRun(context.Background(), task.TaskID, run.RunID, "shiro", nil)

	if err == nil {
		t.Fatal("AbortStartedRun without a cause returned nil")
	}
	got, getErr := manager.Get(context.Background(), task.TaskID)
	if getErr != nil || got.Status != domaintask.StatusFailed {
		t.Fatalf("Task = %#v err=%v, want failed", got, getErr)
	}
}
