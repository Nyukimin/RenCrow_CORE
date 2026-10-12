package taskmanagertest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// WorkFailure says how a function that wraps some work reports that the work
// failed.
type WorkFailure int

const (
	// WorkFailureReturnsInjected: the call returns the error the work ended with.
	WorkFailureReturnsInjected WorkFailure = iota
	// WorkFailureReturnsSome: the call returns an error, but not an injectable
	// one, because the function itself produces the failure (a patch whose diff
	// cannot be read).
	WorkFailureReturnsSome
	// WorkFailureIsRecorded: the outcome is data, not an error (a playback
	// receipt carries the failure it records); the call returns nil.
	WorkFailureIsRecorded
)

// StartedRunPath describes one function that starts a Run itself, for
// VerifyStartedRunClosure.
type StartedRunPath struct {
	// Start builds the function over the Closure's owners and returns one call of
	// it. workErr is what the wrapped work ends with; nil means it succeeds. The
	// call must run the whole lifecycle the function owns (for a TTS session:
	// start, push, and end) and return the first error it meets.
	Start func(t *testing.T, c *Closure, workErr error) func(ctx context.Context) error
	// BindsReceiptOwner is true when the function binds a Transport receipt owner
	// after it created the Action, so a context that already carries one fails it
	// at that point.
	BindsReceiptOwner bool
	// WorkFailure says how the call reports a failure of the wrapped work.
	WorkFailure WorkFailure
	// WorkFailureText is, for WorkFailureReturnsSome, a text the returned error
	// must contain, so a failure from the wrong place does not pass.
	WorkFailureText string
	// WorkIgnoresContext is true when the wrapped work does not take the
	// caller's context (a patch apply), so a cancellation cannot reach it and a
	// cancelled caller still sees the work end in failure.
	WorkIgnoresContext bool
}

// VerifyStartedRunClosure runs the scenarios every function that starts a Run
// must pass: whatever fails after the Run was issued, the Run and its Task end
// in a terminal state, the Action and Attempt (when they exist) do too, the
// execution slot is free again, and the terminal writes are bounded.
//
// Each failure is injected on its own, so a failing scenario names the stage.
func VerifyStartedRunClosure(t *testing.T, path StartedRunPath) {
	t.Helper()

	t.Run("work succeeds", func(t *testing.T) {
		c := NewClosure(t, ClosureOptions{})
		if err := path.Start(t, c, nil)(context.Background()); err != nil {
			t.Fatalf("call error = %v, want nil", err)
		}
		c.AssertActionClosed(t, domainaction.StatusSucceeded, domainaction.AttemptStatusSucceeded)
		assertClosed(t, c, domaintask.StatusSucceeded, domaintask.RunStatusSucceeded, true)
	})

	t.Run("work fails", func(t *testing.T) {
		c := NewClosure(t, ClosureOptions{})
		err := path.Start(t, c, ErrInjected)(context.Background())
		switch path.WorkFailure {
		case WorkFailureReturnsInjected:
			if !errors.Is(err, ErrInjected) {
				t.Fatalf("call error = %v, want the work's own error", err)
			}
		case WorkFailureReturnsSome:
			if err == nil || !strings.Contains(err.Error(), path.WorkFailureText) {
				t.Fatalf("call error = %v, want the failure of the work, containing %q", err, path.WorkFailureText)
			}
		case WorkFailureIsRecorded:
			if err != nil {
				t.Fatalf("call error = %v, want nil because the failure is recorded as data", err)
			}
		}
		c.AssertActionClosed(t, domainaction.StatusFailed, domainaction.AttemptStatusFailed)
		assertClosed(t, c, domaintask.StatusFailed, domaintask.RunStatusFailed, true)
	})

	// The caller's context ends after the Action was stored, while the work runs,
	// and the work ends with the cancellation. The Attempt, Action, Run, and Task
	// are still closed: their writes are detached from the context that ended.
	t.Run("caller context ends while the work runs", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		c := NewClosure(t, ClosureOptions{OnActionCreated: cancel})

		err := path.Start(t, c, context.Canceled)(ctx)

		if ctx.Err() == nil {
			t.Fatal("caller context did not end; the scenario did not exercise the detached close")
		}
		wantAction, wantAttempt := domainaction.StatusCancelled, domainaction.AttemptStatusCancelled
		wantTask, wantRun := domaintask.StatusCancelled, domaintask.RunStatusCancelled
		if path.WorkIgnoresContext {
			wantAction, wantAttempt = domainaction.StatusFailed, domainaction.AttemptStatusFailed
			wantTask, wantRun = domaintask.StatusFailed, domaintask.RunStatusFailed
		}
		if path.WorkFailure == WorkFailureReturnsInjected && !errors.Is(err, context.Canceled) {
			t.Fatalf("call error = %v, want the cancellation", err)
		}
		assertNotReportedAsOpen(t, err)
		c.AssertActionClosed(t, wantAction, wantAttempt)
		assertClosed(t, c, wantTask, wantRun, true)
	})

	t.Run("action creation fails", func(t *testing.T) {
		c := NewClosure(t, ClosureOptions{ActionCreateErr: ErrInjected})

		err := path.Start(t, c, nil)(context.Background())

		if !errors.Is(err, ErrInjected) {
			t.Fatalf("call error = %v, want the Action creation cause", err)
		}
		assertNotReportedAsOpen(t, err)
		c.AssertNoAction(t)
		assertClosed(t, c, domaintask.StatusFailed, domaintask.RunStatusFailed, false)
	})

	// The Task store refuses the write that closes the Run. The caller is told:
	// the cause and the refusal are both in the error, marked as not closed, and
	// the Run stays running (the only state a refused write may leave).
	t.Run("run close is refused after action creation fails", func(t *testing.T) {
		c := NewClosure(t, ClosureOptions{ActionCreateErr: ErrInjected, TaskTerminalErr: ErrTerminalRefused})

		err := path.Start(t, c, nil)(context.Background())

		if !errors.Is(err, ErrInjected) || !errors.Is(err, ErrTerminalRefused) || !errors.Is(err, taskmanager.ErrStartedRunNotClosed) {
			t.Fatalf("call error = %v, want the Action creation cause and the refused close, marked as not closed", err)
		}
		c.AssertNoAction(t)
		c.AssertRunLeftRunning(t)
	})

	// The work succeeded and every record was written except the closing of the
	// Run. A non-nil error is the only way the caller learns of it.
	t.Run("run close is refused after the work succeeded", func(t *testing.T) {
		c := NewClosure(t, ClosureOptions{TaskTerminalErr: ErrTerminalRefused})

		err := path.Start(t, c, nil)(context.Background())

		if !errors.Is(err, ErrTerminalRefused) || !errors.Is(err, taskmanager.ErrStartedRunNotClosed) {
			t.Fatalf("call error = %v, want the refused close, marked as not closed", err)
		}
		c.AssertActionClosed(t, domainaction.StatusSucceeded, domainaction.AttemptStatusSucceeded)
		c.AssertRunLeftRunning(t)
	})

	// The Action store refuses the write that closes the Attempt. The Run is not
	// closed over an open Action: both stay open, the error says so, and the
	// restart recovery settles them from the Action.
	t.Run("attempt close is refused after the work failed", func(t *testing.T) {
		c := NewClosure(t, ClosureOptions{ActionTerminalErr: ErrTerminalRefused})

		err := path.Start(t, c, ErrInjected)(context.Background())

		if !errors.Is(err, ErrTerminalRefused) || !errors.Is(err, actionmanager.ErrAttemptNotClosed) {
			t.Fatalf("call error = %v, want the refused close, marked as not closed", err)
		}
		if path.WorkFailure == WorkFailureReturnsInjected && !errors.Is(err, ErrInjected) {
			t.Fatalf("call error = %v, lost the error of the work", err)
		}
		c.AssertActionLeftOpen(t)
		c.AssertRunLeftRunning(t)
	})

	// The production failure: the caller's context ends while the Action store is
	// still working, so CreateAction fails with the caller's own error. Closing
	// the Run must not depend on that already-ended context.
	for _, tc := range []struct {
		name     string
		cause    error
		wantTask domaintask.Status
		wantRun  domaintask.RunStatus
	}{
		{"deadline", context.DeadlineExceeded, domaintask.StatusFailed, domaintask.RunStatusFailed},
		{"cancelled", context.Canceled, domaintask.StatusCancelled, domaintask.RunStatusCancelled},
	} {
		t.Run("caller context ends during action creation: "+tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			c := NewClosure(t, ClosureOptions{ActionCreateErr: tc.cause, OnActionCreateFail: cancel})

			err := path.Start(t, c, nil)(ctx)

			if !errors.Is(err, tc.cause) {
				t.Fatalf("call error = %v, want the %s cause", err, tc.name)
			}
			assertNotReportedAsOpen(t, err)
			if ctx.Err() == nil {
				t.Fatal("caller context did not end; the scenario did not exercise the detached close")
			}
			c.AssertNoAction(t)
			assertClosed(t, c, tc.wantTask, tc.wantRun, false)
		})
	}

	if path.BindsReceiptOwner {
		t.Run("binding fails after the action was created", func(t *testing.T) {
			c := NewClosure(t, ClosureOptions{})
			ctx, bindErr := c.ReceiptBoundContext(t)

			err := path.Start(t, c, nil)(ctx)

			if !ContainsText(err, bindErr) {
				t.Fatalf("call error = %v, want the binding failure %q", err, bindErr)
			}
			c.AssertActionClosed(t, domainaction.StatusFailed, domainaction.AttemptStatusFailed)
			assertClosed(t, c, domaintask.StatusFailed, domaintask.RunStatusFailed, true)
		})
	}
}

// assertNotReportedAsOpen fails if err claims a Run or Attempt was left open
// although the test is about a path that closed them.
func assertNotReportedAsOpen(t *testing.T, err error) {
	t.Helper()
	if errors.Is(err, taskmanager.ErrStartedRunNotClosed) || errors.Is(err, actionmanager.ErrAttemptNotClosed) {
		t.Fatalf("call error = %v, claims something was left open although it was closed", err)
	}
}

// assertClosed checks the Task and Run state, that every terminal write had a
// finite budget, and that the execution slot is free, in that order: the
// state is the evidence, and the probe of the slot is the last use of the owners.
func assertClosed(t *testing.T, c *Closure, wantTask domaintask.Status, wantRun domaintask.RunStatus, wantActionWrites bool) {
	t.Helper()
	c.AssertRunState(t, wantTask, wantRun)
	c.AssertTerminalWritesBounded(t, wantActionWrites)
	c.AssertSlotFree(t)
}
