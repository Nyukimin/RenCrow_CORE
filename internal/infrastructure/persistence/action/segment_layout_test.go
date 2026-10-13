package action

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
)

// LAYOUT, the SEALED records and the size checks (design 5.3 and 8, AC-13, D8): a
// history that is not the one that was recorded is never opened, and refusing it
// writes nothing.

// segmentedStore builds, by hand and without the rotation code, a store on
// layout v2: segment 0 (the first four pairs, frozen and recorded in LAYOUT), two
// closed segments (sealed and read-only) and one open. A plain store is the
// format of one segment, so each segment is written by one. It is the state a
// process that stopped leaves, and the tests that refuse a damaged store start
// from it. The returned clock is on the day of the newest segment.
func segmentedStore(t testing.TB) (root string, pairs []pair, clock *rotClock) {
	t.Helper()
	root = t.TempDir()
	t.Cleanup(func() { makeAllWritable(root) })
	pairs = makePairs(t, 12, rotStart)
	plainStore(t, root, pairs, 4)
	frozen, err := segmentFileSizes(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeFileAtomically(layoutPath(root), formatSizeRecord(layoutVersionLine, layoutFrozenWord, frozen), nil, "layout"); err != nil {
		t.Fatal(err)
	}
	if err := freezeSegmentZero(root); err != nil {
		t.Fatal(err)
	}
	for i, span := range [][2]int{{4, 7}, {7, 9}, {9, 12}} {
		dir := segmentDir(root, fmt.Sprintf("2026101%d-01", 3+i))
		seg, err := NewJSONLStoreWithOptions(dir, OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		writeAll(t, seg, pairs, span[0], span[1])
		if i < 2 {
			final, err := segmentFileSizes(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writeSealed(dir, final, nil); err != nil {
				t.Fatal(err)
			}
			if err := freezeFiles(segmentFilesOf(dir)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root, pairs, newRotClock(rotStart.Add(48 * time.Hour))
}

// copyOf returns a copy of a store directory that the test may damage.
func copyOf(t testing.TB, root string) string {
	t.Helper()
	dst := t.TempDir()
	t.Cleanup(func() { makeAllWritable(dst) })
	copyTree(t, root, dst)
	return dst
}

func mutateFile(t testing.TB, path string, fn func([]byte) []byte) {
	t.Helper()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fn(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lastLine(data []byte) []byte {
	trimmed := strings.TrimRight(string(data), "\n")
	i := strings.LastIndex(trimmed, "\n")
	return []byte(trimmed[i+1:] + "\n")
}

func TestAChangedSegmentFailsClosedAndNothingIsWritten(t *testing.T) {
	base, _, clock := segmentedStore(t)
	zeroFile := actionFilename
	closedFile := filepath.Join(logsDirName, "20261013-01", attemptFilename)
	cases := []struct {
		name   string
		damage func(root string)
	}{
		{"segment 0 grown by a valid line", func(root string) {
			mutateFile(t, filepath.Join(root, zeroFile), func(d []byte) []byte { return append(d, lastLine(d)...) })
		}},
		{"segment 0 cut by a byte", func(root string) {
			mutateFile(t, filepath.Join(root, zeroFile), func(d []byte) []byte { return d[:len(d)-1] })
		}},
		{"a closed segment grown by a valid line", func(root string) {
			mutateFile(t, filepath.Join(root, closedFile), func(d []byte) []byte { return append(d, lastLine(d)...) })
		}},
		{"a closed segment cut by a byte", func(root string) {
			mutateFile(t, filepath.Join(root, closedFile), func(d []byte) []byte { return d[:len(d)-1] })
		}},
		{"a closed segment's action log grown", func(root string) {
			mutateFile(t, filepath.Join(root, logsDirName, "20261014-01", actionFilename), func(d []byte) []byte { return append(d, lastLine(d)...) })
		}},
		{"a closed segment without its record", func(root string) {
			if err := os.Remove(sealedPath(filepath.Join(root, logsDirName, "20261013-01"))); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := copyOf(t, base)
			tc.damage(root)
			before := fingerprint(t, root)
			for label, opts := range map[string]OpenOptions{
				"with rotation":    rotOptions(clock, 0, nil),
				"without the flag": {},
			} {
				_, err := NewJSONLStoreWithOptions(root, opts)
				if !errors.Is(err, ErrSealedSegmentChanged) {
					t.Fatalf("%s: open = %v, want ErrSealedSegmentChanged", label, err)
				}
			}
			requireUnchanged(t, before, fingerprint(t, root), true, "refused open")
		})
	}
}

func TestAnUnknownOrDamagedLayoutFailsClosedAndNothingIsWritten(t *testing.T) {
	base, _, clock := segmentedStore(t)
	layoutBody := func(root string) string {
		data, err := os.ReadFile(layoutPath(root))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	cases := []struct {
		name string
		file func(root string) string
		body func(root string) string
	}{
		{"a later LAYOUT version", func(r string) string { return layoutPath(r) }, func(r string) string {
			return strings.Replace(layoutBody(r), layoutVersionLine, "actions-layout/v3", 1)
		}},
		{"a LAYOUT without its end line", func(r string) string { return layoutPath(r) }, func(r string) string {
			return strings.TrimSuffix(layoutBody(r), layoutEndLine+"\n")
		}},
		{"an empty LAYOUT", func(r string) string { return layoutPath(r) }, func(string) string { return "" }},
		{"a LAYOUT that is not one", func(r string) string { return layoutPath(r) }, func(string) string { return "hello\n" }},
		{"a LAYOUT with a negative size", func(r string) string { return layoutPath(r) }, func(r string) string {
			return strings.Replace(layoutBody(r), "frozen "+actionFilename+" ", "frozen "+actionFilename+" -", 1)
		}},
		{"a LAYOUT naming another file", func(r string) string { return layoutPath(r) }, func(r string) string {
			return strings.Replace(layoutBody(r), attemptFilename, "other.jsonl", 1)
		}},
		{"a later SEALED version", func(r string) string { return sealedPath(segmentDir(r, "20261013-01")) }, func(r string) string {
			data, _ := os.ReadFile(sealedPath(segmentDir(r, "20261013-01")))
			return strings.Replace(string(data), sealedVersionLine, "actions-segment/v9", 1)
		}},
		{"a truncated SEALED", func(r string) string { return sealedPath(segmentDir(r, "20261014-01")) }, func(r string) string {
			data, _ := os.ReadFile(sealedPath(segmentDir(r, "20261014-01")))
			return string(data[:len(data)/2])
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := copyOf(t, base)
			body := tc.body(root)
			mutateFile(t, tc.file(root), func([]byte) []byte { return []byte(body) })
			before := fingerprint(t, root)
			if _, err := NewJSONLStoreWithOptions(root, rotOptions(clock, 0, nil)); !errors.Is(err, ErrUnknownLayout) {
				t.Fatalf("open = %v, want ErrUnknownLayout", err)
			}
			if _, err := NewJSONLStoreWithOptions(root, OpenOptions{}); !errors.Is(err, ErrUnknownLayout) {
				t.Fatalf("open without the flag = %v, want ErrUnknownLayout", err)
			}
			if _, err := VerifyActionStore(context.Background(), root); !errors.Is(err, ErrUnknownLayout) {
				t.Fatalf("verify = %v, want ErrUnknownLayout", err)
			}
			if _, err := InspectActionLayout(root); !errors.Is(err, ErrUnknownLayout) {
				t.Fatalf("layout show = %v, want ErrUnknownLayout", err)
			}
			requireUnchanged(t, before, fingerprint(t, root), true, "refused open")
		})
	}
}

func TestSegmentsWithDataButNoLayoutAreRefused(t *testing.T) {
	root := t.TempDir()
	pairs := makePairs(t, 3, rotStart)
	plainStore(t, root, pairs, 3)
	dir := segmentDir(root, "20261013-01")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, actionFilename))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, actionFilename), data, 0o644); err != nil {
		t.Fatal(err)
	}
	before := fingerprint(t, root)
	for label, opts := range map[string]OpenOptions{"plain": {}, "indexed": {Index: true}, "rotating": rotOptions(newRotClock(rotStart), 0, nil)} {
		if _, err := NewJSONLStoreWithOptions(root, opts); !errors.Is(err, ErrUnknownLayout) {
			t.Fatalf("%s open = %v, want ErrUnknownLayout (logs/ holds data but there is no LAYOUT)", label, err)
		}
	}
	requireUnchanged(t, before, fingerprint(t, root), true, "refused open")
}

func TestLayoutWithoutAnySegmentFailsClosed(t *testing.T) {
	base, _, clock := segmentedStore(t)
	root := copyOf(t, base)
	if err := os.RemoveAll(logsDir(root)); err != nil {
		t.Fatal(err)
	}
	if _, err := NewJSONLStoreWithOptions(root, rotOptions(clock, 0, nil)); !errors.Is(err, ErrSegmentMissing) {
		t.Fatalf("open = %v, want ErrSegmentMissing", err)
	}
}

func TestAnOlderBinaryCannotOpenASegmentedStoreAndWritesNothing(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("the read-only guard is a file mode; it does not stop root, and the size check is the defense then")
	}
	root, _, _ := segmentedStore(t)
	before := fingerprint(t, root)
	// What the binary that predates segments does first: jsonlbatch.New on the
	// root, with the same two names.
	if _, err := jsonlbatch.New(root, []string{actionFilename, attemptFilename}); err == nil {
		t.Fatal("an older binary opened a segmented store for writing")
	}
	requireUnchanged(t, before, fingerprint(t, root), true, "older binary")
}

func TestARotationThatStoppedAfterTheSealIsCompletedAtTheNextOpen(t *testing.T) {
	for _, withFlag := range []bool{true, false} {
		name := "with rotation"
		if !withFlag {
			name = "without rotation"
		}
		t.Run(name, func(t *testing.T) {
			base, pairs, clock := segmentedStore(t)
			root := copyOf(t, base)
			newest := segmentNames(t, root)[2]
			sz, err := segmentFileSizes(segmentDir(root, newest))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writeSealed(segmentDir(root, newest), sz, nil); err != nil {
				t.Fatal(err)
			}
			opts := OpenOptions{now: clock.Now}
			if withFlag {
				opts = rotOptions(clock, 0, nil)
			}
			store, err := NewJSONLStoreWithOptions(root, opts)
			if err != nil {
				t.Fatalf("open after a rotation that stopped after the seal: %v", err)
			}
			names := segmentNames(t, root)
			if len(names) != 4 {
				t.Fatalf("segments = %v, want a successor of %s", names, newest)
			}
			requireHolds(t, store, pairs, 0, 12)
			extra := makePairs(t, 2, rotStart.Add(72*time.Hour))
			writeAll(t, store, extra, 0, 2)
			if got := fileSize(t, filepath.Join(segmentDir(root, newest), actionFilename)); got != sz[fileAction] {
				t.Fatalf("the sealed segment grew from %d to %d bytes", sz[fileAction], got)
			}
			requireSnapshotEqualsLog(t, store)
		})
	}
}

func TestASecondWriterFailsClosedOnceTheFirstHasRotated(t *testing.T) {
	base, _, clock := segmentedStore(t)
	root := copyOf(t, base)
	first := openRotating(t, root, clock, 0)
	laterClock := newRotClock(rotStart)
	second, err := NewJSONLStoreWithOptions(root, rotOptions(laterClock, 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	newestBefore := segmentNames(t, root)[2]

	clock.Advance(24 * time.Hour)
	extra := makePairs(t, 4, rotStart.Add(96*time.Hour))
	writeAll(t, first, extra, 0, 1) // rotates
	if got := segmentNames(t, root); len(got) != 4 {
		t.Fatalf("the first writer did not rotate: %v", got)
	}
	sealedBefore := fingerprint(t, segmentDir(root, newestBefore))

	err = extra[2].create(context.Background(), second)
	if !errors.Is(err, ErrSegmentMissing) {
		t.Fatalf("the second writer's write = %v, want ErrSegmentMissing (its segment was closed by another writer)", err)
	}
	requireUnchanged(t, sealedBefore, fingerprint(t, segmentDir(root, newestBefore)), true, "closed segment after the second writer")
	writeAll(t, first, extra, 1, 2)
}

// A rotation seals the segment it is in. If another writer has sealed it already,
// this writer's rotation must stop where it is: sealing again and making a
// successor of its own would fork the store, and the failure that follows would
// leave this writer unable to write for a reason that has nothing to do with the
// segment it is looking at.
func TestARotationStopsWhenAnotherWriterAlreadySealedTheSegment(t *testing.T) {
	base, _, clock := segmentedStore(t)
	root := copyOf(t, base)
	a := openRotating(t, root, clock, 0)
	b := openRotating(t, root, clock, 0)
	newest := segmentNames(t, root)[2]

	clock.Advance(24 * time.Hour)
	extra := makePairs(t, 4, rotStart.Add(90*time.Hour))
	writeAll(t, b, extra, 0, 1) // B rotates and writes to its successor
	if got := segmentNames(t, root); len(got) != 4 {
		t.Fatalf("B did not rotate: %v", got)
	}
	sealed := fingerprint(t, segmentDir(root, newest))

	err := extra[2].create(context.Background(), a)
	if !errors.Is(err, ErrSegmentMissing) {
		t.Fatalf("A's write = %v, want ErrSegmentMissing", err)
	}
	if a.rot.incomplete {
		t.Fatal("A thinks it sealed a segment that B sealed; every later write of A would fail for that reason")
	}
	requireUnchanged(t, sealed, fingerprint(t, segmentDir(root, newest)), true, "the segment B sealed")
	if got := segmentNames(t, root); len(got) != 4 {
		t.Fatalf("A made a segment of its own: %v", got)
	}
	writeAll(t, b, extra, 1, 2)
}
