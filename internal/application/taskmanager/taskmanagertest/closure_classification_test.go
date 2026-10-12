package taskmanagertest_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// The Action owner and the Task owner classify how the work ended on their own,
// because neither imports the other. A function that closes both must never get
// a succeeded Action with a failed Task or the reverse, so the two are held
// together here, where both are visible.
func TestRunAndAttemptOutcomesAgreeForEveryKindOfEnd(t *testing.T) {
	cases := map[string]error{
		"success":                       nil,
		"cancellation":                  context.Canceled,
		"wrapped cancellation":          fmt.Errorf("call: %w", context.Canceled),
		"expired deadline":              context.DeadlineExceeded,
		"other error":                   errors.New("boom"),
		"cancellation with a deadline":  errors.Join(context.Canceled, context.DeadlineExceeded),
		"deadline wrapped in a failure": fmt.Errorf("call: %w", context.DeadlineExceeded),
	}
	for name, operationErr := range cases {
		t.Run(name, func(t *testing.T) {
			run := taskmanager.RunOutcomeOf(operationErr, "done")
			attempt := actionmanager.AttemptOutcomeOf(operationErr, "done")

			want := map[domainaction.Status]domaintask.Status{
				domainaction.StatusSucceeded: domaintask.StatusSucceeded,
				domainaction.StatusCancelled: domaintask.StatusCancelled,
				domainaction.StatusFailed:    domaintask.StatusFailed,
			}[attempt.Action]
			if run.Status != want || run.Summary != attempt.Summary {
				t.Fatalf("Task outcome %s %q does not agree with Action outcome %s %q", run.Status, run.Summary, attempt.Action, attempt.Summary)
			}
		})
	}
}
