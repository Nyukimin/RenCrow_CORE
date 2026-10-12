package task

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Segment rotation and layout v2 (spec 5.3, 10.3, 14.1 R-5, R-7, R-8, R-10).

// testClock is the store's clock in these tests: a segment is named after the
// UTC day of the write that starts it, so the tests move days by hand.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock(rfc3339 string) *testClock {
	at, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		panic(err)
	}
	return &testClock{now: at}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *testClock) Set(rfc3339 string) {
	at, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		panic(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = at
}

func rotationOptions(clock *testClock, valve int64, failpoint func(string)) OpenOptions {
	return OpenOptions{Index: true, Persist: true, Rotation: true, RotationSizeValve: valve, checkpoint: manualPolicy(), failpoint: failpoint, now: clock.Now}
}

func openRotating(t testing.TB, root string, clock *testClock, valve int64, failpoint func(string)) *JSONLStore {
	t.Helper()
	store, err := NewJSONLStoreWithOptions(root, rotationOptions(clock, valve, failpoint))
	if err != nil {
		t.Fatalf("open store with rotation: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func segmentNames(t testing.TB, root string) []string {
	t.Helper()
	names, err := listSegmentDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// fileFingerprint is what "the file was not touched" means: bytes, size, mtime.
type fileFingerprint struct {
	size  int64
	mtime time.Time
	sum   [32]byte
}

func fingerprintDir(t testing.TB, dir string) map[string]fileFingerprint {
	t.Helper()
	out := map[string]fileFingerprint{}
	for _, name := range sizedFilenames {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = fileFingerprint{size: info.Size(), mtime: info.ModTime(), sum: sha256.Sum256(data)}
	}
	return out
}

func requireFingerprintsEqual(t testing.TB, what string, before, after map[string]fileFingerprint) {
	t.Helper()
	for name, b := range before {
		a, ok := after[name]
		if !ok || a != b {
			t.Fatalf("%s: %s changed (size %d -> %d, mtime %v -> %v)", what, name, b.size, a.size, b.mtime, a.mtime)
		}
	}
}

// requireSegmentedStoreEqualsFullBuild is requireStoreEqualsFullBuild for a
// store with segments: the index the store holds is the one a build from all
// the segments of the log gives, and every ID the log holds can be fetched.
func requireSegmentedStoreEqualsFullBuild(t testing.TB, store *JSONLStore, root string) {
	t.Helper()
	built, err := buildTaskIndex(root)
	if err != nil {
		t.Fatalf("build from the log: %v", err)
	}
	defer built.close()
	if err := equivalentIndex(store.idx, built); err != nil {
		t.Fatalf("the store's index differs from a build from the log: %v", err)
	}
	facts := scanSegmentedLog(t, root)
	ctx := context.Background()
	for id := range facts.tasks {
		if _, err := store.GetTask(ctx, id); err != nil {
			t.Fatalf("GetTask(%s) for an ID in the log: %v", id, err)
		}
	}
	for id := range facts.runs {
		if _, err := store.GetRun(ctx, id); err != nil {
			t.Fatalf("GetRun(%s) for an ID in the log: %v", id, err)
		}
	}
	stats, _ := store.IndexStats()
	if stats.Tasks != len(facts.tasks) || stats.Runs != len(facts.runs) {
		t.Fatalf("index has %d tasks and %d runs, the log has %d and %d", stats.Tasks, stats.Runs, len(facts.tasks), len(facts.runs))
	}
}

// scanSegmentedLog is scanLog over segment 0 and every segment under logs/.
func scanSegmentedLog(t testing.TB, root string) logFacts {
	t.Helper()
	total := logFacts{
		lines: map[fileKind]int64{}, bytes: map[fileKind]int64{},
		tasks: map[modulecore.TaskID]struct{}{}, runs: map[modulecore.RunID]struct{}{}, ctxs: map[modulecore.TaskID]struct{}{},
	}
	dirs := []string{root}
	for _, name := range segmentNames(t, root) {
		dirs = append(dirs, segmentDir(root, name))
	}
	for _, dir := range dirs {
		part := scanLog(t, dir)
		for kind, n := range part.lines {
			total.lines[kind] += n
		}
		for kind, n := range part.bytes {
			total.bytes[kind] += n
		}
		for id := range part.tasks {
			total.tasks[id] = struct{}{}
		}
		for id := range part.runs {
			total.runs[id] = struct{}{}
		}
		for id := range part.ctxs {
			total.ctxs[id] = struct{}{}
		}
	}
	return total
}

func fileMode(t testing.TB, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// stageOneStore writes a layout v1 store of n populated Tasks and closes it.
func stageOneStore(t testing.TB, root string, n int) {
	t.Helper()
	store := openPersisted(t, root, manualPolicy(), nil)
	populate(t, store, 0, n)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEnablingRotationFreezesSegmentZeroAndMovesWritesToTheFirstSegment(t *testing.T) {
	root := t.TempDir()
	clock := newTestClock("2026-10-13T01:00:00Z")
	stageOneStore(t, root, 12)
	before := fingerprintDir(t, root)

	store := openRotating(t, root, clock, 0, nil)

	layout, err := readLayout(root)
	if err != nil || !layout.v2 {
		t.Fatalf("LAYOUT after enabling rotation: %+v, %v", layout, err)
	}
	for i, name := range sizedFilenames {
		if layout.frozen[i] != before[name].size {
			t.Fatalf("LAYOUT records %s as %d bytes, the file held %d when it was frozen", name, layout.frozen[i], before[name].size)
		}
	}
	if got := segmentNames(t, root); len(got) != 1 || got[0] != "20261013-01" {
		t.Fatalf("segments after enabling rotation = %v, want [20261013-01]", got)
	}
	for _, name := range sizedFilenames {
		if mode := fileMode(t, filepath.Join(root, name)); mode&0o222 != 0 {
			t.Fatalf("segment 0 file %s is %o, want read-only", name, mode)
		}
	}
	if mode := fileMode(t, filepath.Join(root, ".jsonlbatch.wal")); mode&0o222 != 0 {
		t.Fatalf("segment 0 WAL is %o, want read-only", mode)
	}
	if mode := fileMode(t, filepath.Join(root, ".jsonlbatch.lock")); mode&0o200 == 0 {
		t.Fatalf("the lock file is %o: every opener needs it writable", mode)
	}
	requireFingerprintsEqual(t, "segment 0 after the switch", before, fingerprintDir(t, root))

	ctx := context.Background()
	for n := 0; n < 12; n++ {
		if _, err := store.GetTask(ctx, indexedTestTask(n, indexTestNow).TaskID); err != nil {
			t.Fatalf("task %d written before the switch cannot be read: %v", n, err)
		}
	}
	populate(t, store, 12, 15)
	requireFingerprintsEqual(t, "segment 0 after writes", before, fingerprintDir(t, root))
	first := fingerprintDir(t, segmentDir(root, "20261013-01"))
	if first[stateFilename].size == 0 {
		t.Fatal("writes after the switch did not reach the first segment")
	}
	stats, _ := store.IndexStats()
	if stats.Segments != 2 || stats.Segment != "20261013-01" {
		t.Fatalf("index covers %d segments, current %q; want 2 and 20261013-01", stats.Segments, stats.Segment)
	}
	requireSegmentedStoreEqualsFullBuild(t, store, root)
}

func TestADayChangeStartsANewSegmentAndNeverTouchesClosedSegments(t *testing.T) {
	root := t.TempDir()
	clock := newTestClock("2026-10-13T23:00:00Z")
	stageOneStore(t, root, 6)
	store := openRotating(t, root, clock, 0, nil)
	populate(t, store, 6, 12)
	day1 := fingerprintDir(t, segmentDir(root, "20261013-01"))
	seg0 := fingerprintDir(t, root)

	clock.Set("2026-10-14T00:00:05Z")
	populate(t, store, 12, 18)
	if got := segmentNames(t, root); len(got) != 2 || got[1] != "20261014-01" {
		t.Fatalf("segments after the day changed = %v, want a second one named 20261014-01", got)
	}
	requireFingerprintsEqual(t, "the closed day segment", day1, fingerprintDir(t, segmentDir(root, "20261013-01")))
	requireFingerprintsEqual(t, "segment 0", seg0, fingerprintDir(t, root))
	if fingerprintDir(t, segmentDir(root, "20261014-01"))[stateFilename].size == 0 {
		t.Fatal("writes after the day changed did not reach the new segment")
	}

	// The same day again: no further segment.
	clock.Advance(time.Hour)
	populate(t, store, 18, 20)
	if got := segmentNames(t, root); len(got) != 2 {
		t.Fatalf("segments within one day = %v, want 2", got)
	}
	requireSegmentedStoreEqualsFullBuild(t, store, root)
}

func TestTheSizeValveStartsAnotherSegmentOfTheSameDay(t *testing.T) {
	root := t.TempDir()
	clock := newTestClock("2026-10-13T01:00:00Z")
	stageOneStore(t, root, 3)
	const valve = 6 << 10
	store := openRotating(t, root, clock, valve, nil)
	populate(t, store, 3, 80)
	names := segmentNames(t, root)
	if len(names) < 3 {
		t.Fatalf("segments with a %d byte valve = %v, want several", valve, names)
	}
	for i, name := range names {
		if !strings.HasPrefix(name, "20261013-") {
			t.Fatalf("segment %d is %s, want the day of the clock", i, name)
		}
	}
	// Every closed segment stopped growing soon after the valve (one transaction
	// may overshoot it, not more).
	for _, name := range names[:len(names)-1] {
		var total int64
		for _, fp := range fingerprintDir(t, segmentDir(root, name)) {
			total += fp.size
		}
		if total < valve || total > valve*3 {
			t.Fatalf("closed segment %s holds %d bytes, want just over the %d byte valve", name, total, valve)
		}
	}
	requireSegmentedStoreEqualsFullBuild(t, store, root)
}

func TestAClockThatRunsBackwardsNeverWritesToAnEarlierSegment(t *testing.T) {
	root := t.TempDir()
	clock := newTestClock("2026-10-14T10:00:00Z")
	stageOneStore(t, root, 2)
	store := openRotating(t, root, clock, 0, nil)
	populate(t, store, 2, 5)

	clock.Set("2026-10-12T10:00:00Z")
	populate(t, store, 5, 8)
	if got := segmentNames(t, root); len(got) != 1 || got[0] != "20261014-01" {
		t.Fatalf("segments after the clock went back = %v, want only 20261014-01", got)
	}
	requireSegmentedStoreEqualsFullBuild(t, store, root)
}

func TestATaskKeepsItsWholeHistoryAcrossSegments(t *testing.T) {
	root := t.TempDir()
	clock := newTestClock("2026-10-13T08:00:00Z")
	stageOneStore(t, root, 2)
	store := openRotating(t, root, clock, 0, nil)
	ctx := context.Background()
	generation, err := store.WriterGeneration()
	if err != nil {
		t.Fatal(err)
	}

	task := indexedTestTask(500, indexTestNow)
	if err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	run := indexedTestRun(task, 500, generation, indexTestNow)
	if err := store.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContext(ctx, domaintask.SharedRoleContext{TaskID: task.TaskID, CurrentPlan: "day one", UpdatedAt: indexTestNow}); err != nil {
		t.Fatal(err)
	}

	clock.Set("2026-10-14T08:00:00Z")
	task.Status, task.UpdatedAt = domaintask.StatusRunning, indexTestNow.Add(time.Hour)
	if err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNotification(ctx, domaintask.NewNotification(task, indexTestNow.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}

	clock.Set("2026-10-15T08:00:00Z")
	done := run
	completed := indexTestNow.Add(2 * time.Hour)
	done.Status, done.CompletedAt, done.Summary = domaintask.RunStatusSucceeded, &completed, "finished"
	if err := store.SaveRun(ctx, done); err != nil {
		t.Fatal(err)
	}
	task.Status, task.UpdatedAt = domaintask.StatusSucceeded, indexTestNow.Add(2*time.Hour)
	finished := completed
	task.FinishedAt = &finished
	if err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContext(ctx, domaintask.SharedRoleContext{TaskID: task.TaskID, CurrentPlan: "day three", UpdatedAt: completed}); err != nil {
		t.Fatal(err)
	}

	if got := segmentNames(t, root); len(got) != 3 {
		t.Fatalf("segments = %v, want one per day", got)
	}
	latest, err := store.GetTask(ctx, task.TaskID)
	if err != nil || latest.Status != domaintask.StatusSucceeded {
		t.Fatalf("latest version of the task: %+v, %v", latest, err)
	}
	gotRun, err := store.GetRun(ctx, run.RunID)
	if err != nil || gotRun.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("latest version of the run: %+v, %v", gotRun, err)
	}
	gotCtx, err := store.GetContext(ctx, task.TaskID)
	if err != nil || gotCtx.CurrentPlan != "day three" {
		t.Fatalf("latest context: %+v, %v", gotCtx, err)
	}
	history, err := store.TaskHistory(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	segments := map[string]bool{}
	for _, line := range history {
		counts[line.File]++
		segments[line.Segment] = true
	}
	want := map[string]int{stateFilename: 3, runFilename: 2, contextFilename: 2, notificationsFilename: 1}
	for file, n := range want {
		if counts[file] != n {
			t.Fatalf("history holds %d lines of %s, want %d (%v)", counts[file], file, n, counts)
		}
	}
	if len(segments) != 3 {
		t.Fatalf("the history spans segments %v, want 3", segments)
	}
	requireSegmentedStoreEqualsFullBuild(t, store, root)
}

func TestRestartAfterRotationRestoresTheIndexOfAFullBuild(t *testing.T) {
	for _, tc := range []struct {
		name      string
		lastClose func(t *testing.T, s *JSONLStore)
	}{
		{"clean close", func(t *testing.T, s *JSONLStore) {
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
		}},
		{"crash without a final checkpoint", func(t *testing.T, s *JSONLStore) {
			if err := s.closeStore(false); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			clock := newTestClock("2026-10-13T08:00:00Z")
			stageOneStore(t, root, 5)
			store := openRotating(t, root, clock, 0, nil)
			populate(t, store, 5, 9)
			clock.Set("2026-10-14T08:00:00Z")
			populate(t, store, 9, 12)
			if err := store.idx.ck.checkpointNow("test"); err != nil {
				t.Fatal(err)
			}
			clock.Set("2026-10-15T08:00:00Z")
			populate(t, store, 12, 16) // after the last checkpoint, across a rotation
			tc.lastClose(t, store)

			again := openRotating(t, root, clock, 0, nil)
			requireSegmentedStoreEqualsFullBuild(t, again, root)
			stats, _ := again.IndexStats()
			if stats.Segments != 4 {
				t.Fatalf("restored index covers %d segments, want 4", stats.Segments)
			}
			populate(t, again, 16, 18)
			requireSegmentedStoreEqualsFullBuild(t, again, root)
		})
	}
}

func TestRotationEnabledOnTopOfAnExistingV2StoreDoesNotMigrateAgain(t *testing.T) {
	root := t.TempDir()
	clock := newTestClock("2026-10-13T08:00:00Z")
	stageOneStore(t, root, 4)
	first := openRotating(t, root, clock, 0, nil)
	populate(t, first, 4, 6)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	layoutBefore, err := os.ReadFile(layoutPath(root))
	if err != nil {
		t.Fatal(err)
	}
	second := openRotating(t, root, clock, 0, nil)
	populate(t, second, 6, 8)
	layoutAfter, _ := os.ReadFile(layoutPath(root))
	if !bytes.Equal(layoutBefore, layoutAfter) {
		t.Fatal("LAYOUT was rewritten by a reopen")
	}
	if got := segmentNames(t, root); len(got) != 1 {
		t.Fatalf("segments after a reopen on the same day = %v", got)
	}
	requireSegmentedStoreEqualsFullBuild(t, second, root)
}

func TestWithoutRotationAV2StoreKeepsWritingToItsNewestSegment(t *testing.T) {
	root := t.TempDir()
	clock := newTestClock("2026-10-13T08:00:00Z")
	stageOneStore(t, root, 4)
	first := openRotating(t, root, clock, 0, nil)
	populate(t, first, 4, 6)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	// R0: rotation switched off again. The layout stays v2; the newest segment
	// keeps being written, and a new day does not start another one.
	clock.Set("2026-10-20T08:00:00Z")
	store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true, Persist: true, checkpoint: manualPolicy(), now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	populate(t, store, 6, 9)
	if got := segmentNames(t, root); len(got) != 1 || got[0] != "20261013-01" {
		t.Fatalf("segments with rotation off = %v, want only the one that existed", got)
	}
	requireSegmentedStoreEqualsFullBuild(t, store, root)
}

func TestAnIndexlessOpenOfAV2StoreStillGetsTheIndex(t *testing.T) {
	root := t.TempDir()
	clock := newTestClock("2026-10-13T08:00:00Z")
	stageOneStore(t, root, 4)
	first := openRotating(t, root, clock, 0, nil)
	populate(t, first, 4, 6)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := NewJSONLStoreWithOptions(root, OpenOptions{})
	if err != nil {
		t.Fatalf("open of a v2 store without Index: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, ok := store.IndexStats(); !ok {
		t.Fatal("a store on layout v2 cannot be read without the index, yet it opened without one")
	}
	if _, err := store.GetTask(context.Background(), indexedTestTask(1, indexTestNow).TaskID); err != nil {
		t.Fatalf("task in segment 0: %v", err)
	}
}

func TestAChangedSealedSegmentOrAnUnknownLayoutIsRefused(t *testing.T) {
	build := func(t *testing.T) string {
		root := t.TempDir()
		clock := newTestClock("2026-10-13T08:00:00Z")
		stageOneStore(t, root, 5)
		store := openRotating(t, root, clock, 0, nil)
		populate(t, store, 5, 8)
		clock.Set("2026-10-14T08:00:00Z")
		populate(t, store, 8, 11)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		return root
	}
	appendTo := func(t *testing.T, path string, extra string) {
		t.Helper()
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(extra); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name   string
		mutate func(t *testing.T, root string)
		want   error
	}{
		{"segment 0 grew (an older binary wrote to it)", func(t *testing.T, root string) {
			appendTo(t, filepath.Join(root, stateFilename), "\n")
		}, ErrSealedSegmentChanged},
		{"segment 0 shrank", func(t *testing.T, root string) {
			path := filepath.Join(root, runFilename)
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			info, _ := os.Stat(path)
			if err := os.Truncate(path, info.Size()-1); err != nil {
				t.Fatal(err)
			}
		}, ErrSealedSegmentChanged},
		{"a closed segment grew", func(t *testing.T, root string) {
			appendTo(t, filepath.Join(segmentDir(root, "20261013-01"), notificationsFilename), "\n")
		}, ErrSealedSegmentChanged},
		{"LAYOUT names a version this build does not know", func(t *testing.T, root string) {
			if err := os.WriteFile(layoutPath(root), []byte("tasks-layout/v3\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, ErrUnknownLayout},
		{"LAYOUT is cut short", func(t *testing.T, root string) {
			data, _ := os.ReadFile(layoutPath(root))
			if err := os.WriteFile(layoutPath(root), data[:len(data)/2], 0o644); err != nil {
				t.Fatal(err)
			}
		}, ErrUnknownLayout},
		{"a closed segment directory is gone", func(t *testing.T, root string) {
			if err := os.Rename(segmentDir(root, "20261013-01"), filepath.Join(root, "moved")); err != nil {
				t.Fatal(err)
			}
		}, ErrSegmentMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := build(t)
			tc.mutate(t, root)
			clock := newTestClock("2026-10-14T09:00:00Z")
			store, err := NewJSONLStoreWithOptions(root, rotationOptions(clock, 0, nil))
			if err == nil {
				_ = store.Close()
				t.Fatal("the store opened although its history is not what was recorded")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("open error = %v, want one wrapping %v", err, tc.want)
			}
			if reader, err := NewJSONLReader(root); err == nil {
				_ = reader.Close()
				t.Fatal("a reader opened a store whose history is not what was recorded")
			} else if !errors.Is(err, tc.want) {
				t.Fatalf("reader error = %v, want one wrapping %v", err, tc.want)
			}
		})
	}
}

// An older binary opens the store with jsonlbatch.New on the root: it must fail
// before it can write (spec 10.3, U7). The store a v2 build leaves behind is
// used as is.
func TestAnOlderBinaryCannotOpenAFrozenSegmentZero(t *testing.T) {
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		t.Skip("a file's read-only mode does not stop root; the size check is the defense there")
	}
	root := t.TempDir()
	clock := newTestClock("2026-10-13T08:00:00Z")
	stageOneStore(t, root, 5)
	store := openRotating(t, root, clock, 0, nil)
	populate(t, store, 5, 7)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before := fingerprintDir(t, root)
	walBefore, _ := os.ReadFile(filepath.Join(root, ".jsonlbatch.wal"))

	_, err := jsonlbatch.New(root, taskBatchFilenames())
	if err == nil {
		t.Fatal("jsonlbatch.New succeeded on a frozen segment 0: an older binary would go on to write to it")
	}
	if _, oldErr := NewJSONLStoreWithOptions(t.TempDir(), OpenOptions{}); oldErr != nil {
		t.Fatalf("control: opening an ordinary store failed: %v", oldErr)
	}
	requireFingerprintsEqual(t, "segment 0 after an older binary tried to open it", before, fingerprintDir(t, root))
	walAfter, _ := os.ReadFile(filepath.Join(root, ".jsonlbatch.wal"))
	if !bytes.Equal(walBefore, walAfter) {
		t.Fatal("the WAL of segment 0 changed")
	}
}

func TestWritesDuringARotationLoseNothingAndKeepTheirOrder(t *testing.T) {
	root := t.TempDir()
	clock := newTestClock("2026-10-13T23:59:00Z")
	stageOneStore(t, root, 2)
	store := openRotating(t, root, clock, 0, nil)
	ctx := context.Background()
	const writers, perWriter = 8, 12
	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if w == 0 && i == perWriter/2 {
					clock.Set("2026-10-14T00:00:30Z") // the day changes under the other writers
				}
				n := 1000 + w*perWriter + i
				if err := store.SaveTask(ctx, indexedTestTask(n, indexTestNow.Add(time.Duration(n)*time.Second))); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("a write failed during the rotation: %v", err)
	}
	if got := segmentNames(t, root); len(got) != 2 {
		t.Fatalf("segments = %v, want the original and one for the new day", got)
	}
	for n := 1000; n < 1000+writers*perWriter; n++ {
		if _, err := store.GetTask(ctx, indexedTestTask(n, indexTestNow).TaskID); err != nil {
			t.Fatalf("task %d written across the rotation is gone: %v", n, err)
		}
	}
	requireSegmentedStoreEqualsFullBuild(t, store, root)
}
