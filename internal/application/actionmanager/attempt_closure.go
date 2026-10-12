package actionmanager

import (
	"context"
	"errors"
	"fmt"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Failure knowledge: docs/調査/*Run開始後の早期失敗でRunを閉じない_FailureKnowledge.md.
//
// CloseAttempt is the Action owner's half of closing what a function that
// started a Run opened. It applies the same rule as
// taskmanager.CloseStartedRun, so the two cannot drift apart: the write is
// detached from the caller's cancellation, has a finite budget of its own
// (AttemptTerminalWriteTimeout), classifies how the work ended
// (AttemptOutcomeOf), and returns a failed write together with its cause.

// ErrAttemptNotClosed marks an error that carries a failed terminal write of an
// Attempt: the Attempt and its Action are still open. A caller that closes the
// Run after the Attempt, as Memory Promotion always did, does not close the Run
// either when this write fails, so the Run keeps its execution slot too. The
// restart recovery of Action-backed Runs settles both from the open Action. A
// caller that has to report this to its own caller treats it like
// taskmanager.ErrStartedRunNotClosed.
var ErrAttemptNotClosed = errors.New("attempt was not closed")

// AttemptOutcome is how an Attempt and its Action ended.
type AttemptOutcome struct {
	Attempt domainaction.AttemptStatus
	Action  domainaction.Status
	Summary string
}

// AttemptOutcomeOf classifies the result of the work an Attempt recorded. A nil
// error is succeeded with successSummary. A cancellation is cancelled. An
// expired deadline is a timed-out Attempt of a failed Action; every other error
// is failed. The summary of a failure is the error text.
//
// The statuses agree with taskmanager.RunOutcomeOf for the same error: a
// cancelled Action is a cancelled Task, every other failed Action a failed Task.
// A test in taskmanagertest holds the two together.
func AttemptOutcomeOf(operationErr error, successSummary string) AttemptOutcome {
	switch {
	case operationErr == nil:
		return AttemptOutcome{Attempt: domainaction.AttemptStatusSucceeded, Action: domainaction.StatusSucceeded, Summary: successSummary}
	case errors.Is(operationErr, context.Canceled):
		return AttemptOutcome{Attempt: domainaction.AttemptStatusCancelled, Action: domainaction.StatusCancelled, Summary: operationErr.Error()}
	case errors.Is(operationErr, context.DeadlineExceeded):
		return AttemptOutcome{Attempt: domainaction.AttemptStatusTimedOut, Action: domainaction.StatusFailed, Summary: operationErr.Error()}
	default:
		return AttemptOutcome{Attempt: domainaction.AttemptStatusFailed, Action: domainaction.StatusFailed, Summary: operationErr.Error()}
	}
}

// CloseAttempt closes the current Attempt of an Action, and the Action, with the
// given outcome. closed reports whether the write landed. When it did, err is
// cause unchanged (nil for a nil cause). When it did not, err is the failure
// joined with cause and marked with ErrAttemptNotClosed.
//
// closed is separate from err because a caller composes this with the closing of
// its Run and has to decide whether to go on; testing the joined error with
// errors.Is would mistake a marker that the cause itself carries (the work was
// another wrapped call) for a failed write of this one.
func (m *Manager) CloseAttempt(ctx context.Context, actionID modulecore.ActionID, attemptID modulecore.AttemptID, outcome AttemptOutcome, cause error) (closed bool, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), AttemptTerminalWriteTimeout)
	defer cancel()
	if _, _, writeErr := m.CompleteAttempt(closeCtx, actionID, attemptID, outcome.Attempt, outcome.Action, outcome.Summary); writeErr != nil {
		return false, errors.Join(cause, fmt.Errorf("%w: complete attempt: %w", ErrAttemptNotClosed, writeErr))
	}
	return true, cause
}
