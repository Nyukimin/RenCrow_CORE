package action

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Health is what /health says about the store (design IU-6). It is counts, sizes,
// segment names and a fixed reason, read without the OS lock and without writing;
// it never carries an ID or the content of a record.

func TestHealthOfAnIndexedStoreOnThePlainLayout(t *testing.T) {
	store := openIndexed(t, filepath.Join(t.TempDir(), "actions"))
	seedPairs(t, store, 4)

	h := store.Health()
	if !h.Indexed {
		t.Fatal("a store opened with the snapshot says it has none")
	}
	if h.Layout != "v1" || h.Segments != 1 || h.CurrentSegment != segmentLegacyName {
		t.Fatalf("layout=%q segments=%d current=%q, want v1 / 1 / %q", h.Layout, h.Segments, h.CurrentSegment, segmentLegacyName)
	}
	if h.Actions != 4 || h.Attempts != 4 {
		t.Fatalf("actions=%d attempts=%d, want 4 and 4", h.Actions, h.Attempts)
	}
	if h.ActionBytes <= 0 || h.AttemptBytes <= 0 {
		t.Fatalf("log sizes = %d / %d, want both positive", h.ActionBytes, h.AttemptBytes)
	}
	if h.BuildDuration <= 0 {
		t.Fatal("the time the snapshot took to build is not reported")
	}
	if h.Rebuilds != 0 || h.LastRebuildReason != "" || !h.LastRebuildAt.IsZero() {
		t.Fatalf("a store nobody else wrote to reports a rebuild: %d %q %v", h.Rebuilds, h.LastRebuildReason, h.LastRebuildAt)
	}
	if h.Dirty || h.Broken || h.InspectFailed {
		t.Fatalf("a quiet store reports dirty=%v broken=%v inspect_failed=%v", h.Dirty, h.Broken, h.InspectFailed)
	}
	if h.RecordMismatches != 0 {
		t.Fatalf("record mismatches = %d on the plain layout", h.RecordMismatches)
	}
}

func TestHealthOfAStoreOpenedWithoutTheSnapshotStillSaysTheLayout(t *testing.T) {
	store := openFull(t, filepath.Join(t.TempDir(), "actions"))
	seedPairs(t, store, 2)

	h := store.Health()
	if h.Indexed {
		t.Fatal("a store opened without the snapshot says it has one")
	}
	if h.Layout != "v1" || h.Segments != 1 || h.CurrentSegment != segmentLegacyName {
		t.Fatalf("layout=%q segments=%d current=%q", h.Layout, h.Segments, h.CurrentSegment)
	}
	if h.Actions != 0 || h.Attempts != 0 || h.BuildDuration != 0 {
		t.Fatalf("a store with no snapshot reports counts it does not hold: %+v", h)
	}
}

func TestHealthOfARotatedStoreNamesTheSegmentsAndChecksTheRecords(t *testing.T) {
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

	h := store.Health()
	if h.Layout != "v2" {
		t.Fatalf("layout = %q, want v2", h.Layout)
	}
	// segment 0 (the frozen plain files) and one segment per day.
	if h.Segments != 4 || h.CurrentSegment != "20261015-01" {
		t.Fatalf("segments=%d current=%q, want 4 / 20261015-01", h.Segments, h.CurrentSegment)
	}
	if !h.Segment0ReadOnly || !h.Segment0MatchesLayout {
		t.Fatalf("segment 0: read_only=%v matches_layout=%v, want both true", h.Segment0ReadOnly, h.Segment0MatchesLayout)
	}
	if h.RecordMismatches != 0 || h.InspectFailed {
		t.Fatalf("record mismatches = %d inspect_failed=%v on a store nobody touched", h.RecordMismatches, h.InspectFailed)
	}
	if h.Actions != 9 || h.Attempts != 9 {
		t.Fatalf("actions=%d attempts=%d, want 9 and 9", h.Actions, h.Attempts)
	}
	if h.Rebuilds != 0 {
		t.Fatalf("rotation made the snapshot rebuild %d times", h.Rebuilds)
	}
}

func TestHealthSaysWhyTheSnapshotWasReadAgain(t *testing.T) {
	ctx := context.Background()
	store := openIndexed(t, filepath.Join(t.TempDir(), "actions"))
	seedPairs(t, store, 2)

	action, attempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "outside", supportBase.Add(time.Hour))
	appendJSONLine(t, store.actionPath, action)
	appendJSONLine(t, store.attemptPath, attempt)
	before := time.Now().UTC()
	if _, err := store.GetAction(ctx, action.ActionID); err != nil {
		t.Fatal(err)
	}

	h := store.Health()
	if h.Rebuilds != 1 {
		t.Fatalf("rebuilds = %d, want 1", h.Rebuilds)
	}
	if h.LastRebuildReason != "bytes the snapshot was not told about" {
		t.Fatalf("reason = %q", h.LastRebuildReason)
	}
	if h.LastRebuildAt.IsZero() || h.LastRebuildAt.Before(before.Add(-time.Second)) || h.LastRebuildAt.Location() != time.UTC {
		t.Fatalf("last rebuild at %v, want a UTC time just now (%v)", h.LastRebuildAt, before)
	}
	if h.Dirty || h.Broken {
		t.Fatalf("a settled snapshot reports dirty=%v broken=%v", h.Dirty, h.Broken)
	}
}

func TestHealthSeesAClosedSegmentThatDoesNotMatchItsRecord(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeAllWritable(root) })
	pairs := makePairs(t, 6, rotStart)
	clock := newRotClock(rotStart)
	store := openRotating(t, root, clock, 0)
	writeAll(t, store, pairs, 0, 3)
	clock.Set(time.Date(2026, 10, 14, 0, 0, 5, 0, time.UTC))
	writeAll(t, store, pairs, 3, 6)

	// Someone appends to the first day's segment while the store is running. The
	// store would refuse to start on it; a running store has to say so.
	closed := segmentDir(root, "20261013-01")
	path := filepath.Join(closed, logFilenames[fileAction])
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	h := store.Health()
	if h.RecordMismatches != 1 {
		t.Fatalf("record mismatches = %d, want 1", h.RecordMismatches)
	}
	if !h.Segment0MatchesLayout {
		t.Fatal("segment 0 was not touched but is reported as changed")
	}
}

func TestHealthSeesSegmentZeroThatDoesNotMatchLayout(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeAllWritable(root) })
	pairs := makePairs(t, 3, rotStart)
	clock := newRotClock(rotStart)
	store := openRotating(t, root, clock, 0)
	writeAll(t, store, pairs, 0, 3)

	path := filepath.Join(root, logFilenames[fileAction])
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	h := store.Health()
	if h.Segment0MatchesLayout {
		t.Fatal("segment 0 grew after it was frozen and the health says it matches LAYOUT")
	}
	if h.RecordMismatches != 1 {
		t.Fatalf("record mismatches = %d, want 1", h.RecordMismatches)
	}
}

func TestHealthReportsAnUnreadableLayoutAsInspectFailed(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeAllWritable(root) })
	clock := newRotClock(rotStart)
	store := openRotating(t, root, clock, 0)
	writeAll(t, store, makePairs(t, 2, rotStart), 0, 2)
	if err := os.WriteFile(filepath.Join(root, layoutFilename), []byte("not a layout record\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := store.Health()
	if !h.InspectFailed {
		t.Fatal("a damaged LAYOUT is not reported")
	}
	if h.Layout != "" {
		t.Fatalf("layout = %q, want empty when the layout could not be read", h.Layout)
	}
	// What the process knows about itself does not depend on the files.
	if !h.Indexed || h.Actions != 2 {
		t.Fatalf("indexed=%v actions=%d", h.Indexed, h.Actions)
	}
}

// The health is read by a probe, so it must neither wait for a writer nor write.
func TestHealthDoesNotWaitForAWriterAndWritesNothing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "actions")
	store := openIndexed(t, root)
	seedPairs(t, store, 3)
	other := openIndexed(t, root)

	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		// A handle that is inside a transaction holds the OS lock of the store.
		done <- other.batch.Read(context.Background(), func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	before := fingerprint(t, root)

	result := make(chan StoreHealth, 1)
	go func() { result <- store.Health() }()
	select {
	case h := <-result:
		if !h.Indexed || h.Actions != 3 {
			t.Fatalf("health = %+v", h)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Health waited for the OS lock another handle holds")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if after := fingerprint(t, root); fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatal("Health changed the files of the store")
	}
}

// The health goes to /health, which is read by monitoring and by people. It
// carries no ID and no text a record holds.
func TestHealthCarriesNoIDAndNoRecordText(t *testing.T) {
	store := openIndexed(t, filepath.Join(t.TempDir(), "actions"))
	task, run := modulecore.NewTaskID(), modulecore.NewRunID()
	action, attempt := newOpenPair(task, run, domainaction.KindTool, "distinctive-name-for-the-leak-test", supportBase)
	if err := savePair(context.Background(), store, action, attempt); err != nil {
		t.Fatal(err)
	}

	text := fmt.Sprintf("%+v %#v", store.Health(), store.Health())
	for _, secret := range []string{
		string(action.ActionID), string(attempt.AttemptID), string(task), string(run),
		"distinctive-name-for-the-leak-test",
	} {
		if strings.Contains(text, secret) {
			t.Fatalf("the health carries %q", secret)
		}
	}
}

// docs/05 says a store on layout v2 is opened with the snapshot whatever the
// options say (the segments are only joined by it). That is why switching
// action_store.index.enabled off is not a way back for such a store.
func TestAStoreOnLayoutV2IsOpenedWithTheSnapshotWhateverTheOptionsSay(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeAllWritable(root) })
	clock := newRotClock(rotStart)
	first := openRotating(t, root, clock, 0)
	writeAll(t, first, makePairs(t, 2, rotStart), 0, 2)

	reopened, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: false})
	if err != nil {
		t.Fatal(err)
	}
	h := reopened.Health()
	if !h.Indexed || h.Layout != "v2" || h.Actions != 2 {
		t.Fatalf("health = %+v, want an indexed layout v2 store holding both Actions", h)
	}
}

// A rotation seals the segment before it makes the next one, so a segment that is
// sealed with nothing after it is a rotation that stopped between the two: writes
// wait for the successor (the next open or rotation completes it). That is not the
// size mismatch of a closed segment, and it is the one condition under which
// writes fail, so the health names it.
func TestHealthSaysWhenTheSegmentWritesGoToIsAlreadySealed(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeAllWritable(root) })
	clock := newRotClock(rotStart)
	store := openRotating(t, root, clock, 0)
	writeAll(t, store, makePairs(t, 3, rotStart), 0, 3)
	if h := store.Health(); h.CurrentSegmentSealed {
		t.Fatal("the segment writes go to is reported as sealed before anything sealed it")
	}

	newest := segmentNames(t, root)[0]
	sz, err := segmentFileSizes(segmentDir(root, newest))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeSealed(segmentDir(root, newest), sz, nil); err != nil {
		t.Fatal(err)
	}

	h := store.Health()
	if !h.CurrentSegmentSealed {
		t.Fatal("a sealed segment with no successor is not reported")
	}
	if h.RecordMismatches != 0 {
		t.Fatalf("record mismatches = %d: the seal matches the files", h.RecordMismatches)
	}
}
