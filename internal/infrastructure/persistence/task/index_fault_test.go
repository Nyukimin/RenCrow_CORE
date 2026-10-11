package task

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Fault injection for the sidecar (spec 14.2, AC-13): wherever the writer stops
// and whatever happened to the sidecar, the next open yields an index equal to
// a build from the log, with every committed record present.

// checkpointStages are the points of a sidecar replacement, in order.
var checkpointStages = []string{"tmp-created", "tmp-partial", "tmp-written", "tmp-synced", "pre-rename", "post-rename", "dir-synced"}

type simulatedCrash struct{ stage string }

// crashAt returns a failpoint that stops the writer at stage by panicking with
// simulatedCrash. Unwinding is the only difference from a dead process: nothing
// is cleaned up on purpose, so the temporary file stays where it was.
func crashAt(stage string) func(string) {
	return func(s string) {
		if s == stage {
			panic(simulatedCrash{stage})
		}
	}
}

func didCrash(fn func()) (crashed bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(simulatedCrash); !ok {
				panic(r)
			}
			crashed = true
		}
	}()
	fn()
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// requireAllCommittedTasksPresent checks the "no loss" half of the property
// against a list of IDs that were acknowledged as committed.
func requireAllCommittedTasksPresent(t testing.TB, store *JSONLStore, acked []modulecore.TaskID) {
	t.Helper()
	for _, id := range acked {
		if _, err := store.GetTask(context.Background(), id); err != nil {
			t.Fatalf("task %s was acknowledged as committed but is gone: %v", id, err)
		}
	}
}

func TestCheckpointStoppedAtEveryStageLeavesAnIndexEqualToTheLog(t *testing.T) {
	for _, stage := range checkpointStages {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			store := openPersisted(t, root, manualPolicy(), nil)
			populate(t, store, 0, 10)
			if err := store.idx.ck.checkpointNow("test"); err != nil {
				t.Fatal(err)
			}
			old, err := os.ReadFile(sidecarFile(root))
			if err != nil {
				t.Fatal(err)
			}
			populate(t, store, 10, 20)

			store.idx.ck.fp = crashAt(stage)
			if !didCrash(func() { _ = store.idx.ck.checkpointNow("crash") }) {
				t.Fatalf("the failpoint %s did not fire", stage)
			}
			tmp := sidecarFile(root) + sidecarTmpSuffix
			current, err := os.ReadFile(sidecarFile(root))
			if err != nil {
				t.Fatalf("the sidecar path holds nothing after a stop at %s: %v", stage, err)
			}
			replaced := stage == "post-rename" || stage == "dir-synced"
			switch {
			case replaced && bytes.Equal(current, old):
				t.Fatalf("stopped at %s, after the rename, but the old sidecar is still in place", stage)
			case !replaced && !bytes.Equal(current, old):
				t.Fatalf("stopped at %s, before the rename, but the sidecar changed", stage)
			case replaced && fileExists(tmp):
				t.Fatalf("stopped at %s, after the rename, yet a temporary file remains", stage)
			case !replaced && !fileExists(tmp):
				t.Fatalf("stopped at %s, before the rename, yet no temporary file exists", stage)
			}
			if _, _, err := decodeSidecar(current); err != nil {
				t.Fatalf("the sidecar in place after a stop at %s is not whole: %v", stage, err)
			}

			// The process is gone: release its files and lock without running
			// anything the real crash would not have run.
			if err := store.closeStore(false); err != nil {
				t.Fatal(err)
			}
			again := openPersisted(t, root, manualPolicy(), nil)
			if stats, _ := again.IndexStats(); stats.Source != sourceSidecar {
				t.Fatalf("after a stop at %s the index came from %q (%s), want the intact sidecar", stage, stats.Source, stats.RebuildReason)
			}
			if fileExists(tmp) {
				t.Fatalf("the temporary file of the stopped checkpoint survived the next open")
			}
			requireStoreEqualsFullBuild(t, again, root)
			requireSidecarEqualsLog(t, root)
		})
	}
}

func TestEverySingleBitFlipAndTruncationOfASidecarIsHarmless(t *testing.T) {
	root := t.TempDir()
	store := openPersisted(t, root, manualPolicy(), nil)
	populate(t, store, 0, 12)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	good, err := os.ReadFile(sidecarFile(root))
	if err != nil {
		t.Fatal(err)
	}
	damage := map[string][]byte{}
	for i := 0; i < 140; i++ {
		flipped := append([]byte(nil), good...)
		at := len(good)*i/140 + i%7
		flipped[at] ^= byte(1) << (i % 8)
		damage["bit "+strconv.Itoa(at)] = flipped
	}
	for i := 0; i < 40; i++ {
		damage["cut at "+strconv.Itoa(len(good)*i/40)] = good[:len(good)*i/40]
	}
	damage["appended garbage"] = append(append([]byte(nil), good...), "garbage"...)
	damage["only zeros"] = make([]byte, len(good))
	for name, bad := range damage {
		if err := os.WriteFile(sidecarFile(root), bad, 0o600); err != nil {
			t.Fatal(err)
		}
		reopened := openPersisted(t, root, manualPolicy(), nil)
		stats, _ := reopened.IndexStats()
		if stats.Source != sourceRebuilt {
			t.Fatalf("%s: index source = %q, want a rebuild", name, stats.Source)
		}
		requireStoreEqualsFullBuild(t, reopened, root)
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// A transaction that reached the log but not the index, whether its commit was
// recorded or not, never leaves the index behind or ahead of the log.
func TestCrashAroundACommitLeavesTheIndexEqualToTheLog(t *testing.T) {
	for _, committed := range []bool{true, false} {
		t.Run(fmt.Sprintf("committed=%t", committed), func(t *testing.T) {
			root := t.TempDir()
			store := openPersisted(t, root, manualPolicy(), nil)
			populate(t, store, 0, 6)
			if err := store.Close(); err != nil { // sidecar covers everything so far
				t.Fatal(err)
			}
			orphan := indexedTestTask(900, indexTestNow.Add(time.Hour))
			craftWAL(t, root, map[string][]byte{stateFilename: marshalLine(t, orphan)}, committed)

			again := openPersisted(t, root, manualPolicy(), nil)
			stats, _ := again.IndexStats()
			if stats.Source != sourceSidecar {
				t.Fatalf("source=%q (%s), want the sidecar to survive a torn transaction", stats.Source, stats.RebuildReason)
			}
			_, err := again.GetTask(context.Background(), orphan.TaskID)
			if committed && err != nil {
				t.Fatalf("a committed transaction is missing after the restart: %v", err)
			}
			if !committed && err == nil {
				t.Fatal("a transaction that never committed is visible after the restart")
			}
			requireStoreEqualsFullBuild(t, again, root)
		})
	}
}

// The helper below is the child of the process-level tests: the test binary runs
// itself with RENCROW_INDEX_FAULT_MODE set and dies where the mode says. The
// parent then looks at what is on disk. os.Exit skips every deferred call, so
// what the child leaves behind is what a killed process leaves behind.
const (
	faultModeEnv  = "RENCROW_INDEX_FAULT_MODE"
	faultRootEnv  = "RENCROW_INDEX_FAULT_ROOT"
	faultStageEnv = "RENCROW_INDEX_FAULT_STAGE"
	faultExitCode = 87
)

func TestIndexFaultChildProcess(t *testing.T) {
	mode := os.Getenv(faultModeEnv)
	if mode == "" {
		t.Skip("helper process of the fault injection tests")
	}
	root, stage := os.Getenv(faultRootEnv), os.Getenv(faultStageEnv)
	die := func(code int, format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		os.Exit(code)
	}
	killAt := func(s string) {
		if s == stage {
			os.Exit(faultExitCode)
		}
	}
	switch mode {
	case "open":
		// The first checkpoint of a store without a sidecar dies at stage.
		store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true, Persist: true, checkpoint: manualPolicy(), failpoint: killAt})
		if err != nil {
			die(3, "open: %v", err)
		}
		_ = store
		die(4, "the failpoint %s was not reached", stage)
	case "checkpoint":
		store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true, Persist: true, checkpoint: manualPolicy()})
		if err != nil {
			die(3, "open: %v", err)
		}
		generation, _ := store.WriterGeneration()
		for n := 100; n < 130; n++ {
			if err := store.SaveTask(context.Background(), indexedTestTask(n, indexTestNow.Add(time.Duration(n)*time.Second))); err != nil {
				die(3, "save: %v", err)
			}
		}
		_ = generation
		store.idx.ck.fp = killAt
		_ = store.idx.ck.checkpointNow("child")
		die(4, "the failpoint %s was not reached", stage)
	case "stress":
		// Commits and checkpoints as fast as possible; every acknowledged Task is
		// announced on stdout so the parent knows what must survive.
		store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true, Persist: true, checkpoint: &checkpointPolicy{interval: time.Millisecond, lineThreshold: 2, retryBase: time.Millisecond, retryMax: 2 * time.Millisecond}})
		if err != nil {
			die(3, "open: %v", err)
		}
		generation, _ := store.WriterGeneration()
		base, _ := strconv.Atoi(stage)
		for n := base; ; n++ {
			task := indexedTestTask(n, indexTestNow.Add(time.Duration(n)*time.Second))
			if err := store.SaveTask(context.Background(), task); err != nil {
				die(3, "save: %v", err)
			}
			fmt.Fprintf(os.Stdout, "ack %s\n", task.TaskID)
			if n%3 == 0 {
				run := indexedTestRun(task, n, generation, indexTestNow)
				if err := store.SaveRun(context.Background(), run); err != nil {
					die(3, "run: %v", err)
				}
			}
		}
	}
	die(5, "unknown mode %q", mode)
}

func runFaultChild(t *testing.T, mode, root, stage string) (exitCode int, stderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIndexFaultChildProcess$")
	cmd.Env = append(os.Environ(), faultModeEnv+"="+mode, faultRootEnv+"="+root, faultStageEnv+"="+stage)
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	err := cmd.Run()
	if err == nil {
		return 0, errOut.String()
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), errOut.String()
	}
	t.Fatalf("run child: %v", err)
	return -1, ""
}

func TestProcessKilledDuringTheFirstCheckpointAtOpen(t *testing.T) {
	for _, stage := range checkpointStages {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			seed := openPersisted(t, root, manualPolicy(), nil)
			populate(t, seed, 0, 8)
			if err := seed.closeStore(false); err != nil { // log without a sidecar worth the name
				t.Fatal(err)
			}
			if err := os.Remove(sidecarFile(root)); err != nil {
				t.Fatal(err)
			}
			code, stderr := runFaultChild(t, "open", root, stage)
			if code != faultExitCode {
				t.Fatalf("child exit = %d, want %d (killed at %s)\n%s", code, faultExitCode, stage, stderr)
			}
			again := openPersisted(t, root, manualPolicy(), nil)
			stats, _ := again.IndexStats()
			replaced := stage == "post-rename" || stage == "dir-synced"
			if replaced && stats.Source != sourceSidecar {
				t.Fatalf("killed after the rename, yet the next open rebuilt (%s)", stats.RebuildReason)
			}
			if !replaced && (stats.Source != sourceRebuilt || stats.RebuildReason != rebuildMissing) {
				t.Fatalf("killed before the rename: source=%q reason=%q, want rebuilt/missing", stats.Source, stats.RebuildReason)
			}
			if fileExists(sidecarFile(root) + sidecarTmpSuffix) {
				t.Fatal("the temporary file of the killed checkpoint survived the next open")
			}
			requireStoreEqualsFullBuild(t, again, root)
			requireSidecarEqualsLog(t, root)
		})
	}
}

func TestProcessKilledDuringACheckpointOfARunningStore(t *testing.T) {
	for _, stage := range []string{"tmp-partial", "tmp-synced", "pre-rename", "post-rename"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			seed := openPersisted(t, root, manualPolicy(), nil)
			populate(t, seed, 0, 8)
			if err := seed.Close(); err != nil {
				t.Fatal(err)
			}
			code, stderr := runFaultChild(t, "checkpoint", root, stage)
			if code != faultExitCode {
				t.Fatalf("child exit = %d, want %d\n%s", code, faultExitCode, stderr)
			}
			again := openPersisted(t, root, manualPolicy(), nil)
			stats, _ := again.IndexStats()
			if stats.Source != sourceSidecar {
				t.Fatalf("source=%q (%s), want the sidecar (old or new) to be usable", stats.Source, stats.RebuildReason)
			}
			requireStoreEqualsFullBuild(t, again, root)
			for n := 100; n < 130; n++ { // the Tasks the child committed before it died
				if _, err := again.GetTask(context.Background(), indexedTestTask(n, indexTestNow).TaskID); err != nil {
					t.Fatalf("committed task %d is missing: %v", n, err)
				}
			}
		})
	}
}

// Real SIGKILLs (TerminateProcess on Windows) of a writer that commits and
// checkpoints continuously: every Task it acknowledged is still there, and the
// index equals the log, after each of several kills in a row.
func TestProcessKilledWhileCommittingAndCheckpointing(t *testing.T) {
	root := t.TempDir()
	var acked []modulecore.TaskID
	next := 1000
	for round, minAcks := range []int{5, 40, 120, 20} {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIndexFaultChildProcess$")
		cmd.Env = append(os.Environ(), faultModeEnv+"=stress", faultRootEnv+"="+root, faultStageEnv+"="+strconv.Itoa(next))
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(out)
		count := 0
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "ack ") {
				continue
			}
			acked = append(acked, modulecore.TaskID(strings.TrimPrefix(line, "ack ")))
			next++
			if count++; count >= minAcks+rand.New(rand.NewSource(int64(round))).Intn(7) {
				break
			}
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		cancel()
		if count < minAcks {
			t.Fatalf("round %d: the child acknowledged only %d tasks before it ended", round, count)
		}

		store := openPersisted(t, root, manualPolicy(), nil)
		stats, _ := store.IndexStats()
		t.Logf("round %d: %d acknowledged so far, restart: source=%s reason=%q replayed=%d tasks=%d", round, len(acked), stats.Source, stats.RebuildReason, store.idx.replayedLines, stats.Tasks)
		requireAllCommittedTasksPresent(t, store, acked)
		requireStoreEqualsFullBuild(t, store, root)
		if fileExists(sidecarFile(root) + sidecarTmpSuffix) {
			t.Fatalf("round %d: temporary file survived the restart", round)
		}
		if err := store.closeStore(false); err != nil {
			t.Fatal(err)
		}
		// Tasks the child wrote but did not get to acknowledge are in the log
		// too; the next round numbers past all of them.
		next += 1000
	}
}
