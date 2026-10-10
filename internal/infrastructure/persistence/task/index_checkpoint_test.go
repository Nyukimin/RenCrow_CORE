package task

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// manualPolicy never starts the checkpoint goroutine and never triggers by
// itself: the test calls checkpointNow when it wants a sidecar.
func manualPolicy() *checkpointPolicy {
	return &checkpointPolicy{interval: time.Hour, lineThreshold: 1 << 40, retryBase: time.Hour, retryMax: time.Hour, manual: true}
}

func openPersisted(t testing.TB, root string, policy *checkpointPolicy, failpoint func(string)) *JSONLStore {
	t.Helper()
	store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true, Persist: true, checkpoint: policy, failpoint: failpoint})
	if err != nil {
		t.Fatalf("open persisted store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func sidecarFile(root string) string { return filepath.Join(root, sidecarDirName, sidecarLegacyFile) }

func readSidecar(t testing.TB, root string) (*taskIndex, sidecarHeader) {
	t.Helper()
	data, err := os.ReadFile(sidecarFile(root))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	ix, header, err := decodeSidecar(data)
	if err != nil {
		t.Fatalf("decode sidecar: %v", err)
	}
	return ix, header
}

func saveTasks(t testing.TB, store *JSONLStore, from, to int) {
	t.Helper()
	for n := from; n < to; n++ {
		if err := store.SaveTask(context.Background(), indexedTestTask(n, indexTestNow.Add(time.Duration(n)*time.Second))); err != nil {
			t.Fatalf("save task %d: %v", n, err)
		}
	}
}

// requireSidecarEqualsLog is the claim the whole feature rests on: whatever the
// sidecar says, it is what a full build from the log says (the sidecar covers
// the whole log, so no tail replay is involved).
func requireSidecarEqualsLog(t testing.TB, root string) {
	t.Helper()
	fromSidecar, _ := readSidecar(t, root)
	rebuilt, err := buildTaskIndex(root)
	if err != nil {
		t.Fatalf("rebuild from the log: %v", err)
	}
	defer rebuilt.close()
	if err := equivalentIndex(fromSidecar, rebuilt); err != nil {
		t.Fatalf("sidecar differs from the index built from the log: %v", err)
	}
}

func TestPersistRequiresIndex(t *testing.T) {
	store, err := NewJSONLStoreWithOptions(t.TempDir(), OpenOptions{Persist: true})
	if err == nil {
		_ = store.Close()
		t.Fatal("Persist without Index was accepted")
	}
}

func TestPersistedStoreWritesASidecarAtOpenAndAFinalOneAtClose(t *testing.T) {
	root := t.TempDir()
	store := openPersisted(t, root, manualPolicy(), nil)
	if _, header := readSidecar(t, root); header.Lines != [kindCount]int64{} {
		t.Fatalf("sidecar written at open covers lines %v of an empty log", header.Lines)
	}
	saveTasks(t, store, 0, 3)
	if _, header := readSidecar(t, root); header.Lines[kindState] != 0 {
		t.Fatalf("no checkpoint was due, yet the sidecar already covers %d state lines", header.Lines[kindState])
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, header := readSidecar(t, root); header.Lines[kindState] != 3 {
		t.Fatalf("final checkpoint covers %d state lines, want 3", header.Lines[kindState])
	}
	requireSidecarEqualsLog(t, root)
	if _, err := os.Stat(sidecarFile(root) + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file is left behind: %v", err)
	}
}

func TestCheckpointLoopWritesOnLineThresholdAndOnInterval(t *testing.T) {
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	t.Run("line threshold", func(t *testing.T) {
		root := t.TempDir()
		policy := &checkpointPolicy{interval: time.Hour, lineThreshold: 5, retryBase: time.Hour, retryMax: time.Hour}
		store := openPersisted(t, root, policy, nil)
		before, _ := store.IndexStats()
		saveTasks(t, store, 0, 8)
		waitFor("a checkpoint after 5 new lines", func() bool {
			stats, _ := store.IndexStats()
			return stats.Checkpoints > before.Checkpoints
		})
		if _, header := readSidecar(t, root); header.Lines[kindState] < 5 {
			t.Fatalf("threshold checkpoint covers %d state lines, want at least 5", header.Lines[kindState])
		}
	})

	t.Run("interval and idle", func(t *testing.T) {
		root := t.TempDir()
		policy := &checkpointPolicy{interval: 30 * time.Millisecond, lineThreshold: 1 << 40, retryBase: time.Hour, retryMax: time.Hour}
		store := openPersisted(t, root, policy, nil)
		baseline, _ := store.IndexStats()
		saveTasks(t, store, 0, 1)
		waitFor("the interval checkpoint to cover the new line", func() bool {
			stats, _ := store.IndexStats()
			return stats.Checkpoints > baseline.Checkpoints
		})
		if _, header := readSidecar(t, root); header.Lines[kindState] != 1 {
			t.Fatalf("interval checkpoint covers %d state lines, want 1", header.Lines[kindState])
		}
		settled, _ := store.IndexStats()
		time.Sleep(200 * time.Millisecond)
		if after, _ := store.IndexStats(); after.Checkpoints != settled.Checkpoints {
			t.Fatalf("idle store rewrote its sidecar %d times", after.Checkpoints-settled.Checkpoints)
		}
	})
}

func TestCheckpointFailureNeverStopsWrites(t *testing.T) {
	root := t.TempDir()
	// A regular file where the index directory belongs: every checkpoint fails.
	if err := os.WriteFile(filepath.Join(root, sidecarDirName), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := openPersisted(t, root, manualPolicy(), nil)
	saveTasks(t, store, 0, 3)
	if err := store.idx.ck.checkpointNow("test"); err == nil {
		t.Fatal("checkpoint into an unwritable location succeeded")
	}
	stats, _ := store.IndexStats()
	if stats.CheckpointFailures < 2 {
		t.Fatalf("consecutive failures = %d, want the open-time and the explicit one", stats.CheckpointFailures)
	}
	saveTasks(t, store, 3, 5) // the failure must not reach the write path
	requireIndexCoversLog(t, store, root)

	if err := os.Remove(filepath.Join(root, sidecarDirName)); err != nil {
		t.Fatal(err)
	}
	if err := store.idx.ck.checkpointNow("test"); err != nil {
		t.Fatalf("checkpoint after the obstacle is gone: %v", err)
	}
	if stats, _ := store.IndexStats(); stats.CheckpointFailures != 0 {
		t.Fatalf("failures not reset after a success: %d", stats.CheckpointFailures)
	}
	requireSidecarEqualsLog(t, root)
}

// Windows refuses to replace a file another process holds open (an indexer, a
// virus scanner, a CLI reader). The replacement is retried a bounded number of
// times; when it still fails the old sidecar stays in place and nothing is
// lost, because the sidecar is only a shortcut: the next open replays the log
// beyond the old sidecar, or rebuilds, and gets the same index.
func TestSidecarReplacementRetriesAndFailureIsHarmless(t *testing.T) {
	root := t.TempDir()
	store := openPersisted(t, root, manualPolicy(), nil)
	saveTasks(t, store, 0, 2)
	if err := store.idx.ck.checkpointNow("test"); err != nil {
		t.Fatal(err)
	}
	oldBytes, err := os.ReadFile(sidecarFile(root))
	if err != nil {
		t.Fatal(err)
	}
	ck := store.idx.ck
	ck.renameBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}
	realRename := ck.rename

	attempts := 0
	ck.rename = func(oldpath, newpath string) error {
		attempts++
		if attempts < 4 {
			return errors.New("the process cannot access the file because it is being used by another process")
		}
		return realRename(oldpath, newpath)
	}
	saveTasks(t, store, 2, 4)
	if err := ck.checkpointNow("test"); err != nil {
		t.Fatalf("replacement that succeeds on the 4th attempt failed: %v", err)
	}
	if attempts != 4 {
		t.Fatalf("rename attempts = %d, want 4", attempts)
	}
	if _, header := readSidecar(t, root); header.Lines[kindState] != 4 {
		t.Fatalf("sidecar covers %d state lines after the retried replacement, want 4", header.Lines[kindState])
	}

	attempts = 0
	ck.rename = func(string, string) error { attempts++; return errors.New("sharing violation") }
	before, _ := os.ReadFile(sidecarFile(root))
	saveTasks(t, store, 4, 6)
	if err := ck.checkpointNow("test"); err == nil {
		t.Fatal("a replacement that never succeeds reported success")
	}
	if attempts != len(ck.renameBackoff)+1 {
		t.Fatalf("rename was tried %d times, want a bound of %d", attempts, len(ck.renameBackoff)+1)
	}
	after, err := os.ReadFile(sidecarFile(root))
	if err != nil || string(after) != string(before) {
		t.Fatalf("the old sidecar changed although the replacement failed (err=%v)", err)
	}
	if _, err := os.Stat(sidecarFile(root) + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file of the failed replacement is left behind: %v", err)
	}
	saveTasks(t, store, 6, 7) // writes go on
	requireIndexCoversLog(t, store, root)

	ck.rename = realRename
	if err := ck.checkpointNow("test"); err != nil {
		t.Fatalf("the next checkpoint does not recover: %v", err)
	}
	requireSidecarEqualsLog(t, root)
	_ = oldBytes
}

func TestCloseStopsTheCheckpointLoopAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	policy := &checkpointPolicy{interval: 5 * time.Millisecond, lineThreshold: 2, retryBase: time.Hour, retryMax: time.Hour}
	store := openPersisted(t, root, policy, nil)
	saveTasks(t, store, 0, 6)
	ck := store.idx.ck
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ck.done:
	default:
		t.Fatal("checkpoint goroutine still running after Close")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	ck.stopAndFlush() // stopping again must be harmless
	before, _ := os.ReadFile(sidecarFile(root))
	time.Sleep(50 * time.Millisecond)
	after, _ := os.ReadFile(sidecarFile(root))
	if string(before) != string(after) {
		t.Fatal("sidecar changed after Close")
	}
	requireSidecarEqualsLog(t, root)
}

func TestCheckpointDoesNotLeakGoroutinesAcrossOpenAndClose(t *testing.T) {
	root := t.TempDir()
	runtime.GC()
	before := runtime.NumGoroutine()
	for i := 0; i < 10; i++ {
		store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true, Persist: true})
		if err != nil {
			t.Fatal(err)
		}
		saveTasks(t, store, i, i+1)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutines grew from %d to %d across 10 open/close cycles", before, after)
	}
}

// Run with -race: commits keep merging into the index while checkpoints snapshot
// it, from several goroutines at once.
func TestCheckpointRacesSafelyWithCommits(t *testing.T) {
	root := t.TempDir()
	policy := &checkpointPolicy{interval: 2 * time.Millisecond, lineThreshold: 3, retryBase: time.Millisecond, retryMax: 5 * time.Millisecond}
	store := openPersisted(t, root, policy, nil)
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for n := 0; n < 25; n++ {
				id := w*100 + n
				task := indexedTestTask(id, indexTestNow.Add(time.Duration(id)*time.Second))
				if err := store.SaveTask(context.Background(), task); err != nil {
					t.Errorf("save: %v", err)
					return
				}
				run := indexedTestRun(task, id, 1, indexTestNow)
				_ = run
				task.Status = domaintask.StatusRunning
				task.UpdatedAt = task.UpdatedAt.Add(time.Second)
				if err := store.SaveTask(context.Background(), task); err != nil {
					t.Errorf("update: %v", err)
					return
				}
			}
		}(w)
	}
	for c := 0; c < 3; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 20; n++ {
				if err := store.idx.ck.checkpointNow("race"); err != nil {
					t.Errorf("checkpoint: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	requireIndexCoversLog(t, store, root)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	requireSidecarEqualsLog(t, root)
}

func TestStaleTemporaryFileIsNotMistakenForASidecar(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, sidecarDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := sidecarFile(root) + ".tmp"
	if err := os.WriteFile(stale, []byte("RTSI half a write from a crashed checkpoint"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := openPersisted(t, root, manualPolicy(), nil)
	saveTasks(t, store, 0, 2)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale temporary file survived: %v", err)
	}
	requireSidecarEqualsLog(t, root)
}
