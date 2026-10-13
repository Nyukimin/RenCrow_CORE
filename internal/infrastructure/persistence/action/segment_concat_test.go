package action

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
)

// layout concat (design 8): the way back from segments to the plain layout. The
// result is the plain store the history would have made had it never rotated,
// byte for byte, and a concat that stopped anywhere is finished by running it
// again.

// rotatedStore moves a plain store to segments and writes across a day change and
// the size valve. It returns the root, the pairs, the plain store's own files as
// they were at the migration (the "original" a concat must keep as a prefix) and
// the files of a control store given the same records without rotation.
func rotatedStore(t testing.TB, n int) (root string, pairs []pair, original, control map[string][]byte) {
	t.Helper()
	root = t.TempDir()
	t.Cleanup(func() { makeAllWritable(root) })
	pairs = makePairs(t, n, rotStart)
	first := n / 4
	plainStore(t, root, pairs, first)
	original = map[string][]byte{}
	for _, name := range logFilenames {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		original[name] = data
	}
	clock := newRotClock(rotStart.Add(time.Hour))
	store := openRotating(t, root, clock, 3000)
	second := first + (n-first)/2
	writeAll(t, store, pairs, first, second)
	clock.Advance(26 * time.Hour)
	writeAll(t, store, pairs, second, n)
	if got := segmentNames(t, root); len(got) < 4 {
		t.Fatalf("the fixture made %v; the concat test needs at least four segments", got)
	}

	controlRoot := t.TempDir()
	plainStore(t, controlRoot, pairs, n)
	control = map[string][]byte{}
	for _, name := range logFilenames {
		data, err := os.ReadFile(filepath.Join(controlRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		control[name] = data
	}
	return root, pairs, original, control
}

func requireRootLogs(t testing.TB, root string, want map[string][]byte) {
	t.Helper()
	for name, data := range want {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("%s holds %d bytes, want the %d bytes the history makes without rotation (or they differ)", name, len(got), len(data))
		}
	}
}

func TestConcatReturnsTheStoreToThePlainLayoutByteForByte(t *testing.T) {
	ctx := context.Background()
	root, pairs, original, control := rotatedStore(t, 24)
	segmentsBefore := segmentNames(t, root)
	before := fingerprint(t, root)

	plan, err := ConcatActionLayout(ctx, root, false)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.State != "plan" || plan.Segments != len(segmentsBefore) {
		t.Fatalf("plan = %+v, want state plan with %d segments", plan, len(segmentsBefore))
	}
	for _, file := range plan.Files {
		if want := int64(len(control[file.Name])); file.FinalSize != want {
			t.Fatalf("the plan says %s ends at %d bytes, the plain history is %d", file.Name, file.FinalSize, want)
		}
	}
	requireUnchanged(t, before, fingerprint(t, root), true, "a plan")

	report, err := ConcatActionLayout(ctx, root, true)
	if err != nil {
		t.Fatalf("concat: %v", err)
	}
	if report.State != "done" || report.RolledBackLogs == "" || report.RolledBackLayout == "" {
		t.Fatalf("report = %+v", report)
	}
	requireRootLogs(t, root, control)
	for _, name := range logFilenames {
		if !bytes.HasPrefix(control[name], original[name]) {
			t.Fatal("the control is not the original plus appends")
		}
	}
	if _, err := os.Stat(layoutPath(root)); err == nil {
		t.Fatal("LAYOUT is still there")
	}
	if _, err := os.Stat(logsDir(root)); err == nil {
		t.Fatal("logs/ is still there")
	}
	for _, aside := range []string{report.RolledBackLogs, report.RolledBackLayout} {
		if _, err := os.Stat(filepath.Join(root, aside)); err != nil {
			t.Fatalf("what the concat set aside (%s) is gone: %v", aside, err)
		}
	}
	if kept := segmentNamesUnder(t, filepath.Join(root, report.RolledBackLogs)); len(kept) != len(segmentsBefore) {
		t.Fatalf("%d segments were kept aside, %d existed", len(kept), len(segmentsBefore))
	}

	// The plain store works, and goes on to the same bytes the control makes.
	plain, err := NewJSONLStoreWithOptions(root, OpenOptions{})
	if err != nil {
		t.Fatalf("open the returned store with the binary that predates segments: %v", err)
	}
	requireHolds(t, plain, pairs, 0, len(pairs))
	extra := makePairs(t, 2, rotStart.Add(200*time.Hour))
	writeAll(t, plain, extra, 0, 2)
	controlRoot := t.TempDir()
	for name, data := range control {
		if err := os.WriteFile(filepath.Join(controlRoot, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	controlStore, err := NewJSONLStoreWithOptions(controlRoot, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	writeAll(t, controlStore, extra, 0, 2)
	for _, name := range logFilenames {
		a, _ := os.ReadFile(filepath.Join(root, name))
		b, _ := os.ReadFile(filepath.Join(controlRoot, name))
		if !bytes.Equal(a, b) {
			t.Fatalf("%s differs from the plain history after the next writes", name)
		}
	}

	// And it can be moved to segments again.
	again, err := NewJSONLStoreWithOptions(root, rotOptions(newRotClock(rotStart.Add(300*time.Hour)), 0, nil))
	if err != nil {
		t.Fatalf("migrate again: %v", err)
	}
	requireHolds(t, again, pairs, 0, len(pairs))
}

func segmentNamesUnder(t testing.TB, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names
}

func TestConcatOnAPlainStoreChangesNothing(t *testing.T) {
	root := t.TempDir()
	plainStore(t, root, makePairs(t, 3, rotStart), 3)
	before := fingerprint(t, root)
	report, err := ConcatActionLayout(context.Background(), root, true)
	if err != nil || report.State != "layout_v1" {
		t.Fatalf("concat on a plain store = %+v, %v; want layout_v1", report, err)
	}
	requireUnchanged(t, before, fingerprint(t, root), true, "concat of a plain store")
}

func TestConcatStoppedAtEveryStepIsFinishedByRunningItAgain(t *testing.T) {
	ctx := context.Background()
	base, _, _, control := rotatedStore(t, 16)

	points := 0
	{
		root := copyOf(t, base)
		if _, err := concatActionLayout(ctx, root, true, func(string) { points++ }); err != nil {
			t.Fatal(err)
		}
	}
	if points < 8 {
		t.Fatalf("only %d failpoints fired in a concat", points)
	}
	for stop := 1; stop <= points; stop++ {
		t.Run(fmt.Sprintf("stop-%d", stop), func(t *testing.T) {
			root := copyOf(t, base)
			seen := 0
			crashed := didCrash(func() {
				_, _ = concatActionLayout(ctx, root, true, func(stage string) {
					if seen++; seen == stop {
						panic(simulatedCrash{stage})
					}
				})
			})
			if !crashed {
				t.Fatal("the failpoint did not fire")
			}
			// Between the stop and the end the store is either as it was, or refused: no
			// opener writes to a store whose segment 0 is not the size LAYOUT recorded,
			// and a refused open changes nothing.
			if _, err := os.Stat(layoutPath(root)); err == nil {
				before := fingerprint(t, root)
				if store, err := NewJSONLStoreWithOptions(root, rotOptions(newRotClock(rotStart.Add(500*time.Hour)), 0, nil)); err != nil {
					requireUnchanged(t, before, fingerprint(t, root), true, "an open in the middle of a concat")
				} else if layout, _ := readLayout(root); !layout.v2 {
					t.Fatalf("the open turned the half-finished concat into %+v", layout)
				} else {
					_ = store
				}
			}
			report, err := ConcatActionLayout(ctx, root, true)
			if err != nil {
				t.Fatalf("run the concat again: %v", err)
			}
			if _, err := os.Stat(layoutPath(root)); err == nil {
				t.Fatalf("LAYOUT is still there after running the concat again (state %q)", report.State)
			}
			if report.State != "done" && report.State != "layout_v1" {
				t.Fatalf("state = %q after running it again", report.State)
			}
			requireRootLogs(t, root, control)
			if _, err := os.Stat(layoutPath(root)); err == nil {
				t.Fatal("LAYOUT is still there")
			}
			plain, err := NewJSONLStoreWithOptions(root, OpenOptions{})
			if err != nil {
				t.Fatalf("open the returned store: %v", err)
			}
			requireSnapshotEqualsLogOrPlain(t, plain)
		})
	}
}

func TestConcatRefusesAChangedSegmentAndChangesNothing(t *testing.T) {
	base, _, _, _ := rotatedStore(t, 16)
	root := copyOf(t, base)
	names := segmentNames(t, root)
	mutateFile(t, filepath.Join(root, logsDirName, names[0], actionFilename), func(d []byte) []byte { return append(d, lastLine(d)...) })
	before := fingerprint(t, root)
	_, err := ConcatActionLayout(context.Background(), root, true)
	if !errors.Is(err, ErrSealedSegmentChanged) {
		t.Fatalf("concat of a store with a changed closed segment = %v, want ErrSealedSegmentChanged", err)
	}
	requireUnchanged(t, before, fingerprint(t, root), true, "refused concat")
}

func TestConcatRefusesWhileTheStoreIsInUse(t *testing.T) {
	base, _, _, _ := rotatedStore(t, 16)
	root := copyOf(t, base)
	names := segmentNames(t, root)
	newest := segmentDir(root, names[len(names)-1])
	holder, err := jsonlbatch.New(newest, logFilenames[:])
	if err != nil {
		t.Fatal(err)
	}
	locked, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- holder.Read(context.Background(), func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	defer func() { close(release); <-done }()

	restore := concatLockWait
	concatLockWait = 150 * time.Millisecond
	defer func() { concatLockWait = restore }()
	before := fingerprint(t, root)
	started := time.Now()
	_, err = ConcatActionLayout(context.Background(), root, true)
	if !errors.Is(err, ErrStoreBusy) {
		t.Fatalf("concat while a writer holds the store = %v, want ErrStoreBusy", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("the concat waited far longer than its lock wait")
	}
	requireUnchanged(t, before, fingerprint(t, root), true, "busy concat")
	if runtime.GOOS != "windows" && !segmentZeroFrozen(root) {
		t.Fatal("a refused concat unfroze segment 0")
	}
}

// Once the concat has the store quiet it makes every file of every segment
// read-only, so that a writer that is still running fails instead of appending
// to a segment that has been copied.
func TestAWriterThatSurvivesTheStartOfAConcatCannotAppend(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("the read-only guard is a file mode; it does not stop root")
	}
	ctx := context.Background()
	base, _, _, _ := rotatedStore(t, 16)
	root := copyOf(t, base)
	clock := newRotClock(rotStart.Add(30 * time.Hour)) // the day of the newest segment
	writer, err := NewJSONLStoreWithOptions(root, rotOptions(clock, 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	extra := makePairs(t, 1, rotStart.Add(300*time.Hour))[0]
	var attemptErr error
	attempted := false
	_, err = concatActionLayout(ctx, root, true, func(stage string) {
		if stage == "concat-frozen" && !attempted {
			attempted = true
			attemptErr = extra.create(ctx, writer)
		}
	})
	if err != nil {
		t.Fatalf("concat: %v", err)
	}
	if !attempted {
		t.Fatal("the concat never reached the point where the segments are frozen")
	}
	if attemptErr == nil {
		t.Fatal("a running writer appended after the concat froze the segments")
	}
	for _, name := range logFilenames {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(extra.action.ActionID)) {
			t.Fatalf("%s holds the record the late writer tried to append", name)
		}
	}
}

func TestConcatDoesNotDeleteWhatItSetsAside(t *testing.T) {
	root, _, _, _ := rotatedStore(t, 16)
	names := segmentNames(t, root)
	sizesBefore := map[string]int64{}
	for _, name := range names {
		for _, file := range logFilenames {
			sizesBefore[name+"/"+file] = fileSize(t, filepath.Join(segmentDir(root, name), file))
		}
	}
	report, err := ConcatActionLayout(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	for key, size := range sizesBefore {
		path := filepath.Join(root, report.RolledBackLogs, filepath.FromSlash(key))
		if got := fileSize(t, path); got != size {
			t.Fatalf("%s is %d bytes after the concat, was %d", key, got, size)
		}
	}
}
