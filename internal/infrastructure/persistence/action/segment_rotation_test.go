package action

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/segmentname"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Rotation (design 5.3, AC-3): writes go to the segment of the UTC day, a further
// segment of the same day once the current one outgrows the size valve, and never
// to a segment that was closed. A store that is not rotated behaves as it always
// has.

func openRotating(t testing.TB, root string, clock *rotClock, valve int64) *JSONLStore {
	t.Helper()
	store, err := NewJSONLStoreWithOptions(root, rotOptions(clock, valve, nil))
	if err != nil {
		t.Fatalf("open with rotation: %v", err)
	}
	return store
}

func TestFirstOpenWithRotationMovesThePlainStoreToSegments(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeAllWritable(root) })
	pairs := makePairs(t, 12, rotStart)
	plainStore(t, root, pairs, 8)
	before, err := segmentFileSizes(root)
	if err != nil {
		t.Fatal(err)
	}
	if before[fileAction] == 0 || before[fileAttempt] == 0 {
		t.Fatalf("the plain store is empty: %v", before)
	}
	seg0 := fingerprint(t, root)

	clock := newRotClock(rotStart.Add(time.Hour))
	store := openRotating(t, root, clock, 0)

	layout, err := readLayout(root)
	if err != nil {
		t.Fatalf("read LAYOUT: %v", err)
	}
	if !layout.v2 {
		t.Fatal("the first open with Rotation did not move the store to layout v2 (no LAYOUT)")
	}
	if layout.frozen != before {
		t.Fatalf("LAYOUT records segment 0 as %v, it was %v when it was frozen", layout.frozen, before)
	}
	if got := segmentNames(t, root); len(got) != 1 || got[0] != "20261013-01" {
		t.Fatalf("segments after the migration = %v, want [20261013-01]", got)
	}
	if runtime.GOOS != "windows" {
		for _, path := range segmentZeroFiles(root) {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0o200 != 0 {
				t.Fatalf("%s of segment 0 is still writable", filepath.Base(path))
			}
		}
	}
	requireHolds(t, store, pairs, 0, 8)

	writeAll(t, store, pairs, 8, 12)
	requireHolds(t, store, pairs, 0, 12)
	after := fingerprint(t, root)
	for _, name := range []string{actionFilename, attemptFilename} {
		if after[name] != seg0[name] {
			t.Fatalf("segment 0 file %s changed after the migration", name)
		}
	}
	segSizes, err := segmentFileSizes(segmentDir(root, "20261013-01"))
	if err != nil {
		t.Fatal(err)
	}
	if segSizes[fileAction] == 0 || segSizes[fileAttempt] == 0 {
		t.Fatalf("the new writes did not go to the segment: %v", segSizes)
	}

	controlRoot := t.TempDir()
	plainStore(t, controlRoot, pairs, 12)
	requireSameAnswers(t, openFull(t, controlRoot), store)
	requireSnapshotEqualsLog(t, store)
}

func TestWithoutRotationTheLayoutStaysPlain(t *testing.T) {
	root := t.TempDir()
	pairs := makePairs(t, 6, rotStart)
	store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	writeAll(t, store, pairs, 0, 6)
	for _, name := range []string{layoutFilename, logsDirName} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			t.Fatalf("%s exists although rotation was never switched on", name)
		}
	}
	if fileSize(t, filepath.Join(root, actionFilename)) == 0 {
		t.Fatal("the writes did not go to the plain files")
	}
}

func TestRotationRequiresTheSnapshot(t *testing.T) {
	if _, err := NewJSONLStoreWithOptions(t.TempDir(), OpenOptions{Rotation: true}); err == nil {
		t.Fatal("Rotation without Index opened; the segments are joined only by the snapshot")
	}
}

func TestANewUTCDayStartsANewSegmentAndSealsTheOldOne(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeAllWritable(root) })
	pairs := makePairs(t, 9, rotStart)
	clock := newRotClock(rotStart)
	store := openRotating(t, root, clock, 0)
	writeAll(t, store, pairs, 0, 3)

	clock.Set(time.Date(2026, 10, 14, 0, 0, 5, 0, time.UTC))
	writeAll(t, store, pairs, 3, 6)
	clock.Set(time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC))
	writeAll(t, store, pairs, 6, 9)

	want := []string{"20261013-01", "20261014-01", "20261015-01"}
	if got := segmentNames(t, root); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("segments = %v, want %v", got, want)
	}
	requireHolds(t, store, pairs, 0, 9)
	requireSnapshotEqualsLog(t, store)

	for _, name := range want[:2] {
		dir := segmentDir(root, name)
		record, ok, err := readSealed(dir)
		if err != nil || !ok {
			t.Fatalf("segment %s has no readable SEALED record (ok=%v err=%v)", name, ok, err)
		}
		actual, err := segmentFileSizes(dir)
		if err != nil {
			t.Fatal(err)
		}
		if record != actual {
			t.Fatalf("SEALED of %s records %v, the files are %v", name, record, actual)
		}
	}
	if _, ok, _ := readSealed(segmentDir(root, want[2])); ok {
		t.Fatal("the segment writes go to is already sealed")
	}
}

func TestTheSizeValveStartsTheNextSegmentOfTheSameDay(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeAllWritable(root) })
	const valve = 3000
	pairs := makePairs(t, 24, rotStart)
	clock := newRotClock(rotStart)
	store := openRotating(t, root, clock, valve)
	writeAll(t, store, pairs, 0, 24)

	names := segmentNames(t, root)
	if len(names) < 3 {
		t.Fatalf("%d segments after writing %d pairs with a %d byte valve: %v", len(names), len(pairs), valve, names)
	}
	for i, name := range names {
		if segmentname.Day(name) != "20261013" || segmentname.Sequence(name) != i+1 {
			t.Fatalf("segment %d is %q, want the day's sequence %d", i, name, i+1)
		}
	}
	for _, name := range names[:len(names)-1] {
		sz, err := segmentFileSizes(segmentDir(root, name))
		if err != nil {
			t.Fatal(err)
		}
		total := sz[fileAction] + sz[fileAttempt]
		if total <= valve || total > valve+2000 {
			t.Fatalf("closed segment %s holds %d bytes; the valve is %d and one transaction is under 2000", name, total, valve)
		}
	}
	requireHolds(t, store, pairs, 0, 24)
	requireSnapshotEqualsLog(t, store)
}

func TestAClosedSegmentIsNeverWrittenAgain(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeAllWritable(root) })
	pairs := makePairs(t, 40, rotStart)
	clock := newRotClock(rotStart)
	store := openRotating(t, root, clock, 2500)
	writeAll(t, store, pairs, 0, 20)
	closedBefore := segmentNames(t, root)
	if len(closedBefore) < 3 {
		t.Fatalf("only %d segments; the test needs two closed and one open: %v", len(closedBefore), closedBefore)
	}
	closedBefore = closedBefore[:len(closedBefore)-1]
	before := fingerprint(t, root)
	clock.Advance(26 * time.Hour)
	writeAll(t, store, pairs, 20, 40)

	after := fingerprint(t, root)
	closedPrefixes := []string{actionFilename, attemptFilename, layoutFilename}
	for _, name := range closedBefore {
		closedPrefixes = append(closedPrefixes, filepath.Join(logsDirName, name)+string(filepath.Separator))
	}
	checked := 0
	for path, sum := range before {
		for _, prefix := range closedPrefixes {
			if path == prefix || (strings.HasSuffix(prefix, string(filepath.Separator)) && strings.HasPrefix(path, prefix)) {
				checked++
				if after[path] != sum {
					t.Fatalf("%s belongs to a closed segment and changed", path)
				}
			}
		}
	}
	if checked < 3*len(closedBefore) {
		t.Fatalf("only %d files of the closed segments were compared", checked)
	}
	requireHolds(t, store, pairs, 0, 40)
}

func TestAClockThatGoesBackNeverReturnsToAnEarlierSegment(t *testing.T) {
	finish := captureLog(t)
	root := t.TempDir()
	t.Cleanup(func() { makeAllWritable(root) })
	pairs := makePairs(t, 12, rotStart)
	clock := newRotClock(rotStart)
	store := openRotating(t, root, clock, 0)
	writeAll(t, store, pairs, 0, 2)
	clock.Set(rotStart.Add(48 * time.Hour))
	writeAll(t, store, pairs, 2, 4)
	if got := segmentNames(t, root); len(got) != 2 || got[1] != "20261015-01" {
		t.Fatalf("segments = %v, want the second to be 20261015-01", got)
	}

	clock.Set(rotStart.Add(24 * time.Hour)) // one day behind the newest segment
	writeAll(t, store, pairs, 4, 6)
	writeAll(t, store, pairs, 6, 7) // an unchanged state says nothing more
	clock.Set(rotStart)             // further behind
	writeAll(t, store, pairs, 7, 9)
	clock.Set(rotStart.Add(48 * time.Hour)) // the day of the newest segment again: no longer behind
	writeAll(t, store, pairs, 9, 11)

	if got := segmentNames(t, root); len(got) != 2 {
		t.Fatalf("a clock that went back created or returned to a segment: %v", got)
	}
	requireHolds(t, store, pairs, 0, 11)
	text := finish()
	for reason, want := range map[string]int{"reason=clock_behind": 1, "reason=clock_further_behind": 1, "reason=clock_recovered": 1} {
		if got := strings.Count(text, reason); got != want {
			t.Errorf("%q appears %d times in the log, want %d", reason, got, want)
		}
	}
	if !strings.Contains(text, "WARN") {
		t.Error("a clock behind the newest segment was not warned about")
	}
}

func TestADayHasAtMost99SegmentsAndTheStoreKeepsWriting(t *testing.T) {
	finish := captureLog(t)
	root := t.TempDir()
	t.Cleanup(func() { makeAllWritable(root) })
	pairs := makePairs(t, 104, rotStart)
	clock := newRotClock(rotStart)
	store := openRotating(t, root, clock, 1) // every transaction outgrows the valve
	ctx := context.Background()
	for i := range pairs {
		if err := pairs[i].create(ctx, store); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	names := segmentNames(t, root)
	if len(names) != maxSegmentsPerDay {
		t.Fatalf("%d segments for one day, want %d", len(names), maxSegmentsPerDay)
	}
	if got := strings.Count(finish(), "already has 99 segments"); got != 1 {
		t.Fatalf("the limit was reported %d times, want once", got)
	}
	for i := range pairs {
		if _, err := store.GetAction(ctx, pairs[i].action.ActionID); err != nil {
			t.Fatalf("Action %d is gone: %v", i, err)
		}
	}
}

func TestRotationSwitchedOffOnASegmentedStoreKeepsWritingToTheNewestSegment(t *testing.T) {
	finish := captureLog(t)
	root := t.TempDir()
	t.Cleanup(func() { makeAllWritable(root) })
	pairs := makePairs(t, 8, rotStart)
	clock := newRotClock(rotStart)
	store := openRotating(t, root, clock, 0)
	writeAll(t, store, pairs, 0, 3)

	off, err := NewJSONLStoreWithOptions(root, OpenOptions{now: clock.Now})
	if err != nil {
		t.Fatalf("a store on segments must open without the flags: %v", err)
	}
	clock.Advance(48 * time.Hour)
	writeAll(t, off, pairs, 3, 6)
	if got := segmentNames(t, root); len(got) != 1 {
		t.Fatalf("segments = %v; with rotation off the store stays in its newest segment", got)
	}
	requireHolds(t, off, pairs, 0, 6)
	if !strings.Contains(finish(), "rotation is off") {
		t.Error("a segmented store opened with rotation off did not say that its commit time grows")
	}
}

func TestFreshWorkspaceWithRotationStartsOnSegments(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state", "actions")
	t.Cleanup(func() { makeAllWritable(root) })
	pairs := makePairs(t, 4, rotStart)
	clock := newRotClock(rotStart)
	store := openRotating(t, root, clock, 0)
	writeAll(t, store, pairs, 0, 4)
	layout, err := readLayout(root)
	if err != nil || !layout.v2 {
		t.Fatalf("an empty store opened with rotation is not on layout v2 (err=%v)", err)
	}
	requireHolds(t, store, pairs, 0, 4)
	if _, err := store.GetAction(context.Background(), modulecore.NewActionID()); err == nil {
		t.Fatal("an unknown Action was found")
	} else if err != domainaction.ErrNotFound {
		t.Fatalf("unknown Action: %v", err)
	}
}
