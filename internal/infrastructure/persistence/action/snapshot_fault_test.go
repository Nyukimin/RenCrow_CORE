package action

import (
	"bufio"
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
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Fault injection (spec 8 and AC-13). Wherever the writer stops, the next open
// finds every acknowledged record, the snapshot built from the log equals a full
// read of it, and the store keeps working.
//
// A process that dies leaves its files and nothing else, so the stops that can
// be placed inside this package are real process exits (os.Exit in a child). The
// stops inside jsonlbatch.Write (after the prepare record, after the append) are
// the WAL shapes that remain, written by hand.

const (
	crashEnvRoot  = "ACTIDX_CRASH_ROOT"
	crashEnvStage = "ACTIDX_CRASH_STAGE"
	crashExitCode = 77
)

// crashStages are the places the child stops, in the order a transaction passes
// them.
var crashStages = []string{
	"callback",           // the callback saved its records and returns nothing yet: no byte was appended
	"hook-start",         // the commit record is durable; the snapshot has heard nothing
	"hook-after-actions", // the Action log's bytes are applied, the Attempt log's are not
	"hook-done",          // the snapshot is up to date; the caller has not been told
}

// TestHelperCrashProcess is the child. It does nothing unless the parent set
// the environment.
func TestHelperCrashProcess(t *testing.T) {
	root, stage := os.Getenv(crashEnvRoot), os.Getenv(crashEnvStage)
	if root == "" || stage == "" {
		t.Skip("child of the crash tests")
	}
	ctx := context.Background()
	store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true})
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
	crash := func(at string) {
		if at == stage {
			os.Exit(crashExitCode)
		}
	}
	store.idx.hookFault = crash
	doomed, doomedAttempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "doomed", supportBase.Add(time.Hour))
	fmt.Fprintln(ack, "maybe", doomed.ActionID)
	_ = ack.Sync()
	_ = store.Transaction(ctx, func(tx domainaction.Store) error {
		if err := tx.SaveAction(ctx, doomed); err != nil {
			return err
		}
		if err := tx.SaveAttempt(ctx, doomedAttempt); err != nil {
			return err
		}
		crash("callback")
		return nil
	})
	fmt.Println("the crash point was not reached")
	os.Exit(3)
}

func TestSnapshotSurvivesAProcessKilledAtEveryStage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("child process exit codes are checked the POSIX way")
	}
	for _, stage := range crashStages {
		t.Run(stage, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "actions")
			cmd := exec.Command(os.Args[0], "-test.run=^TestHelperCrashProcess$")
			cmd.Env = append(os.Environ(), crashEnvRoot+"="+root, crashEnvStage+"="+stage)
			out, err := cmd.CombinedOutput()
			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != crashExitCode {
				t.Fatalf("child did not stop at %s: %v\n%s", stage, err, out)
			}

			acked, maybe := readAcks(t, filepath.Join(base, "acked.txt"))
			if len(acked) != 3 {
				t.Fatalf("child acknowledged %d Actions, want 3", len(acked))
			}
			committedBeforeStop := stage != "callback"

			oracleRoot := filepath.Join(base, "oracle")
			copyStoreDir(t, root, oracleRoot)
			oracle := openFull(t, oracleRoot)
			snap := openIndexed(t, root)
			ctx := context.Background()
			for _, id := range acked {
				if _, err := snap.GetAction(ctx, modulecore.ActionID(id)); err != nil {
					t.Fatalf("Action %s was acknowledged but is gone after a stop at %s: %v", id, stage, err)
				}
			}
			_, err = snap.GetAction(ctx, modulecore.ActionID(maybe))
			if committedBeforeStop && err != nil {
				t.Fatalf("a stop at %s is after the commit record, yet the Action is gone: %v", stage, err)
			}
			if !committedBeforeStop && err == nil {
				t.Fatalf("a stop at %s is before anything was appended, yet the Action exists", stage)
			}
			requireSnapshotEqualsLog(t, snap)
			requireSameAnswers(t, oracle, snap)
			if stats, _ := snap.IndexStats(); stats.Rebuilds != 0 {
				t.Fatalf("the reopened snapshot needed %d rebuilds; opening is the build", stats.Rebuilds)
			}
			// The store carries on.
			action, attempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "after", supportBase.Add(2*time.Hour))
			if err := savePair(ctx, snap, action, attempt); err != nil {
				t.Fatalf("write after the stop at %s: %v", stage, err)
			}
			requireSnapshotEqualsLog(t, snap)
		})
	}
}

func readAcks(t *testing.T, path string) (acked []string, maybe string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		text := scanner.Text()
		if strings.HasPrefix(text, "maybe ") {
			maybe = strings.TrimPrefix(text, "maybe ")
			continue
		}
		acked = append(acked, text)
	}
	return acked, maybe
}

// A writer that stopped inside jsonlbatch.Write leaves a WAL the next writer
// repairs. The snapshot is built after that repair, so it must hold exactly the
// acknowledged history, and the interrupted transaction only if its commit
// record is whole.
func TestSnapshotOpenAfterAWriterStoppedInsideTheCommit(t *testing.T) {
	type shape struct {
		name      string
		prepare   func(t *testing.T, root string, action domainaction.Action, attempt domainaction.Attempt)
		persisted bool
	}
	appends := func(t *testing.T, action domainaction.Action, attempt domainaction.Attempt) map[string][]byte {
		return map[string][]byte{actionFilename: mustJSONLine(t, action), attemptFilename: mustJSONLine(t, attempt)}
	}
	shapes := []shape{
		{"prepare record only", func(t *testing.T, root string, action domainaction.Action, attempt domainaction.Attempt) {
			craftPrepareOnly(t, root, appends(t, action, attempt))
		}, false},
		{"bytes appended, no commit record", func(t *testing.T, root string, action domainaction.Action, attempt domainaction.Attempt) {
			craftWAL(t, root, appends(t, action, attempt), false)
		}, false},
		{"only the first file appended", func(t *testing.T, root string, action domainaction.Action, attempt domainaction.Attempt) {
			craftWAL(t, root, map[string][]byte{attemptFilename: mustJSONLine(t, attempt)}, false)
		}, false},
		{"commit record written", func(t *testing.T, root string, action domainaction.Action, attempt domainaction.Attempt) {
			craftWAL(t, root, appends(t, action, attempt), true)
		}, true},
		{"commit record torn", func(t *testing.T, root string, action domainaction.Action, attempt domainaction.Attempt) {
			craftWAL(t, root, appends(t, action, attempt), true)
			wal := filepath.Join(root, ".jsonlbatch.wal")
			data, err := os.ReadFile(wal)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(wal, data[:len(data)-10], 0o600); err != nil {
				t.Fatal(err)
			}
		}, false},
	}
	for _, sh := range shapes {
		t.Run(sh.name, func(t *testing.T) {
			ctx := context.Background()
			base := t.TempDir()
			root := filepath.Join(base, "actions")
			store := openFull(t, root)
			kept, _ := seedPairs(t, store, 3)
			interrupted, interruptedAttempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "interrupted", supportBase.Add(time.Hour))
			sh.prepare(t, root, interrupted, interruptedAttempt)

			oracleRoot := filepath.Join(base, "oracle")
			copyStoreDir(t, root, oracleRoot)
			oracle := openFull(t, oracleRoot)
			snap := openIndexed(t, root)
			for _, a := range kept {
				if _, err := snap.GetAction(ctx, a.ActionID); err != nil {
					t.Fatalf("acknowledged Action lost: %v", err)
				}
			}
			_, err := snap.GetAction(ctx, interrupted.ActionID)
			if sh.persisted && err != nil {
				t.Fatalf("a transaction with a whole commit record is gone: %v", err)
			}
			if !sh.persisted && err == nil {
				t.Fatal("a transaction without a whole commit record survived")
			}
			requireSnapshotEqualsLog(t, snap)
			requireSameAnswers(t, oracle, snap)
		})
	}
}

// craftPrepareOnly writes the prepare record of a transaction and nothing else:
// the writer stopped before it appended a byte.
func craftPrepareOnly(t testing.TB, root string, appends map[string][]byte) {
	t.Helper()
	// Appending then truncating the files back is the same on disk as never having
	// appended: the prepare record keeps the offsets and the hashes.
	sizes := map[string]int64{}
	for name := range appends {
		sizes[name] = fileSize(t, filepath.Join(root, name))
	}
	craftWAL(t, root, appends, false)
	for name, size := range sizes {
		if err := os.Truncate(filepath.Join(root, name), size); err != nil {
			t.Fatal(err)
		}
	}
}
