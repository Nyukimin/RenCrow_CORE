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
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/txobserve"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// The timing arithmetic, the thresholds and both line formats are tested in
// txobserve. These tests cover what the task store adds: that every entry point
// is labelled, that the two hot files are sized (and followed on rotation), and
// that a read-only handle stays silent.

// labeledStoreHarness opens a real writer store whose observer treats every
// transaction as slow, so each store entry point leaves one labelled line. It
// opens the store without the index: an indexed store answers reads without the
// OS lock, so a read is not a batch transaction and leaves no line.
func labeledStoreHarness(t *testing.T) (*JSONLStore, func() []string) {
	t.Helper()
	root := t.TempDir()
	store, err := NewJSONLStoreWithOptions(root, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if store.txObserver == nil || store.txHot == nil {
		t.Fatal("writer store has no tx observer installed")
	}
	var mu sync.Mutex
	var lines []string
	opts := txObserverOptions(store.txHot)
	opts.Logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	opts.SlowExecCommit = -1
	opts.SlowLockWait = -1
	store.txObserver = txobserve.New(opts)
	store.batch.SetTxObserver(store.txObserver.Observe)
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

func TestWithTxLabelCarriesTheOpAndTheTaskScope(t *testing.T) {
	var nilCtx context.Context // a nil ctx must degrade to "unlabeled", not panic
	if got := txobserve.LabelFrom(withTxLabel(nilCtx, "Transaction", "")); got != (txobserve.Label{}) {
		t.Fatalf("label from a nil context = %+v, want zero", got)
	}
	ctx := withTxLabel(context.Background(), "ReadTransaction", "tsk_x")
	if got := txobserve.LabelFrom(ctx); got != (txobserve.Label{Op: "ReadTransaction", ScopeKey: "task_id", ScopeValue: "tsk_x"}) {
		t.Fatalf("label = %+v", got)
	}
}

// The hot files are reported under the names the task store's summary has always
// used, and a rotation moves the sizing to the new segment's files.
func TestTxHotPathsNameTheFilesAndFollowARotation(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	hot := newTxHotPaths(write("state-a", 1000), write("run-a", 234))

	got := hot.sizes()
	want := txobserve.HotSizes{Files: []txobserve.HotFile{{Name: "task_state", Bytes: 1000}, {Name: "task_run", Bytes: 234}}, OK: true}
	if !got.OK || len(got.Files) != 2 || got.Files[0] != want.Files[0] || got.Files[1] != want.Files[1] {
		t.Fatalf("sizes = %+v, want %+v", got, want)
	}

	hot.set(write("state-b", 70), write("run-b", 5))
	got = hot.sizes()
	if !got.OK || got.Files[0].Bytes != 70 || got.Files[1].Bytes != 5 {
		t.Fatalf("sizes after the rotation = %+v, want the new segment's files", got)
	}

	hot.set(filepath.Join(dir, "missing-state"), filepath.Join(dir, "run-b"))
	if got := hot.sizes(); got.OK || len(got.Files) != 0 {
		t.Fatalf("sizes with an unreadable file = %+v, want unknown", got)
	}
}

// The summary line a real store writes carries the real size of each hot file
// under the task store's key names.
func TestJSONLStoreSummaryReportsRealHotFileSizes(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStoreWithOptions(root, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var mu sync.Mutex
	var lines []string
	opts := txObserverOptions(store.txHot)
	opts.Logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	opts.SummaryInterval = time.Nanosecond
	store.txObserver = txobserve.New(opts)
	store.batch.SetTxObserver(store.txObserver.Observe)

	task := snapshotTestTask(time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC))
	task.Status = domaintask.StatusQueued
	task.StartedAt = nil
	task.DependencyTaskIDs = nil
	if err := store.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if err := store.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	state, err := os.Stat(filepath.Join(store.root, stateFilename))
	if err != nil {
		t.Fatal(err)
	}
	run, err := os.Stat(filepath.Join(store.root, runFilename))
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) == 0 {
		t.Fatal("no summary line was written")
	}
	last := lines[len(lines)-1]
	want := fmt.Sprintf(" hot_bytes=%d task_state_bytes=%d task_run_bytes=%d", state.Size()+run.Size(), state.Size(), run.Size())
	if !strings.HasPrefix(last, "[TaskStore] tx summary window=") || !strings.HasSuffix(last, want) {
		t.Fatalf("summary line = %q, want a [TaskStore] summary ending with%s", last, want)
	}
}
