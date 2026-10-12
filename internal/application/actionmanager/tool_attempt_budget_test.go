package actionmanager_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	actionstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// observedToolBookkeeping is the time the production host took for one whole
// Tool call on 2026-10-12: 7 to 10 seconds from "tool start" to "tool complete"
// in the journal, for Tools whose own work is trivial, while the Action files
// held about 55,000 Actions. The call is the Action create, the admission check
// and the terminal write. It is deliberately the whole call, not one
// transaction, so the budget keeps a margin for the lock waits that other
// writers (Memory Promotion, LLM calls) add.
const observedToolBookkeeping = 10 * time.Second

// terminalWriteBudgetMultiple is the rule shared with the Task terminal write:
// a terminal write must be allowed at least three times the observed latency.
const terminalWriteBudgetMultiple = 3

// deadlineObservingStore records how long the context of every write
// transaction may still run when the store receives it.
type deadlineObservingStore struct {
	actionmanager.Store
	remaining []time.Duration
	unbounded int
}

func (s *deadlineObservingStore) Transaction(ctx context.Context, callback func(actionmanager.Store) error) error {
	if deadline, ok := ctx.Deadline(); ok {
		s.remaining = append(s.remaining, time.Until(deadline))
	} else {
		s.unbounded++
	}
	return s.Store.Transaction(ctx, callback)
}

func newDeadlineObservedToolAttempt(t *testing.T) (*actionmanager.Manager, *deadlineObservingStore, domainaction.Action, domainaction.Attempt) {
	t.Helper()
	store, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	observed := &deadlineObservingStore{Store: store}
	manager := actionmanager.New(observed)
	action, attempt, err := manager.CreateAction(context.Background(), actionmanager.CreateInput{
		TaskID: modulecore.NewTaskID(),
		RunID:  modulecore.NewRunID(),
		Kind:   domainaction.KindTool,
		Name:   "rencrow_system_monitor",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Only the terminal write is under test; forget the create transaction.
	observed.remaining, observed.unbounded = nil, 0
	return manager, observed, action, attempt
}

// The terminal write of a Tool attempt must outlast the store's measured
// latency. A 5 second budget expired on the production host and turned a
// finished Tool call into HTTP 500 with the Attempt left running.
func TestCompleteToolAttemptTerminalWriteBudgetCoversObservedStoreLatency(t *testing.T) {
	t.Parallel()
	manager, observed, action, attempt := newDeadlineObservedToolAttempt(t)

	// The caller's own deadline has already run out, as when a request budget
	// expires while the Tool runs. The terminal write must not inherit it.
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	if err := manager.CompleteToolAttempt(expired, action.ActionID, attempt.AttemptID, nil, nil); err != nil {
		t.Fatalf("CompleteToolAttempt() error = %v", err)
	}

	if len(observed.remaining) != 1 {
		t.Fatalf("terminal write transactions with a deadline = %d (unbounded = %d), want exactly 1 bounded", len(observed.remaining), observed.unbounded)
	}
	want := terminalWriteBudgetMultiple * observedToolBookkeeping
	if observed.remaining[0] < want {
		t.Fatalf("terminal write budget = %v, want at least %v (%d x the observed %v)", observed.remaining[0].Round(time.Second), want, terminalWriteBudgetMultiple, observedToolBookkeeping)
	}
}

// The budget stays finite: a wedged store must still release the caller.
func TestCompleteToolAttemptTerminalWriteBudgetIsBounded(t *testing.T) {
	t.Parallel()
	manager, observed, action, attempt := newDeadlineObservedToolAttempt(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if err := manager.CompleteToolAttempt(cancelled, action.ActionID, attempt.AttemptID, nil, nil); err != nil {
		t.Fatalf("CompleteToolAttempt() error = %v", err)
	}

	if observed.unbounded != 0 || len(observed.remaining) != 1 {
		t.Fatalf("terminal write deadlines = %v, unbounded = %d, want one bounded write", observed.remaining, observed.unbounded)
	}
	if observed.remaining[0] > 5*time.Minute {
		t.Fatalf("terminal write budget = %v, want a bound of at most 5m", observed.remaining[0].Round(time.Second))
	}
}
