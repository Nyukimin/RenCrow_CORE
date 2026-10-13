package action

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/segmentname"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Fault injection for segments (design 8, AC-13). Wherever the writer stops - in
// the migration, in a rotation, inside a commit - the next open finds every
// acknowledged record, the snapshot equals a full read of the logs, the store
// goes on, and no byte of a closed segment has changed.

func TestMigrationStoppedAtEveryStepLosesNothingAndCanBeFinished(t *testing.T) {
	const n = 10
	base := t.TempDir()
	pairs := makePairs(t, n+2, rotStart)
	plainStore(t, base, pairs, n)
	original := fingerprint(t, base)
	originalSizes, err := segmentFileSizes(base)
	if err != nil {
		t.Fatal(err)
	}

	points := 0
	{
		root := copyOf(t, base)
		clock := newRotClock(rotStart.Add(time.Hour))
		if _, err := NewJSONLStoreWithOptions(root, rotOptions(clock, 0, func(string) { points++ })); err != nil {
			t.Fatal(err)
		}
	}
	if points < 6 {
		t.Fatalf("only %d failpoints fired in a migration", points)
	}

	for stop := 1; stop <= points; stop++ {
		for _, mode := range []string{"migrate again", "open without rotation"} {
			t.Run(fmt.Sprintf("stop-%d/%s", stop, mode), func(t *testing.T) {
				root := copyOf(t, base)
				clock := newRotClock(rotStart.Add(time.Hour))
				seen := 0
				crashed := didCrash(func() {
					_, _ = NewJSONLStoreWithOptions(root, rotOptions(clock, 0, func(stage string) {
						if seen++; seen == stop {
							panic(simulatedCrash{stage})
						}
					}))
				})
				if !crashed {
					t.Fatal("the failpoint did not fire")
				}
				opts := rotOptions(clock, 0, nil)
				if mode == "open without rotation" {
					opts = OpenOptions{now: clock.Now}
				}
				store, err := NewJSONLStoreWithOptions(root, opts)
				if err != nil {
					t.Fatalf("open after the stop: %v", err)
				}
				requireHolds(t, store, pairs, 0, n)
				writeAll(t, store, pairs, n, n+2)
				requireHolds(t, store, pairs, 0, n+2)
				requireSnapshotEqualsLogOrPlain(t, store)
				report, err := VerifyActionStore(context.Background(), root)
				if err != nil || !report.OK {
					t.Fatalf("verify after the stop: ok=%v err=%v problems=%v", report.OK, err, report.Problems)
				}
				if layout, _ := readLayout(root); layout.v2 {
					if layout.frozen != originalSizes {
						t.Fatalf("LAYOUT records %v, the original was %v", layout.frozen, originalSizes)
					}
				}
				after := fingerprint(t, root)
				for _, name := range []string{actionFilename, attemptFilename} {
					if layout, _ := readLayout(root); layout.v2 && after[name] != original[name] {
						t.Fatalf("segment 0 file %s changed", name)
					}
				}
			})
		}
	}
}

func TestRotationStoppedAtEveryStepLosesNothingAndTheStoreGoesOn(t *testing.T) {
	base, pairs, _ := segmentedStore(t)
	next := rotStart.Add(72 * time.Hour) // the fixture ended on 2026-10-15
	extra := makePairs(t, 4, next)
	ctx := context.Background()

	points := 0
	armed := false
	{
		root := copyOf(t, base)
		clock := newRotClock(next)
		store, err := NewJSONLStoreWithOptions(root, rotOptions(clock, 0, func(string) {
			if armed {
				points++
			}
		}))
		if err != nil {
			t.Fatal(err)
		}
		armed = true
		if err := extra[0].create(ctx, store); err != nil {
			t.Fatal(err)
		}
		armed = false
	}
	if points < 6 {
		t.Fatalf("only %d failpoints fired in a rotation", points)
	}

	for stop := 1; stop <= points; stop++ {
		t.Run(fmt.Sprintf("stop-%d", stop), func(t *testing.T) {
			root := copyOf(t, base)
			closedBefore := fingerprint(t, root)
			clock := newRotClock(next)
			seen, armed := 0, false
			store, err := NewJSONLStoreWithOptions(root, rotOptions(clock, 0, func(stage string) {
				if !armed {
					return
				}
				if seen++; seen == stop {
					panic(simulatedCrash{stage})
				}
			}))
			if err != nil {
				t.Fatal(err)
			}
			armed = true
			if !didCrash(func() { _ = extra[0].create(ctx, store) }) {
				t.Fatal("the failpoint did not fire")
			}

			reopened, err := NewJSONLStoreWithOptions(root, rotOptions(clock, 0, nil))
			if err != nil {
				t.Fatalf("open after the stop: %v", err)
			}
			requireHolds(t, reopened, pairs, 0, 12)
			if _, err := reopened.GetAction(ctx, extra[0].action.ActionID); err == nil {
				t.Fatal("the transaction that stopped before it appended a byte exists")
			}
			writeAll(t, reopened, extra, 0, 4)
			requireHolds(t, reopened, extra, 0, 4)
			requireSnapshotEqualsLog(t, reopened)

			names := segmentNames(t, root)
			for i := 1; i < len(names); i++ {
				if names[i] <= names[i-1] {
					t.Fatalf("segments are not in order: %v", names)
				}
			}
			for _, name := range names[:len(names)-1] {
				record, ok, err := readSealed(segmentDir(root, name))
				actual, sizeErr := segmentFileSizes(segmentDir(root, name))
				if err != nil || !ok || sizeErr != nil || record != actual {
					t.Fatalf("closed segment %s: record %v (ok=%v err=%v), files %v", name, record, ok, err, actual)
				}
			}
			requireUnchanged(t, filterPrefix(closedBefore, "logs"+string(filepath.Separator)+"20261013-01", "logs"+string(filepath.Separator)+"20261014-01"),
				fingerprint(t, root), false, "closed segments")
			report, err := VerifyActionStore(ctx, root)
			if err != nil || !report.OK {
				t.Fatalf("verify: ok=%v err=%v problems=%v", report.OK, err, report.Problems)
			}
		})
	}
}

// filterPrefix keeps the entries whose path starts with one of the prefixes.
func filterPrefix[V any](m map[string]V, prefixes ...string) map[string]V {
	out := map[string]V{}
	for path, v := range m {
		for _, prefix := range prefixes {
			if strings.HasPrefix(path, prefix+string(filepath.Separator)) {
				out[path] = v
			}
		}
	}
	return out
}

// A writer that stopped inside jsonlbatch.Write leaves a WAL the next writer
// repairs; the segment the writer was in is the newest one.
func TestAnInterruptedCommitInTheNewestSegmentIsRepairedByItsWAL(t *testing.T) {
	type shape struct {
		name      string
		prepare   func(t *testing.T, dir string, p pair)
		persisted bool
	}
	appends := func(t *testing.T, p pair) map[string][]byte {
		return map[string][]byte{actionFilename: mustJSONLine(t, p.action), attemptFilename: mustJSONLine(t, p.attempt)}
	}
	shapes := []shape{
		{"prepare record only", func(t *testing.T, dir string, p pair) { craftPrepareOnly(t, dir, appends(t, p)) }, false},
		{"bytes appended, no commit record", func(t *testing.T, dir string, p pair) { craftWAL(t, dir, appends(t, p), false) }, false},
		{"commit record written", func(t *testing.T, dir string, p pair) { craftWAL(t, dir, appends(t, p), true) }, true},
	}
	for _, sh := range shapes {
		t.Run(sh.name, func(t *testing.T) {
			base, pairs, clock := segmentedStore(t)
			root := copyOf(t, base)
			interrupted := makePairs(t, 1, rotStart.Add(80*time.Hour))[0]
			newest := segmentNames(t, root)
			sh.prepare(t, segmentDir(root, newest[len(newest)-1]), interrupted)
			before := fingerprint(t, segmentDir(root, newest[0]))

			store, err := NewJSONLStoreWithOptions(root, rotOptions(clock, 0, nil))
			if err != nil {
				t.Fatalf("open after the interrupted commit: %v", err)
			}
			requireHolds(t, store, pairs, 0, 12)
			_, err = store.GetAction(context.Background(), interrupted.action.ActionID)
			if sh.persisted && err != nil {
				t.Fatalf("a transaction with a whole commit record is gone: %v", err)
			}
			if !sh.persisted && err == nil {
				t.Fatal("a transaction without a whole commit record survived")
			}
			requireSnapshotEqualsLog(t, store)
			requireUnchanged(t, before, fingerprint(t, segmentDir(root, newest[0])), true, "oldest closed segment")
		})
	}
}

// A closed segment whose WAL is not settled is not what a crash leaves behind: a
// rotation settles it first. Repairing it would cut bytes something else wrote.
func TestAClosedSegmentWithAnUnsettledWALIsNotRepaired(t *testing.T) {
	base, _, clock := segmentedStore(t)
	root := copyOf(t, base)
	names := segmentNames(t, root)
	p := makePairs(t, 1, rotStart.Add(80*time.Hour))[0]
	makeAllWritable(segmentDir(root, names[0])) // the closed segments are read-only
	craftWAL(t, segmentDir(root, names[0]), map[string][]byte{actionFilename: mustJSONLine(t, p.action)}, false)
	// The record is still in the files, so the size check would also object; the
	// refusal must be about the WAL and must leave the bytes where they are.
	before := fingerprint(t, root)
	if _, err := NewJSONLStoreWithOptions(root, rotOptions(clock, 0, nil)); err == nil {
		t.Fatal("a store with a closed segment holding a pending transaction was opened")
	}
	requireUnchanged(t, before, fingerprint(t, root), true, "refused open")
}

const (
	segCrashEnvRoot  = "ACTSEG_CRASH_ROOT"
	segCrashEnvStage = "ACTSEG_CRASH_STAGE"
)

// segCrashStages are the places the child stops: the rotation that precedes its
// write (the valve is one byte, so every write after the first rotates), and
// inside the commit.
var segCrashStages = []struct {
	name      string
	committed bool
}{
	{"rotate-settled", false},
	{"rotate-sealed", false},
	{"rotate-created", false},
	{"rotate-switched", false},
	{"callback", false},
	{"hook-start", true},
	{"hook-after-actions", true},
	{"hook-done", true},
}

func TestHelperCrashProcessWithSegments(t *testing.T) {
	root, stage := os.Getenv(segCrashEnvRoot), os.Getenv(segCrashEnvStage)
	if root == "" || stage == "" {
		t.Skip("child of the segment crash tests")
	}
	ctx := context.Background()
	armed := false
	exitAt := func(at string) {
		if armed && at == stage {
			os.Exit(crashExitCode)
		}
	}
	store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true, Rotation: true, RotationSizeValve: 1, failpoint: exitAt})
	if err != nil {
		fmt.Println("open failed:", err)
		os.Exit(2)
	}
	ack, err := os.OpenFile(filepath.Join(root, "..", "acked.txt"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		os.Exit(2)
	}
	defer ack.Close()
	for i := 0; i < 3; i++ {
		action, attempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "acked", supportBase.Add(time.Duration(i)*time.Second))
		if err := savePair(ctx, store, action, attempt); err != nil {
			fmt.Println("write failed:", err)
			os.Exit(2)
		}
		fmt.Fprintln(ack, action.ActionID)
		_ = ack.Sync()
	}
	store.idx.hookFault = exitAt
	doomed, doomedAttempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "doomed", supportBase.Add(time.Hour))
	fmt.Fprintln(ack, "maybe", doomed.ActionID)
	_ = ack.Sync()
	armed = true
	_ = store.Transaction(ctx, func(tx domainaction.Store) error {
		if err := tx.SaveAction(ctx, doomed); err != nil {
			return err
		}
		if err := tx.SaveAttempt(ctx, doomedAttempt); err != nil {
			return err
		}
		exitAt("callback")
		return nil
	})
	fmt.Println("the crash point was not reached")
	os.Exit(3)
}

func TestSegmentedStoreSurvivesAProcessKilledAtEveryStage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("child process exit codes are checked the POSIX way")
	}
	for _, stage := range segCrashStages {
		t.Run(stage.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "actions")
			t.Cleanup(func() { makeAllWritable(base) })
			cmd := exec.Command(os.Args[0], "-test.run=^TestHelperCrashProcessWithSegments$")
			cmd.Env = append(os.Environ(), segCrashEnvRoot+"="+root, segCrashEnvStage+"="+stage.name)
			out, err := cmd.CombinedOutput()
			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != crashExitCode {
				t.Fatalf("child did not stop at %s: %v\n%s", stage.name, err, out)
			}
			acked, maybe := readAcks(t, filepath.Join(base, "acked.txt"))
			if len(acked) != 3 {
				t.Fatalf("child acknowledged %d Actions, want 3", len(acked))
			}
			if names := segmentNames(t, root); len(names) < 2 {
				t.Fatalf("the child made %v; with a one byte valve it rotates at every write", names)
			}

			snap, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true, Rotation: true})
			if err != nil {
				t.Fatalf("open after the stop at %s: %v", stage.name, err)
			}
			ctx := context.Background()
			for _, id := range acked {
				if _, err := snap.GetAction(ctx, modulecore.ActionID(id)); err != nil {
					t.Fatalf("Action %s was acknowledged but is gone after a stop at %s: %v", id, stage.name, err)
				}
			}
			_, err = snap.GetAction(ctx, modulecore.ActionID(maybe))
			if stage.committed && err != nil {
				t.Fatalf("a stop at %s is after the commit record, yet the Action is gone: %v", stage.name, err)
			}
			if !stage.committed && err == nil {
				t.Fatalf("a stop at %s is before anything was appended, yet the Action exists", stage.name)
			}
			requireSnapshotEqualsLog(t, snap)
			action, attempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "after", supportBase.Add(2*time.Hour))
			if err := savePair(ctx, snap, action, attempt); err != nil {
				t.Fatalf("write after the stop at %s: %v", stage.name, err)
			}
			requireSnapshotEqualsLog(t, snap)
			report, err := VerifyActionStore(ctx, root)
			if err != nil || !report.OK {
				t.Fatalf("verify after the stop at %s: ok=%v err=%v problems=%v", stage.name, report.OK, err, report.Problems)
			}
			names := segmentNames(t, root)
			for _, name := range names {
				if !segmentname.IsName(name) {
					t.Fatalf("stray directory %q under logs/", name)
				}
			}
		})
	}
}
