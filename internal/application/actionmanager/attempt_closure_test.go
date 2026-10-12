package actionmanager_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	actionstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Failure knowledge: docs/調査/*Run開始後の早期失敗でRunを閉じない_FailureKnowledge.md.

func TestAttemptOutcomeOfClassifiesHowTheWorkEnded(t *testing.T) {
	other := errors.New("some failure")
	cases := []struct {
		name        string
		err         error
		wantAttempt domainaction.AttemptStatus
		wantAction  domainaction.Status
		wantSummary string
	}{
		{"success", nil, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "done"},
		{"cancellation", context.Canceled, domainaction.AttemptStatusCancelled, domainaction.StatusCancelled, context.Canceled.Error()},
		{"wrapped cancellation", fmt.Errorf("call: %w", context.Canceled), domainaction.AttemptStatusCancelled, domainaction.StatusCancelled, "call: context canceled"},
		{"expired deadline", context.DeadlineExceeded, domainaction.AttemptStatusTimedOut, domainaction.StatusFailed, context.DeadlineExceeded.Error()},
		{"other error", other, domainaction.AttemptStatusFailed, domainaction.StatusFailed, other.Error()},
		{"cancellation together with a deadline", errors.Join(context.Canceled, context.DeadlineExceeded), domainaction.AttemptStatusCancelled, domainaction.StatusCancelled, "context canceled\ncontext deadline exceeded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := actionmanager.AttemptOutcomeOf(tc.err, "done")
			if got.Attempt != tc.wantAttempt || got.Action != tc.wantAction || got.Summary != tc.wantSummary {
				t.Fatalf("AttemptOutcomeOf(%v) = %+v, want %s/%s %q", tc.err, got, tc.wantAttempt, tc.wantAction, tc.wantSummary)
			}
		})
	}
}

// closingStore refuses every Action transaction after the first (CreateAction)
// when err is set, and records the budget of each of them.
type closingStore struct {
	actionmanager.Store
	err error

	mu        sync.Mutex
	calls     int
	budgets   []time.Duration
	unbounded int
}

func (s *closingStore) Transaction(ctx context.Context, fn func(actionmanager.Store) error) error {
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
	return s.Store.Transaction(ctx, fn)
}

func openAttemptForClosure(t *testing.T, refuse error) (*actionmanager.Manager, *closingStore, domainaction.Action, domainaction.Attempt) {
	t.Helper()
	base, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	probe := &closingStore{Store: base, err: refuse}
	manager := actionmanager.New(probe)
	action, attempt, err := manager.CreateAction(context.Background(), actionmanager.CreateInput{
		TaskID: modulecore.NewTaskID(),
		RunID:  modulecore.NewRunID(),
		Kind:   domainaction.KindTTS,
		Name:   "close_attempt",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return manager, probe, action, attempt
}

func TestCloseAttemptClosesWithTheClassifiedOutcome(t *testing.T) {
	cases := []struct {
		name        string
		cause       error
		wantAttempt domainaction.AttemptStatus
		wantAction  domainaction.Status
	}{
		{"success", nil, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded},
		{"cancellation", context.Canceled, domainaction.AttemptStatusCancelled, domainaction.StatusCancelled},
		{"deadline", context.DeadlineExceeded, domainaction.AttemptStatusTimedOut, domainaction.StatusFailed},
		{"other error", errors.New("boom"), domainaction.AttemptStatusFailed, domainaction.StatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manager, _, action, attempt := openAttemptForClosure(t, nil)

			closed, err := manager.CloseAttempt(context.Background(), action.ActionID, attempt.AttemptID, actionmanager.AttemptOutcomeOf(tc.cause, "done"), tc.cause)

			if !closed || err != tc.cause {
				t.Fatalf("CloseAttempt = %v, %v, want closed with the cause itself (%v)", closed, err, tc.cause)
			}
			gotAction, err := manager.GetAction(context.Background(), action.ActionID)
			if err != nil {
				t.Fatal(err)
			}
			attempts, err := manager.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: action.ActionID})
			if err != nil || len(attempts) != 1 {
				t.Fatalf("attempts=%d err=%v", len(attempts), err)
			}
			if gotAction.Status != tc.wantAction || attempts[0].Status != tc.wantAttempt {
				t.Fatalf("Action/Attempt = %s/%s, want %s/%s", gotAction.Status, attempts[0].Status, tc.wantAction, tc.wantAttempt)
			}
		})
	}
}

func TestCloseAttemptIsDetachedFromTheCallerAndBounded(t *testing.T) {
	manager, probe, action, attempt := openAttemptForClosure(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	closed, err := manager.CloseAttempt(ctx, action.ActionID, attempt.AttemptID, actionmanager.AttemptOutcomeOf(context.Canceled, ""), context.Canceled)

	if !closed || !errors.Is(err, context.Canceled) || errors.Is(err, actionmanager.ErrAttemptNotClosed) {
		t.Fatalf("CloseAttempt = %v, %v, want closed with the cancellation alone", closed, err)
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if len(probe.budgets) != 1 || probe.unbounded != 0 {
		t.Fatalf("terminal writes: %d bounded, %d unbounded, want exactly one bounded", len(probe.budgets), probe.unbounded)
	}
	if budget := probe.budgets[0]; budget <= 0 || budget > actionmanager.AttemptTerminalWriteTimeout {
		t.Fatalf("terminal write budget = %s, want within (0, %s]", budget, actionmanager.AttemptTerminalWriteTimeout)
	}
	if actionmanager.AttemptTerminalWriteTimeout != 60*time.Second {
		t.Fatalf("AttemptTerminalWriteTimeout = %s, want the 60s of the other terminal writes", actionmanager.AttemptTerminalWriteTimeout)
	}
}

func TestCloseAttemptReturnsAFailedWriteWithItsCause(t *testing.T) {
	refused := errors.New("terminal write refused")
	for _, tc := range []struct {
		name  string
		cause error
	}{
		{"with a cause", context.Canceled},
		{"without a cause", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, _, action, attempt := openAttemptForClosure(t, refused)

			closed, err := manager.CloseAttempt(context.Background(), action.ActionID, attempt.AttemptID, actionmanager.AttemptOutcomeOf(tc.cause, ""), tc.cause)

			if closed || !errors.Is(err, refused) || !errors.Is(err, actionmanager.ErrAttemptNotClosed) {
				t.Fatalf("CloseAttempt = %v, %v, want the refused write marked as not closed", closed, err)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("CloseAttempt = %v, lost the cause %v", err, tc.cause)
			}
			got, getErr := manager.GetAction(context.Background(), action.ActionID)
			if getErr != nil || got.Status != domainaction.StatusOpen {
				t.Fatalf("Action = %#v err=%v, want it still open because the write was refused", got, getErr)
			}
		})
	}
}
