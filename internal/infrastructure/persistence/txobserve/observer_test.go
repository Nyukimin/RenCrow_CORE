package txobserve

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
)

// harness drives an Observer with a fake clock, a fake sizing function and a
// captured log.
type harness struct {
	mu       sync.Mutex
	now      time.Time
	lines    []string
	hotCalls int
	observer *Observer
}

func newHarness(t *testing.T, mutate func(*Options)) *harness {
	t.Helper()
	h := &harness{now: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)}
	opts := Options{
		Prefix: testPrefix,
		Hot: func() HotSizes {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.hotCalls++
			return HotSizes{Files: []HotFile{{Name: "task_state", Bytes: 1000}, {Name: "task_run", Bytes: 234}}, OK: true}
		},
		Now: func() time.Time {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.now
		},
		Logf: func(format string, args ...any) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.lines = append(h.lines, fmt.Sprintf(format, args...))
		},
	}
	if mutate != nil {
		mutate(&opts)
	}
	h.observer = New(opts)
	return h
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = h.now.Add(d)
}

func (h *harness) logged() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.lines...)
}

func (h *harness) hotSized() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hotCalls
}

func fastWrite() jsonlbatch.TxObservation {
	return jsonlbatch.TxObservation{Kind: jsonlbatch.TxKindWrite, Acquired: true, LockWait: ms(1), Exec: ms(20), Commit: ms(5), Files: []string{"task_state.jsonl"}}
}

func TestObserverStaysSilentAndSkipsSizingForFastTransactions(t *testing.T) {
	h := newHarness(t, nil)
	for i := 0; i < 50; i++ {
		h.observer.Observe(context.Background(), fastWrite())
		h.advance(time.Second)
	}
	if lines := h.logged(); len(lines) != 0 {
		t.Fatalf("fast transactions logged %v, want nothing before the summary interval", lines)
	}
	if h.hotSized() != 0 {
		t.Fatalf("hot sizes read %d times for fast transactions, want 0 (only slow lines and summaries size the files)", h.hotSized())
	}
}

func TestObserverLogsOneSlowLinePerSlowTransaction(t *testing.T) {
	h := newHarness(t, nil)
	ctx := WithLabel(context.Background(), Label{Op: "TaskTransaction", ScopeKey: "task_id", ScopeValue: "tsk_slow"})
	slow := fastWrite()
	slow.Exec = 5 * time.Second
	slow.Commit = time.Second
	h.observer.Observe(ctx, slow)
	h.observer.Observe(ctx, fastWrite())
	lockSlow := fastWrite()
	lockSlow.LockWait = 11 * time.Second
	h.observer.Observe(WithLabel(context.Background(), Label{Op: "Transaction", ScopeKey: "task_id"}), lockSlow)

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

func TestObserverSlowLineWithoutLabelIsMarkedUnlabeled(t *testing.T) {
	h := newHarness(t, nil)
	slow := fastWrite()
	slow.Exec = 6 * time.Second
	h.observer.Observe(context.Background(), slow)
	lines := h.logged()
	if len(lines) != 1 || !strings.Contains(lines[0], "op=unlabeled") {
		t.Fatalf("lines = %v, want one unlabeled slow line", lines)
	}
}

func TestObserverSummaryIsEmittedAtMostOncePerInterval(t *testing.T) {
	h := newHarness(t, nil)
	h.observer.Observe(context.Background(), fastWrite())
	h.advance(summaryInterval - time.Second)
	h.observer.Observe(context.Background(), fastWrite())
	if lines := h.logged(); len(lines) != 0 {
		t.Fatalf("lines before the interval = %v, want none", lines)
	}

	h.advance(time.Second)
	h.observer.Observe(context.Background(), jsonlbatch.TxObservation{Kind: jsonlbatch.TxKindRead, Acquired: true, Exec: ms(40)})
	lines := h.logged()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "[TaskStore] tx summary window=10m0s write_n=2 ") || !strings.Contains(lines[0], " read_n=1 ") || !strings.HasSuffix(lines[0], " hot_bytes=1234 task_state_bytes=1000 task_run_bytes=234") {
		t.Fatalf("summary lines = %v", lines)
	}

	// Within the next interval nothing more is printed, however many transactions finish.
	for i := 0; i < 20; i++ {
		h.advance(10 * time.Second)
		h.observer.Observe(context.Background(), fastWrite())
	}
	if lines := h.logged(); len(lines) != 1 {
		t.Fatalf("lines inside the second interval = %d, want still 1", len(lines))
	}

	h.advance(summaryInterval)
	h.observer.Observe(context.Background(), fastWrite())
	if lines := h.logged(); len(lines) != 2 {
		t.Fatalf("lines after the second interval = %d, want 2", len(lines))
	}
}

func TestObserverPrintsNothingWhileNoTransactionFinishes(t *testing.T) {
	h := newHarness(t, nil)
	h.observer.Observe(context.Background(), fastWrite())
	// A long quiet period must not produce output by itself: there is no timer.
	h.advance(24 * time.Hour)
	if lines := h.logged(); len(lines) != 0 {
		t.Fatalf("lines during a quiet period = %v, want none", lines)
	}
	h.observer.Observe(context.Background(), fastWrite())
	if lines := h.logged(); len(lines) != 1 || !strings.Contains(lines[0], "tx summary") {
		t.Fatalf("lines after the next transaction = %v, want one summary", lines)
	}
}

func TestObserverSummaryCoversOnlyTheLastRingCapacitySamples(t *testing.T) {
	h := newHarness(t, nil)
	for i := 0; i < ringCapacity+100; i++ {
		h.observer.Observe(context.Background(), fastWrite())
	}
	h.advance(summaryInterval)
	h.observer.Observe(context.Background(), fastWrite())
	lines := h.logged()
	if len(lines) != 1 || !strings.Contains(lines[0], fmt.Sprintf(" write_n=%d ", ringCapacity)) {
		t.Fatalf("lines = %v, want a summary over exactly %d samples", lines, ringCapacity)
	}
}

func TestObserverClaimsTheSummaryOnceUnderConcurrency(t *testing.T) {
	h := newHarness(t, nil)
	h.advance(summaryInterval)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.observer.Observe(context.Background(), fastWrite())
		}()
	}
	wg.Wait()
	if lines := h.logged(); len(lines) != 1 {
		t.Fatalf("summary lines under concurrency = %d, want exactly 1: %v", len(lines), lines)
	}
}

func TestObserverReportsLockFailureAsSlowWithoutPhases(t *testing.T) {
	h := newHarness(t, nil)
	h.observer.Observe(context.Background(), jsonlbatch.TxObservation{Kind: jsonlbatch.TxKindWrite, LockWait: 12 * time.Second, Err: context.DeadlineExceeded})
	lines := h.logged()
	if len(lines) != 1 || !strings.Contains(lines[0], "result=lock_failed") || strings.Contains(lines[0], "DeadlineExceeded") || strings.Contains(lines[0], "deadline") {
		t.Fatalf("lines = %v, want a lock_failed line that carries no error text", lines)
	}
}

// The prefix and the hot-file names are the only per-store parts of a line.
func TestObserverUsesThePrefixAndHotFileNamesItWasGiven(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.Prefix = "[ActionStore]"
		o.Hot = func() HotSizes {
			return HotSizes{Files: []HotFile{{Name: "action_state", Bytes: 70}, {Name: "action_attempt", Bytes: 30}}, OK: true}
		}
	})
	ctx := WithLabel(context.Background(), Label{Op: "Transaction"})
	slow := fastWrite()
	slow.Exec = 6 * time.Second
	h.observer.Observe(ctx, slow)
	h.advance(summaryInterval)
	h.observer.Observe(ctx, fastWrite())

	lines := h.logged()
	if len(lines) != 2 {
		t.Fatalf("lines = %v, want a slow line and a summary", lines)
	}
	if want := "[ActionStore] tx slow kind=write op=Transaction result=ok lock_wait=1ms settle=0s exec=6s commit=5ms files=task_state.jsonl hot_bytes=100"; lines[0] != want {
		t.Fatalf("slow line =\n%s\nwant\n%s", lines[0], want)
	}
	if !strings.HasPrefix(lines[1], "[ActionStore] tx summary window=") || !strings.HasSuffix(lines[1], " hot_bytes=100 action_state_bytes=70 action_attempt_bytes=30") {
		t.Fatalf("summary line = %q", lines[1])
	}
}

func TestObserverWithoutHotSizesReportsThemUnknown(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Hot = nil })
	slow := fastWrite()
	slow.Exec = 6 * time.Second
	h.observer.Observe(context.Background(), slow)
	lines := h.logged()
	if len(lines) != 1 || !strings.HasSuffix(lines[0], " hot_bytes=unknown") {
		t.Fatalf("lines = %v, want hot_bytes=unknown", lines)
	}
}

// Zero thresholds mean the defaults; a negative value forces every transaction
// onto the slow path (the way store tests exercise the labels).
func TestObserverThresholdOptions(t *testing.T) {
	h := newHarness(t, nil)
	h.observer.Observe(context.Background(), fastWrite())
	if lines := h.logged(); len(lines) != 0 {
		t.Fatalf("default thresholds logged %v for a fast transaction", lines)
	}

	forced := newHarness(t, func(o *Options) { o.SlowExecCommit, o.SlowLockWait = -1, -1 })
	forced.observer.Observe(context.Background(), fastWrite())
	if lines := forced.logged(); len(lines) != 1 || !strings.Contains(lines[0], " tx slow ") {
		t.Fatalf("negative thresholds logged %v, want one slow line", lines)
	}

	short := newHarness(t, func(o *Options) { o.SummaryInterval = time.Minute })
	short.advance(time.Minute)
	short.observer.Observe(context.Background(), fastWrite())
	if lines := short.logged(); len(lines) != 1 || !strings.Contains(lines[0], " tx summary window=") {
		t.Fatalf("a one-minute interval logged %v, want one summary", lines)
	}
}

func TestLabelTolerantOfNilContext(t *testing.T) {
	var nilCtx context.Context // a nil ctx must degrade to "unlabeled", not panic
	if got := LabelFrom(WithLabel(nilCtx, Label{Op: "Transaction"})); got != (Label{}) {
		t.Fatalf("label from a nil context = %+v, want zero", got)
	}
	ctx := WithLabel(context.Background(), Label{Op: "ReadTransaction", ScopeKey: "task_id", ScopeValue: "tsk_x"})
	if got := LabelFrom(ctx); got != (Label{Op: "ReadTransaction", ScopeKey: "task_id", ScopeValue: "tsk_x"}) {
		t.Fatalf("label = %+v", got)
	}
	if got := LabelFrom(nilCtx); got != (Label{}) {
		t.Fatalf("label from nil = %+v, want zero", got)
	}
}
