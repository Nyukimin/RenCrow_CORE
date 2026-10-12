package task

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Fault injection for segments (spec 14.2, AC-13): wherever the writer stops, the
// next open finds every acknowledged record, the index equals a build from the
// log, and no byte of a closed segment has changed.

// copyTree copies a directory tree, keeping file modes. The copy is what a
// process that died leaves behind: nothing in it is cleaned up.
func copyTree(t testing.TB, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

// makeWritable undoes the read-only mode of segment 0 so a test can remove its
// directory (t.TempDir cleanup, and copies of a frozen store).
func makeWritable(t testing.TB, root string) {
	t.Helper()
	_ = unfreezeSegmentZero(root)
}

func requireAllStageOneTasks(t testing.TB, store *JSONLStore, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := store.GetTask(context.Background(), indexedTestTask(i, indexTestNow).TaskID); err != nil {
			t.Fatalf("task %d of the stage 1 store is gone: %v", i, err)
		}
	}
}

func TestMigrationStoppedAtEveryStepLosesNothingAndCanBeFinished(t *testing.T) {
	const n = 10
	base := t.TempDir()
	stageOneStore(t, base, n)
	original := fingerprintDir(t, base)

	// Count the points first.
	points := 0
	{
		root := t.TempDir()
		copyTree(t, base, root)
		clock := newTestClock("2026-10-13T08:00:00Z")
		store, err := NewJSONLStoreWithOptions(root, rotationOptions(clock, 0, func(string) { points++ }))
		if err != nil {
			t.Fatal(err)
		}
		_ = store.Close()
		makeWritable(t, root)
	}
	if points < 15 {
		t.Fatalf("only %d failpoints fired in a migration", points)
	}

	for stop := 1; stop <= points; stop++ {
		for _, mode := range []string{"migrate again", "open without rotation"} {
			t.Run(fmt.Sprintf("stop-%d/%s", stop, mode), func(t *testing.T) {
				root := t.TempDir()
				copyTree(t, base, root)
				t.Cleanup(func() { makeWritable(t, root) })
				clock := newTestClock("2026-10-13T08:00:00Z")
				seen := 0
				crashed := didCrash(func() {
					store, err := NewJSONLStoreWithOptions(root, rotationOptions(clock, 0, func(stage string) {
						if seen++; seen == stop {
							panic(simulatedCrash{stage})
						}
					}))
					if err == nil {
						_ = store.Close()
					}
				})
				if !crashed {
					t.Fatalf("the failpoint did not fire")
				}
				opts := rotationOptions(clock, 0, nil)
				if mode == "open without rotation" {
					opts.Rotation = false
				}
				store, err := NewJSONLStoreWithOptions(root, opts)
				if err != nil {
					t.Fatalf("open after the stop: %v", err)
				}
				defer store.Close()
				requireAllStageOneTasks(t, store, n)
				populate(t, store, n, n+2)
				requireSegmentedOrPlainStoreEqualsFullBuild(t, store, root)
				if layout, _ := readLayout(root); layout.v2 {
					for name, fp := range original {
						if layout.frozen[sizedIndex(name)] != fp.size {
							t.Fatalf("LAYOUT records %s as %d bytes, the original was %d", name, layout.frozen[sizedIndex(name)], fp.size)
						}
					}
					requireContentEqual(t, "segment 0", original, fingerprintDir(t, root))
				}
				if mode == "migrate again" {
					if layout, _ := readLayout(root); !layout.v2 {
						t.Fatal("a second open with rotation did not finish the migration")
					}
				}
			})
		}
	}
}

// requireContentEqual is requireFingerprintsEqual for a copy: the bytes are the
// same, the modification times are those of the copy.
func requireContentEqual(t testing.TB, what string, before, after map[string]fileFingerprint) {
	t.Helper()
	for name, b := range before {
		a, ok := after[name]
		if !ok || a.size != b.size || a.sum != b.sum {
			t.Fatalf("%s: %s changed (size %d -> %d)", what, name, b.size, a.size)
		}
	}
}

func sizedIndex(name string) int {
	for i, n := range sizedFilenames {
		if n == name {
			return i
		}
	}
	panic("not a sized file: " + name)
}

// requireSegmentedOrPlainStoreEqualsFullBuild is the equality of a store with a
// build from its log, whichever layout it is on.
func requireSegmentedOrPlainStoreEqualsFullBuild(t testing.TB, store *JSONLStore, root string) {
	t.Helper()
	requireSegmentedStoreEqualsFullBuild(t, store, root)
}

func TestRotationStoppedAtEveryStepLosesNothing(t *testing.T) {
	for _, stage := range []string{"rotate-settled", "rotate-created", "rotate-switched"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			t.Cleanup(func() { makeWritable(t, root) })
			clock := newTestClock("2026-10-13T08:00:00Z")
			stageOneStore(t, root, 4)
			var armed atomic.Bool
			store, err := NewJSONLStoreWithOptions(root, rotationOptions(clock, 0, func(s string) {
				if s == stage && armed.Load() {
					panic(simulatedCrash{s})
				}
			}))
			if err != nil {
				t.Fatal(err)
			}
			populate(t, store, 4, 8)
			acked := scanSegmentedLog(t, root)
			closedBefore := fingerprintDir(t, segmentDir(root, "20261013-01"))

			armed.Store(true)
			clock.Set("2026-10-14T08:00:00Z")
			crashed := didCrash(func() { _ = store.SaveTask(context.Background(), indexedTestTask(900, indexTestNow)) })
			if !crashed {
				t.Fatal("the failpoint did not fire")
			}
			if err := store.closeStore(false); err != nil {
				t.Fatal(err)
			}
			requireFingerprintsEqual(t, "the segment that was being closed", closedBefore, fingerprintDir(t, segmentDir(root, "20261013-01")))

			again := openRotating(t, root, clock, 0, nil)
			for id := range acked.tasks {
				if _, err := again.GetTask(context.Background(), id); err != nil {
					t.Fatalf("task %s was acknowledged and is gone: %v", id, err)
				}
			}
			populate(t, again, 8, 10)
			if got := segmentNames(t, root); len(got) != 2 || got[1] != "20261014-01" {
				t.Fatalf("segments after reopening on the next day = %v", got)
			}
			requireSegmentedStoreEqualsFullBuild(t, again, root)
		})
	}
}

// The sidecar written when a segment closes may never reach the disk: the writer
// dies right after the rotation. The sidecar then says the closed segment is
// still the open one, and the next start has to replay it and the new segment.
func TestACrashRightAfterARotationReplaysFromTheOlderSidecar(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeWritable(t, root) })
	clock := newTestClock("2026-10-13T08:00:00Z")
	stageOneStore(t, root, 4)
	store := openRotating(t, root, clock, 0, nil)
	populate(t, store, 4, 8)
	if err := store.idx.ck.checkpointNow("test"); err != nil {
		t.Fatal(err)
	}
	before, _ := readSidecarV2Header(t, root)
	populate(t, store, 8, 10) // more in the segment the sidecar calls open
	clock.Set("2026-10-14T08:00:00Z")
	populate(t, store, 10, 11) // the rotation, and one write into the new segment
	if err := store.closeStore(false); err != nil {
		t.Fatal(err)
	}
	after, _ := readSidecarV2Header(t, root)
	if after.OwnOrdinal != before.OwnOrdinal || after.OwnName != before.OwnName {
		t.Fatalf("the sidecar moved on (%d %s -> %d %s) although no checkpoint ran", before.OwnOrdinal, before.OwnName, after.OwnOrdinal, after.OwnName)
	}

	again := openRotating(t, root, clock, 0, nil)
	stats, _ := again.IndexStats()
	if stats.Source != sourceSidecar || stats.Segments != 3 {
		t.Fatalf("restored index: source %q, %d segments, want a restore from the sidecar over 3 segments (%s)", stats.Source, stats.Segments, stats.RebuildReason)
	}
	if again.idx.replayedLines == 0 {
		t.Fatal("nothing was replayed although the log is ahead of the sidecar")
	}
	requireSegmentedStoreEqualsFullBuild(t, again, root)
}

func readSidecarV2Header(t testing.TB, root string) (sidecarHeader, *taskIndex) {
	t.Helper()
	data, err := os.ReadFile(sidecarPathFor(root, true))
	if err != nil {
		t.Fatalf("read the layout v2 sidecar: %v", err)
	}
	ix, header, err := decodeSidecar(data)
	if err != nil {
		t.Fatalf("decode the layout v2 sidecar: %v", err)
	}
	return header, ix
}

func TestEverySingleBitFlipAndTruncationOfALayoutV2SidecarIsHarmless(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeWritable(t, root) })
	clock := newTestClock("2026-10-13T08:00:00Z")
	stageOneStore(t, root, 4)
	store := openRotating(t, root, clock, 0, nil)
	populate(t, store, 4, 7)
	clock.Set("2026-10-14T08:00:00Z")
	populate(t, store, 7, 10)
	clock.Set("2026-10-15T08:00:00Z")
	populate(t, store, 10, 12)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	good, err := os.ReadFile(sidecarPathFor(root, true))
	if err != nil {
		t.Fatal(err)
	}
	damage := map[string][]byte{}
	for i := 0; i < 90; i++ {
		flipped := append([]byte(nil), good...)
		at := len(good)*i/90 + i%7
		flipped[at] ^= byte(1) << (i % 8)
		damage[fmt.Sprintf("flip-%d", i)] = flipped
	}
	// The segment table sits right after the fixed header: damage it on purpose.
	for i := 0; i < 24; i++ {
		flipped := append([]byte(nil), good...)
		flipped[sidecarHeaderFixed+4+i*5%(3*sidecarSegEntryLen)] ^= 0x40
		damage[fmt.Sprintf("table-%d", i)] = flipped
	}
	for _, cut := range []int{0, 1, len(good) / 3, len(good) / 2, len(good) - 9, len(good) - 1} {
		damage[fmt.Sprintf("cut-%d", cut)] = good[:cut]
	}
	damage["appended"] = append(append([]byte(nil), good...), 0)
	for name, data := range damage {
		if err := os.WriteFile(sidecarPathFor(root, true), data, 0o600); err != nil {
			t.Fatal(err)
		}
		again, err := NewJSONLStoreWithOptions(root, rotationOptions(clock, 0, nil))
		if err != nil {
			t.Fatalf("%s: open: %v", name, err)
		}
		stats, _ := again.IndexStats()
		if stats.Source != sourceRebuilt {
			t.Fatalf("%s: a damaged sidecar was used (source %q)", name, stats.Source)
		}
		requireSegmentedStoreEqualsFullBuild(t, again, root)
		if err := again.closeStore(false); err != nil {
			t.Fatal(err)
		}
	}
}

func TestASidecarOfTheOtherLayoutIsNotUsed(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeWritable(t, root) })
	clock := newTestClock("2026-10-13T08:00:00Z")
	stageOneStore(t, root, 4)
	v1Sidecar, err := os.ReadFile(sidecarFile(root))
	if err != nil {
		t.Fatal(err)
	}
	store := openRotating(t, root, clock, 0, nil)
	populate(t, store, 4, 7)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// A layout v1 sidecar where the layout v2 one belongs.
	if err := os.WriteFile(sidecarPathFor(root, true), v1Sidecar, 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := NewJSONLStoreWithOptions(root, rotationOptions(clock, 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	stats, _ := again.IndexStats()
	if stats.Source != sourceRebuilt || stats.RebuildReason != rebuildLayout {
		t.Fatalf("source %q reason %q, want rebuilt/layout", stats.Source, stats.RebuildReason)
	}
	requireSegmentedStoreEqualsFullBuild(t, again, root)
	_ = again.Close()
}

// INV-5 across segments: the generation of a Run in a closed segment still
// fences a writer whose counter was rolled back, whether the index comes from the
// sidecar or from the log.
func TestWriterGenerationFenceHoldsAcrossSegments(t *testing.T) {
	const refused = "writer generation does not advance persisted Run ownership"
	for _, sidecarGone := range []bool{false, true} {
		t.Run(fmt.Sprintf("sidecar gone=%v", sidecarGone), func(t *testing.T) {
			root := t.TempDir()
			t.Cleanup(func() { makeWritable(t, root) })
			clock := newTestClock("2026-10-13T08:00:00Z")
			stageOneStore(t, root, 2)                     // generation 1
			store := openRotating(t, root, clock, 0, nil) // generation 2
			populate(t, store, 2, 5)
			clock.Set("2026-10-14T08:00:00Z")
			populate(t, store, 5, 8)
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if sidecarGone {
				if err := os.Remove(sidecarPathFor(root, true)); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, ".writer.lock"), []byte("1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if again, err := NewJSONLStoreWithOptions(root, rotationOptions(clock, 0, nil)); err == nil || !strings.Contains(err.Error(), refused) {
				if again != nil {
					_ = again.Close()
				}
				t.Fatalf("open with a rolled-back counter = %v, want %q", err, refused)
			}
		})
	}
}

// Readers and verification while the writer writes and rotates (R-12, run with
// -race): every snapshot is whole and agrees with the log it was taken from.
func TestReadersAndVerifySeeWholeSnapshotsWhileTheWriterRotates(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeWritable(t, root) })
	clock := newTestClock("2026-10-13T08:00:00Z")
	stageOneStore(t, root, 3)
	store := openRotating(t, root, clock, 0, nil)
	ctx := context.Background()

	var acked atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%6 == 5 {
				clock.Advance(26 * time.Hour)
			}
			n := 2000 + i
			if err := store.SaveTask(ctx, indexedTestTask(n, indexTestNow.Add(time.Duration(n)*time.Second))); err != nil {
				t.Errorf("write %d: %v", n, err)
				return
			}
			acked.Store(int64(i + 1))
			time.Sleep(time.Millisecond)
		}
	}()

	deadline := time.Now().Add(4 * time.Second)
	opens := 0
	for time.Now().Before(deadline) {
		known := acked.Load()
		reader, err := NewJSONLReader(root)
		if err != nil {
			t.Fatalf("reader open: %v", err)
		}
		for i := int64(0); i < known; i += 7 {
			if _, err := reader.GetTask(ctx, indexedTestTask(int(2000+i), indexTestNow).TaskID); err != nil {
				t.Fatalf("a task acknowledged before the reader opened is not in its snapshot: %v", err)
			}
		}
		_ = reader.Close()
		report, err := VerifyTaskStore(ctx, root)
		if err != nil || !report.OK {
			t.Fatalf("verify while the writer rotates: %v %+v", err, report)
		}
		opens++
	}
	close(stop)
	wg.Wait()
	if opens < 3 {
		t.Fatalf("only %d snapshots were taken", opens)
	}
	if got := segmentNames(t, root); len(got) < 3 {
		t.Fatalf("the writer rotated only %d times", len(got))
	}
	requireSegmentedStoreEqualsFullBuild(t, store, root)
	_ = modulecore.TaskID("")
}

// The sidecar of layout v2 is replaced the way the sidecar of layout v1 is: a
// stop at any point of the replacement leaves either the old or the new file
// whole, and the next open restores from it.
func TestCheckpointOfLayoutV2StoppedAtEveryStageLeavesAnIndexEqualToTheLog(t *testing.T) {
	for _, stage := range checkpointStages {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			t.Cleanup(func() { makeWritable(t, root) })
			clock := newTestClock("2026-10-13T08:00:00Z")
			stageOneStore(t, root, 4)
			store := openRotating(t, root, clock, 0, nil)
			populate(t, store, 4, 8)
			clock.Set("2026-10-14T08:00:00Z")
			populate(t, store, 8, 11)
			if err := store.idx.ck.checkpointNow("test"); err != nil {
				t.Fatal(err)
			}
			old, err := os.ReadFile(sidecarPathFor(root, true))
			if err != nil {
				t.Fatal(err)
			}
			populate(t, store, 11, 14)

			store.idx.ck.fp = crashAt(stage)
			if !didCrash(func() { _ = store.idx.ck.checkpointNow("crash") }) {
				t.Fatalf("the failpoint %s did not fire", stage)
			}
			current, err := os.ReadFile(sidecarPathFor(root, true))
			if err != nil {
				t.Fatalf("the sidecar path holds nothing after a stop at %s: %v", stage, err)
			}
			if _, _, err := decodeSidecar(current); err != nil {
				t.Fatalf("the sidecar in place after a stop at %s is not whole: %v", stage, err)
			}
			replaced := stage == "post-rename" || stage == "dir-synced"
			if replaced == (string(current) == string(old)) {
				t.Fatalf("stopped at %s: replaced=%v but the file is %s", stage, replaced, map[bool]string{true: "the old one", false: "a new one"}[string(current) == string(old)])
			}
			if err := store.closeStore(false); err != nil {
				t.Fatal(err)
			}
			again := openRotating(t, root, clock, 0, nil)
			if stats, _ := again.IndexStats(); stats.Source != sourceSidecar {
				t.Fatalf("after a stop at %s the index came from %q (%s), want the intact sidecar", stage, stats.Source, stats.RebuildReason)
			}
			if fileExists(sidecarPathFor(root, true) + sidecarTmpSuffix) {
				t.Fatal("the temporary file of the stopped checkpoint survived the next open")
			}
			requireSegmentedStoreEqualsFullBuild(t, again, root)
		})
	}
}
