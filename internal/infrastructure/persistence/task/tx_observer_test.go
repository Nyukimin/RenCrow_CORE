package task

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// txHarness drives a txObserver with a fake clock, a fake stat and a captured log.
type txHarness struct {
	mu        sync.Mutex
	now       time.Time
	lines     []string
	statCalls int
	observer  *txObserver
}

func newTxHarness(t *testing.T) *txHarness {
	t.Helper()
	h := &txHarness{now: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)}
	h.observer = newTxObserverWith("state.jsonl", "run.jsonl", txObserverDeps{
		now: func() time.Time {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.now
		},
		logf: func(format string, args ...any) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.lines = append(h.lines, fmt.Sprintf(format, args...))
		},
		statSize: func(path string) (int64, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.statCalls++
			if path == "state.jsonl" {
				return 1000, nil
			}
			return 234, nil
		},
	})
	return h
}

func (h *txHarness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = h.now.Add(d)
}

func (h *txHarness) logged() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.lines...)
}

func (h *txHarness) stats() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.statCalls
}

func fastWrite() jsonlbatch.TxObservation {
	return jsonlbatch.TxObservation{Kind: jsonlbatch.TxKindWrite, Acquired: true, LockWait: ms(1), Exec: ms(20), Commit: ms(5), Files: []string{"task_state.jsonl"}}
}

func TestTxObserverStaysSilentAndSkipsStatForFastTransactions(t *testing.T) {
	h := newTxHarness(t)
	for i := 0; i < 50; i++ {
		h.observer.observe(context.Background(), fastWrite())
		h.advance(time.Second)
	}
	if lines := h.logged(); len(lines) != 0 {
		t.Fatalf("fast transactions logged %v, want nothing before the summary interval", lines)
	}
	if h.stats() != 0 {
		t.Fatalf("stat called %d times for fast transactions, want 0 (only slow lines and summaries stat)", h.stats())
	}
}

func TestTxObserverLogsOneSlowLinePerSlowTransaction(t *testing.T) {
	h := newTxHarness(t)
	ctx := withTxLabel(context.Background(), "TaskTransaction", modulecore.TaskID("tsk_slow"))
	slow := fastWrite()
	slow.Exec = 5 * time.Second
	slow.Commit = time.Second
	h.observer.observe(ctx, slow)
	h.observer.observe(ctx, fastWrite())
	lockSlow := fastWrite()
	lockSlow.LockWait = 11 * time.Second
	h.observer.observe(withTxLabel(context.Background(), "Transaction", ""), lockSlow)

	lines := h.logged()
	if len(lines) != 2 {
		t.Fatalf("lines = %v, want one line per slow transaction", lines)
	}
	want := "[TaskStore] tx slow kind=write op=TaskTransaction task_id=tsk_slow result=ok lock_wait=1ms settle=0s exec=5s commit=1s files=task_state.jsonl hot_bytes=1234"
	if lines[0] != want {
		t.Fatalf("slow line =\n%s\nwant\n%s", lines[0], want)
	}
	if !strings.Contains(lines[1], "op=Transaction ") || strings.Contains(lines[1], "task_id=") || !strings.Contains(lines[1], "lock_wait=11s") {
		t.Fatalf("lock-wait slow line = %q", lines[1])
	}
}

func TestTxObserverSlowLineWithoutLabelIsMarkedUnlabeled(t *testing.T) {
	h := newTxHarness(t)
	slow := fastWrite()
	slow.Exec = 6 * time.Second
	h.observer.observe(context.Background(), slow)
	lines := h.logged()
	if len(lines) != 1 || !strings.Contains(lines[0], "op=unlabeled") {
		t.Fatalf("lines = %v, want one unlabeled slow line", lines)
	}
}

func TestTxObserverSummaryIsEmittedAtMostOncePerInterval(t *testing.T) {
	h := newTxHarness(t)
	h.observer.observe(context.Background(), fastWrite())
	h.advance(txSummaryInterval - time.Second)
	h.observer.observe(context.Background(), fastWrite())
	if lines := h.logged(); len(lines) != 0 {
		t.Fatalf("lines before the interval = %v, want none", lines)
	}

	h.advance(time.Second)
	h.observer.observe(context.Background(), jsonlbatch.TxObservation{Kind: jsonlbatch.TxKindRead, Acquired: true, Exec: ms(40)})
	lines := h.logged()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "[TaskStore] tx summary window=10m0s write_n=2 ") || !strings.Contains(lines[0], " read_n=1 ") || !strings.HasSuffix(lines[0], " hot_bytes=1234 task_state_bytes=1000 task_run_bytes=234") {
		t.Fatalf("summary lines = %v", lines)
	}

	// Within the next interval nothing more is printed, however many transactions finish.
	for i := 0; i < 20; i++ {
		h.advance(10 * time.Second)
		h.observer.observe(context.Background(), fastWrite())
	}
	if lines := h.logged(); len(lines) != 1 {
		t.Fatalf("lines inside the second interval = %d, want still 1", len(lines))
	}

	h.advance(txSummaryInterval)
	h.observer.observe(context.Background(), fastWrite())
	if lines := h.logged(); len(lines) != 2 {
		t.Fatalf("lines after the second interval = %d, want 2", len(lines))
	}
}

func TestTxObserverPrintsNothingWhileNoTransactionFinishes(t *testing.T) {
	h := newTxHarness(t)
	h.observer.observe(context.Background(), fastWrite())
	// A long quiet period must not produce output by itself: there is no timer.
	h.advance(24 * time.Hour)
	if lines := h.logged(); len(lines) != 0 {
		t.Fatalf("lines during a quiet period = %v, want none", lines)
	}
	h.observer.observe(context.Background(), fastWrite())
	if lines := h.logged(); len(lines) != 1 || !strings.Contains(lines[0], "tx summary") {
		t.Fatalf("lines after the next transaction = %v, want one summary", lines)
	}
}

func TestTxObserverSummaryCoversOnlyTheLastRingCapacitySamples(t *testing.T) {
	h := newTxHarness(t)
	for i := 0; i < txRingCapacity+100; i++ {
		h.observer.observe(context.Background(), fastWrite())
	}
	h.advance(txSummaryInterval)
	h.observer.observe(context.Background(), fastWrite())
	lines := h.logged()
	if len(lines) != 1 || !strings.Contains(lines[0], fmt.Sprintf(" write_n=%d ", txRingCapacity)) {
		t.Fatalf("lines = %v, want a summary over exactly %d samples", lines, txRingCapacity)
	}
}

func TestTxObserverClaimsTheSummaryOnceUnderConcurrency(t *testing.T) {
	h := newTxHarness(t)
	h.advance(txSummaryInterval)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.observer.observe(context.Background(), fastWrite())
		}()
	}
	wg.Wait()
	if lines := h.logged(); len(lines) != 1 {
		t.Fatalf("summary lines under concurrency = %d, want exactly 1: %v", len(lines), lines)
	}
}

func TestTxObserverReportsLockFailureAsSlowWithoutPhases(t *testing.T) {
	h := newTxHarness(t)
	h.observer.observe(context.Background(), jsonlbatch.TxObservation{Kind: jsonlbatch.TxKindWrite, LockWait: 12 * time.Second, Err: context.DeadlineExceeded})
	lines := h.logged()
	if len(lines) != 1 || !strings.Contains(lines[0], "result=lock_failed") || strings.Contains(lines[0], "DeadlineExceeded") || strings.Contains(lines[0], "deadline") {
		t.Fatalf("lines = %v, want a lock_failed line that carries no error text", lines)
	}
}

// labeledStoreHarness opens a real writer store whose observer treats every
// transaction as slow, so each store entry point leaves one labelled line.
func labeledStoreHarness(t *testing.T) (*JSONLStore, func() []string) {
	t.Helper()
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if store.txObserver == nil {
		t.Fatal("writer store has no tx observer installed")
	}
	var mu sync.Mutex
	var lines []string
	store.txObserver.logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	store.txObserver.slowExecCommit = -1
	store.txObserver.slowLockWait = -1
	return store, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := append([]string(nil), lines...)
		lines = lines[:0]
		return out
	}
}

func TestJSONLStoreLabelsEveryBatchEntryPoint(t *testing.T) {
	store, drain := labeledStoreHarness(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	task := snapshotTestTask(now)
	task.Status = domaintask.StatusQueued
	task.StartedAt = nil
	task.DependencyTaskIDs = nil
	drain()

	expectOne := func(name, op string, wantTaskID modulecore.TaskID, wantKind string, run func() error) {
		t.Helper()
		if err := run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		lines := drain()
		if len(lines) != 1 {
			t.Fatalf("%s: lines = %v, want exactly one", name, lines)
		}
		if !strings.Contains(lines[0], " kind="+wantKind+" ") || !strings.Contains(lines[0], " op="+op+" ") {
			t.Fatalf("%s: line = %q, want kind=%s op=%s", name, lines[0], wantKind, op)
		}
		hasTaskID := strings.Contains(lines[0], " task_id=")
		if wantTaskID != "" && !strings.Contains(lines[0], " task_id="+string(wantTaskID)+" ") {
			t.Fatalf("%s: line = %q, want task_id=%s", name, lines[0], wantTaskID)
		}
		if wantTaskID == "" && hasTaskID {
			t.Fatalf("%s: line = %q, want no task_id", name, lines[0])
		}
	}

	expectOne("SaveTask", "TaskTransaction", task.TaskID, "write", func() error { return store.SaveTask(ctx, task) })
	expectOne("GetTask", "ReadTransaction", "", "read", func() error { _, err := store.GetTask(ctx, task.TaskID); return err })
	expectOne("Transaction", "Transaction", "", "write", func() error {
		return store.Transaction(ctx, func(domaintask.Store) error { return nil })
	})
	expectOne("ExecuteIdempotentTaskOperation", "ExecuteIdempotentTaskOperation", task.TaskID, "write", func() error {
		_, err := store.ExecuteIdempotentTaskOperation(ctx, task.TaskID, "op:label-1", taskOperationHash("label-1"), func(domaintask.Store) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		})
		return err
	})
	expectOne("ExecuteIdempotentGlobalTaskOperation", "ExecuteIdempotentGlobalTaskOperation", "", "write", func() error {
		_, err := store.ExecuteIdempotentGlobalTaskOperation(ctx, "op:label-2", taskOperationHash("label-2"), func(domaintask.Store) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		})
		return err
	})
	expectOne("LookupTaskOperation", "LookupTaskOperation", "", "read", func() error {
		_, err := store.LookupTaskOperation(ctx, "op:label-1")
		return err
	})
	expectOne("AcquireTaskExecutionFence", "AcquireTaskExecutionFence", task.TaskID, "write", func() error {
		return store.AcquireTaskExecutionFence(ctx, task.TaskID, "capability:label-1")
	})
	expectOne("GetTaskExecutionFence", "GetTaskExecutionFence", task.TaskID, "read", func() error {
		_, err := store.GetTaskExecutionFence(ctx, task.TaskID)
		return err
	})
	expectOne("ReleaseTaskExecutionFence", "ReleaseTaskExecutionFence", task.TaskID, "write", func() error {
		return store.ReleaseTaskExecutionFence(ctx, task.TaskID, "capability:label-1")
	})
}

func TestJSONLStoreSlowLineReportsRealHotSizes(t *testing.T) {
	store, drain := labeledStoreHarness(t)
	ctx := context.Background()
	task := snapshotTestTask(time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC))
	task.Status = domaintask.StatusQueued
	task.StartedAt = nil
	task.DependencyTaskIDs = nil
	if err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	lines := drain()
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want one", lines)
	}
	state, err := os.Stat(filepath.Join(store.root, stateFilename))
	if err != nil {
		t.Fatal(err)
	}
	run, err := os.Stat(filepath.Join(store.root, runFilename))
	if err != nil {
		t.Fatal(err)
	}
	// The line is logged after the commit, so it sees the appended task record.
	if want := fmt.Sprintf(" hot_bytes=%d", state.Size()+run.Size()); !strings.Contains(lines[0], want) || state.Size() == 0 {
		t.Fatalf("line = %q, want %s (state=%d run=%d)", lines[0], want, state.Size(), run.Size())
	}
	if !strings.Contains(lines[0], " files=task_state.jsonl") {
		t.Fatalf("line = %q, want the appended file list", lines[0])
	}
}

func TestReaderStoreHasNoTxObserver(t *testing.T) {
	root := t.TempDir()
	writer, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := NewJSONLReader(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if reader.txObserver != nil {
		t.Fatal("read-only store installed a tx observer; short-lived CLI handles must stay silent")
	}
}

func TestWithTxLabelTolerantOfNilContext(t *testing.T) {
	var nilCtx context.Context // a nil ctx must degrade to "unlabeled", not panic
	if got := txLabelFrom(withTxLabel(nilCtx, "Transaction", "")); got != (txLabel{}) {
		t.Fatalf("label from a nil context = %+v, want zero", got)
	}
	ctx := withTxLabel(context.Background(), "ReadTransaction", "tsk_x")
	if got := txLabelFrom(ctx); got.op != "ReadTransaction" || got.taskID != "tsk_x" {
		t.Fatalf("label = %+v", got)
	}
	if got := txLabelFrom(nilCtx); got != (txLabel{}) {
		t.Fatalf("label from nil = %+v, want zero", got)
	}
}
