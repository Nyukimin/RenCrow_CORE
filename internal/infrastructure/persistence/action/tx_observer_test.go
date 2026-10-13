package action

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/txobserve"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// observedStore opens a real store whose observer treats every transaction as
// slow and writes into the returned drain, so each entry point leaves one
// labelled line. The open itself is observed too (the store is opened through the
// same seam production uses), so its line is the first one drained.
func observedStore(t *testing.T, tune func(*txobserve.Options)) (*JSONLStore, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var lines []string
	store, err := newJSONLStore(filepath.Join(t.TempDir(), "actions"), OpenOptions{Index: defaultIndexOption}, func(o *txobserve.Options) {
		o.Logf = func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, fmt.Sprintf(format, args...))
		}
		o.SlowExecCommit = -1
		o.SlowLockWait = -1
		if tune != nil {
			tune(o)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := append([]string(nil), lines...)
		lines = lines[:0]
		return out
	}
}

func observedAction(now time.Time) (domainaction.Action, domainaction.Attempt) {
	action := domainaction.Action{
		ActionID:  modulecore.NewActionID(),
		TaskID:    modulecore.NewTaskID(),
		RunID:     modulecore.NewRunID(),
		Kind:      domainaction.KindTool,
		Status:    domainaction.StatusOpen,
		CreatedAt: now,
		UpdatedAt: now,
	}
	attempt := domainaction.Attempt{
		AttemptID:   modulecore.NewAttemptID(),
		ActionID:    action.ActionID,
		StartReason: domainaction.AttemptStartReasonFirst,
		Status:      domainaction.AttemptStatusRunning,
		StartedAt:   now,
	}
	return action, attempt
}

func TestStoreOpenIsObservedAndLabelled(t *testing.T) {
	_, drain := observedStore(t, nil)

	lines := drain()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "[ActionStore] tx slow kind=read op=Open ") {
		t.Fatalf("open lines = %q, want exactly one labelled read line", lines)
	}
}

func TestStoreLabelsEveryBatchEntryPoint(t *testing.T) {
	store, drain := observedStore(t, nil)
	drain()
	ctx := context.Background()
	action, attempt := observedAction(time.Now().UTC())
	// An active attempt must be its action's current one, so both are written
	// together first; the single-record saves below then re-save them unchanged.
	action.CurrentAttemptID = attempt.AttemptID
	if err := store.Transaction(ctx, func(tx domainaction.Store) error {
		if err := tx.SaveAction(ctx, action); err != nil {
			return err
		}
		return tx.SaveAttempt(ctx, attempt)
	}); err != nil {
		t.Fatal(err)
	}
	drain()

	expectOne := func(name, op, kind string, run func() error) {
		t.Helper()
		if err := run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		lines := drain()
		if len(lines) != 1 {
			t.Fatalf("%s: lines = %q, want exactly one", name, lines)
		}
		if want := "[ActionStore] tx slow kind=" + kind + " op=" + op + " "; !strings.HasPrefix(lines[0], want) {
			t.Fatalf("%s: line = %q, want prefix %q", name, lines[0], want)
		}
		if strings.Contains(lines[0], "_id=") {
			t.Fatalf("%s: line = %q, want no record id (the Action store labels carry the op only)", name, lines[0])
		}
	}

	expectOne("SaveAction", "SaveAction", "write", func() error { return store.SaveAction(ctx, action) })
	expectOne("SaveAttempt", "SaveAttempt", "write", func() error { return store.SaveAttempt(ctx, attempt) })
	expectOne("GetAction", "GetAction", "read", func() error { _, err := store.GetAction(ctx, action.ActionID); return err })
	expectOne("ListActions", "ListActions", "read", func() error {
		_, err := store.ListActions(ctx, domainaction.Filter{TaskID: action.TaskID})
		return err
	})
	expectOne("GetAttempt", "GetAttempt", "read", func() error { _, err := store.GetAttempt(ctx, attempt.AttemptID); return err })
	expectOne("ListAttempts", "ListAttempts", "read", func() error {
		_, err := store.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: action.ActionID})
		return err
	})
	expectOne("Transaction", "Transaction", "write", func() error {
		return store.Transaction(ctx, func(domainaction.Store) error { return nil })
	})
	expectOne("ReadTransaction", "ReadTransaction", "read", func() error {
		return store.ReadTransaction(ctx, func(domainaction.Store) error { return nil })
	})
}

// A nested Transaction on the transaction store joins the outer batch
// transaction: one store call is one observation, whatever it does inside.
func TestStoreObservesOneTransactionPerStoreCall(t *testing.T) {
	store, drain := observedStore(t, nil)
	drain()
	ctx := context.Background()
	action, attempt := observedAction(time.Now().UTC())

	action.CurrentAttemptID = attempt.AttemptID
	if err := store.Transaction(ctx, func(tx domainaction.Store) error {
		if err := tx.SaveAction(ctx, action); err != nil {
			return err
		}
		if err := tx.SaveAttempt(ctx, attempt); err != nil {
			return err
		}
		_, err := tx.GetAction(ctx, action.ActionID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	lines := drain()
	if len(lines) != 1 || !strings.Contains(lines[0], " kind=write op=Transaction ") {
		t.Fatalf("lines = %q, want one Transaction line", lines)
	}
	if !strings.Contains(lines[0], " files=action_attempt.jsonl+action_state.jsonl ") {
		t.Fatalf("line = %q, want both appended files listed", lines[0])
	}
}

func TestStoreSlowLineReportsRealHotSizes(t *testing.T) {
	store, drain := observedStore(t, nil)
	drain()
	action, _ := observedAction(time.Now().UTC())

	if err := store.SaveAction(context.Background(), action); err != nil {
		t.Fatal(err)
	}

	lines := drain()
	if len(lines) != 1 {
		t.Fatalf("lines = %q, want one", lines)
	}
	state, err := os.Stat(filepath.Join(store.root, actionFilename))
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := os.Stat(filepath.Join(store.root, attemptFilename))
	if err != nil {
		t.Fatal(err)
	}
	// The line is logged after the commit, so it sees the appended record.
	if want := fmt.Sprintf(" hot_bytes=%d", state.Size()+attempts.Size()); !strings.HasSuffix(lines[0], want) || state.Size() == 0 {
		t.Fatalf("line = %q, want suffix %s (action=%d attempt=%d)", lines[0], want, state.Size(), attempts.Size())
	}
}

func TestStoreSummaryReportsEachHotFileUnderItsKey(t *testing.T) {
	store, drain := observedStore(t, func(o *txobserve.Options) {
		o.SlowExecCommit = 0
		o.SlowLockWait = 0
		o.SummaryInterval = time.Nanosecond
	})
	drain()
	action, attempt := observedAction(time.Now().UTC())
	action.CurrentAttemptID = attempt.AttemptID
	if err := store.Transaction(context.Background(), func(tx domainaction.Store) error {
		if err := tx.SaveAction(context.Background(), action); err != nil {
			return err
		}
		return tx.SaveAttempt(context.Background(), attempt)
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if _, err := store.GetAction(context.Background(), action.ActionID); err != nil {
		t.Fatal(err)
	}

	state, err := os.Stat(filepath.Join(store.root, actionFilename))
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := os.Stat(filepath.Join(store.root, attemptFilename))
	if err != nil {
		t.Fatal(err)
	}
	lines := drain()
	if len(lines) == 0 {
		t.Fatal("no summary line was written")
	}
	last := lines[len(lines)-1]
	want := fmt.Sprintf(" hot_bytes=%d action_state_bytes=%d action_attempt_bytes=%d", state.Size()+attempts.Size(), state.Size(), attempts.Size())
	if !strings.HasPrefix(last, "[ActionStore] tx summary window=") || !strings.HasSuffix(last, want) {
		t.Fatalf("summary line = %q, want an [ActionStore] summary ending with%s", last, want)
	}
	for _, token := range []string{" write_n=", " write_commit_p50=", " read_n=", " read_exec_p95=", " write_lock_wait_p95="} {
		if !strings.Contains(last, token) {
			t.Fatalf("summary line %q lacks %q", last, token)
		}
	}
}

// Lines carry the op, the file names and sizes, and never a record body, id or
// error text.
func TestStoreLinesCarryNoRecordContent(t *testing.T) {
	store, drain := observedStore(t, nil)
	drain()
	action, _ := observedAction(time.Now().UTC())
	action.Name = "secret-looking-name"
	if err := store.SaveAction(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	// A failing callback must not leak its error text either.
	_ = store.Transaction(context.Background(), func(domainaction.Store) error { return fmt.Errorf("leak-me-%s", action.ActionID) })

	for _, line := range drain() {
		if strings.Contains(line, "secret-looking-name") || strings.Contains(line, "leak-me") || strings.Contains(line, string(action.ActionID)) {
			t.Fatalf("line %q carries record content", line)
		}
	}
}
