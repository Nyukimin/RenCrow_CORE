package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Failure knowledge: docs/調査/*Run開始後の早期失敗でRunを閉じない_FailureKnowledge.md.
//
// A Run holds an execution slot until it is closed. A function that starts a
// Run (CreateAndStartRun) owns the obligation to close it on every path that
// ends after the start, including the paths that fail while the function is
// still preparing its work. CloseStartedRun is the one way such a function
// closes a Run it started. It holds the rule that was fixed for Memory
// Promotion and is shared by every caller:
//
//  1. The write is detached from the caller's cancellation, because a
//     cancelled or expired caller must still record how the run ended.
//  2. The write has a finite budget of its own (StartedRunTerminalWriteTimeout).
//  3. How the run ended is classified by RunOutcomeOf: a cancellation is
//     cancelled; an expired deadline and any other error are failed.
//  4. A write that fails is returned together with the cause and marked with
//     ErrStartedRunNotClosed. It is never dropped.

// StartedRunTerminalWriteTimeout is the budget of one terminal write of a Run
// the caller started. It is detached from the caller's context, so it must
// outlast the stores: every Task and Action transaction reads the whole
// history, and on the production host one write took 7 to 11 seconds
// (2026-10-12). It is the same rule and value as the other terminal writes
// (Heartbeat, OPS ingress, Tool attempts). Each write gets its own budget.
const StartedRunTerminalWriteTimeout = 60 * time.Second

// ErrStartedRunNotClosed marks an error that carries a failed terminal write:
// the Run, and with it the Task, is still running and holds its execution slot.
// A caller that does not report a plain cancellation must still report an error
// that carries this marker, because a cancelled run is the likely moment for the
// terminal write to fail too.
var ErrStartedRunNotClosed = errors.New("started run was not closed")

// RunOutcome is how a started Run ended: the terminal status of its Task and
// the summary recorded with it.
type RunOutcome struct {
	Status  domaintask.Status
	Summary string
}

// RunOutcomeOf classifies the result of the work a Run was started for. A nil
// error is succeeded with successSummary. A cancellation is cancelled. An
// expired deadline and every other error are failed. The summary of a failure is
// the error text.
//
// A caller whose outcome is not an error (a recorded playback result) builds a
// RunOutcome directly.
func RunOutcomeOf(operationErr error, successSummary string) RunOutcome {
	switch {
	case operationErr == nil:
		return RunOutcome{Status: domaintask.StatusSucceeded, Summary: successSummary}
	case errors.Is(operationErr, context.Canceled):
		return RunOutcome{Status: domaintask.StatusCancelled, Summary: operationErr.Error()}
	default:
		return RunOutcome{Status: domaintask.StatusFailed, Summary: operationErr.Error()}
	}
}

// CloseStartedRun closes a Run the caller started with CreateAndStartRun, and
// its Task, with the given outcome. actorID is the actor the Run was started
// for. It returns cause when the Run was closed, so a caller can write
// `return tasks.CloseStartedRun(ctx, ..., RunOutcomeOf(err, ""), err)`: that
// returns err and nil for a nil err. When the write fails it returns the failure
// joined with cause and marked with ErrStartedRunNotClosed; with a nil cause
// that error alone is returned, so a non-nil result always means the caller has
// to report a problem even though its own work succeeded.
//
// Call it on every path that ends after the start. It never relies on ctx
// staying alive, and it closes only the Run and Task: the Action and Attempt of
// the work belong to the Action owner and are closed first.
func (m *Manager) CloseStartedRun(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, outcome RunOutcome, cause error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	switch outcome.Status {
	case domaintask.StatusSucceeded, domaintask.StatusFailed, domaintask.StatusCancelled:
	default:
		return errors.Join(cause, fmt.Errorf("%w: run outcome status %q is not terminal", ErrStartedRunNotClosed, outcome.Status))
	}
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), StartedRunTerminalWriteTimeout)
	defer cancel()
	if _, err := m.CompleteRun(closeCtx, taskID, runID, actorID, outcome.Status, outcome.Summary, ""); err != nil {
		return errors.Join(cause, fmt.Errorf("%w: complete run: %w", ErrStartedRunNotClosed, err))
	}
	return cause
}

// AbortStartedRun closes a Run whose work never began, because the preparation
// that follows CreateAndStartRun failed with cause (binding the identity,
// creating the Action). It is CloseStartedRun with the outcome RunOutcomeOf
// gives that cause, and it returns cause the same way: unchanged when the Run
// was closed, joined with the failed write and marked ErrStartedRunNotClosed
// when it was not. A nil cause is a caller error and is reported as a failure.
func (m *Manager) AbortStartedRun(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, cause error) error {
	if cause == nil {
		cause = errors.New("started run aborted without a cause")
	}
	return m.CloseStartedRun(ctx, taskID, runID, actorID, RunOutcomeOf(cause, ""), cause)
}
