package action

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// The snapshot covers a number of bytes of each log. Whenever a log is not
// exactly that long, or a write ended without a known commit boundary, the next
// operation reads the logs in full again before it answers.

func TestSnapshotRebuildsWhenAnotherHandleWrote(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "actions")
	a := openIndexed(t, root)
	seedPairs(t, a, 3)
	b := openIndexed(t, root)

	action, attempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "from-b", supportBase.Add(time.Hour))
	if err := savePair(ctx, b, action, attempt); err != nil {
		t.Fatal(err)
	}
	if got, err := a.GetAction(ctx, action.ActionID); err != nil || got.ActionID != action.ActionID {
		t.Fatalf("handle A does not see what handle B committed: %v", err)
	}
	if stats, _ := a.IndexStats(); stats.Rebuilds != 1 {
		t.Fatalf("handle A rebuilt %d times, want 1", stats.Rebuilds)
	}
	for i := 0; i < 3; i++ {
		if _, err := a.GetAttempt(ctx, attempt.AttemptID); err != nil {
			t.Fatal(err)
		}
	}
	if stats, _ := a.IndexStats(); stats.Rebuilds != 1 {
		t.Fatalf("handle A rebuilt again (%d) with nothing new in the logs", stats.Rebuilds)
	}
	// And the other way round: A writes, B must not answer from before.
	second, secondAttempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "from-a", supportBase.Add(2*time.Hour))
	if err := savePair(ctx, a, second, secondAttempt); err != nil {
		t.Fatal(err)
	}
	if _, err := b.GetAction(ctx, second.ActionID); err != nil {
		t.Fatalf("handle B does not see what handle A committed: %v", err)
	}
	requireSnapshotEqualsLog(t, a)
	requireSnapshotEqualsLog(t, b)
}

func TestSnapshotAbsorbsAnAppendMadeOutsideTheStore(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "actions")
	store := openIndexed(t, root)
	seedPairs(t, store, 2)

	action, attempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "outside", supportBase.Add(time.Hour))
	appendJSONLine(t, store.actionPath, action)
	appendJSONLine(t, store.attemptPath, attempt)

	if _, err := store.GetAction(ctx, action.ActionID); err != nil {
		t.Fatalf("an Action appended to the log is not visible: %v", err)
	}
	if stats, _ := store.IndexStats(); stats.Rebuilds != 1 {
		t.Fatalf("rebuilds = %d, want 1", stats.Rebuilds)
	}
	requireSnapshotEqualsLog(t, store)
}

func TestSnapshotWriteSeesAnAppendMadeOutsideTheStore(t *testing.T) {
	ctx := context.Background()
	store := openIndexed(t, filepath.Join(t.TempDir(), "actions"))
	task, run := modulecore.NewTaskID(), modulecore.NewRunID()
	outside, outsideAttempt := newOpenPair(task, run, domainaction.KindDelegation, domainaction.NativeDelegationActionName, supportBase)
	appendJSONLine(t, store.actionPath, outside)
	appendJSONLine(t, store.attemptPath, nativeAttempt(outside, supportBase, nil))
	_ = outsideAttempt

	// A second native delegation in the same Task Run is refused only if the
	// transaction knows about the first one, which nobody told the snapshot about.
	other, otherAttempt := newOpenPair(task, run, domainaction.KindDelegation, domainaction.NativeDelegationActionName, supportBase.Add(time.Second))
	err := store.Transaction(ctx, func(tx domainaction.Store) error {
		if err := tx.SaveAction(ctx, other); err != nil {
			return err
		}
		return tx.SaveAttempt(ctx, nativeAttempt(other, supportBase.Add(time.Second), nil))
	})
	_ = otherAttempt
	if err == nil {
		t.Fatal("a second native delegation for one Task Run was committed")
	}
}

func TestSnapshotFailsClosedOnAnInvalidAppendAndRecoversWhenItIsGone(t *testing.T) {
	ctx := context.Background()
	store := openIndexed(t, filepath.Join(t.TempDir(), "actions"))
	actions, _ := seedPairs(t, store, 2)
	good, err := os.ReadFile(store.actionPath)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(store.actionPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{not json}\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	if _, err := store.GetAction(ctx, actions[0].ActionID); err == nil {
		t.Fatal("a read answered from a snapshot that the log has outgrown with an invalid record")
	}
	action, attempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "x", supportBase.Add(time.Hour))
	if err := savePair(ctx, store, action, attempt); err == nil {
		t.Fatal("a write was committed on top of an invalid log")
	}
	if err := os.WriteFile(store.actionPath, good, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetAction(ctx, actions[0].ActionID); err != nil {
		t.Fatalf("the store does not recover once the log is valid again: %v", err)
	}
	requireSnapshotEqualsLog(t, store)
}

func TestSnapshotRebuildsWhenALogIsShorterThanItCovers(t *testing.T) {
	ctx := context.Background()
	store := openIndexed(t, filepath.Join(t.TempDir(), "actions"))
	actions, _ := seedPairs(t, store, 4)
	// Drop the last pair from both logs: a valid, shorter history.
	for _, path := range []string{store.actionPath, store.attemptPath} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		rows := lines(data)
		short := []byte{}
		for _, row := range rows[:len(rows)-1] {
			short = append(short, row...)
			short = append(short, '\n')
		}
		if err := os.WriteFile(path, short, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.GetAction(ctx, actions[3].ActionID); !errors.Is(err, domainaction.ErrNotFound) {
		t.Fatalf("an Action that is no longer in the log is still answered: %v", err)
	}
	if _, err := store.GetAction(ctx, actions[2].ActionID); err != nil {
		t.Fatal(err)
	}
	requireSnapshotEqualsLog(t, store)
}

func TestHookThatCannotAbsorbACommitMarksTheSnapshotBroken(t *testing.T) {
	ctx := context.Background()
	store := openIndexed(t, filepath.Join(t.TempDir(), "actions"))
	ix := requireIndexed(t, store)
	seedPairs(t, store, 2)

	// An append that does not continue the covered prefix cannot be applied.
	ix.onCommit(ctx, 0, []jsonlbatch.CommittedAppend{{Name: actionFilename, Offset: 1, Payload: []byte("{}\n")}})
	if stats, _ := store.IndexStats(); !stats.Broken {
		t.Fatal("the snapshot is not marked broken after a commit it could not absorb")
	}
	action, attempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "after", supportBase.Add(time.Hour))
	if err := savePair(ctx, store, action, attempt); err != nil {
		t.Fatal(err)
	}
	stats, _ := store.IndexStats()
	if stats.Broken || stats.Rebuilds != 1 {
		t.Fatalf("after the next transaction: %+v, want a single rebuild and a sound snapshot", stats)
	}
	requireSnapshotEqualsLog(t, store)
}

// A panic in the hook unwinds through the Store (its lock is released by defer)
// and the process may carry on, as it does when an HTTP server recovers a
// handler. The snapshot must then be rebuilt, never trusted.
func TestPanicWhileAbsorbingACommitIsRepairedByTheNextOperation(t *testing.T) {
	for _, stage := range []string{"hook-start", "hook-after-actions"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			root := filepath.Join(t.TempDir(), "actions")
			store := openIndexed(t, root)
			ix := requireIndexed(t, store)
			seedPairs(t, store, 2)

			ix.hookFault = func(at string) {
				if at == stage {
					panic("simulated bug in the hook")
				}
			}
			action, attempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "committed", supportBase.Add(time.Hour))
			func() {
				defer func() { _ = recover() }()
				_ = savePair(ctx, store, action, attempt)
			}()
			ix.hookFault = nil

			// The commit happened before the hook ran: the Action is in the log, so
			// the store must answer with it, from a rebuilt snapshot.
			if _, err := store.GetAction(ctx, action.ActionID); err != nil {
				t.Fatalf("an Action committed before the hook panicked is not visible: %v", err)
			}
			if stats, _ := store.IndexStats(); stats.Rebuilds != 1 || stats.Broken {
				t.Fatalf("after the panic: %+v, want one rebuild and a sound snapshot", stats)
			}
			requireSnapshotEqualsLog(t, store)
			// The OS lock was released: another handle can write.
			other := openIndexed(t, root)
			next, nextAttempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "next", supportBase.Add(2*time.Hour))
			if err := savePair(ctx, other, next, nextAttempt); err != nil {
				t.Fatalf("the lock was not released by the panic: %v", err)
			}
		})
	}
}

func TestEmptyAndFailedTransactionsLeaveTheSnapshotAlone(t *testing.T) {
	ctx := context.Background()
	store := openIndexed(t, filepath.Join(t.TempDir(), "actions"))
	seedPairs(t, store, 3)
	before, _ := store.IndexStats()
	if err := store.Transaction(ctx, func(domainaction.Store) error { return nil }); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("no")
	if err := store.Transaction(ctx, func(tx domainaction.Store) error {
		action, _ := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "never", supportBase)
		if err := tx.SaveAction(ctx, action); err != nil {
			return err
		}
		return failure
	}); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	after, _ := store.IndexStats()
	if after != before {
		t.Fatalf("stats changed from %+v to %+v although nothing was committed", before, after)
	}
	requireSnapshotEqualsLog(t, store)
}

// ErrCommitUncertain leaves one of two shapes behind. Neither lets the snapshot
// move on its own: it is marked dirty and the next lock holder decides from the
// WAL.
func TestCommitUncertainShapesAreResolvedFromTheLog(t *testing.T) {
	uncertain := errors.Join(jsonlbatch.ErrCommitUncertain, jsonlbatch.ErrRecoveryRequired)
	ctx := context.Background()
	pending := func(t *testing.T, store *JSONLStore, root string, commit bool) (domainaction.Action, domainaction.Attempt) {
		t.Helper()
		action, attempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "uncertain", supportBase.Add(time.Hour))
		craftWAL(t, root, map[string][]byte{actionFilename: mustJSONLine(t, action), attemptFilename: mustJSONLine(t, attempt)}, commit)
		requireIndexed(t, store).noteWriteResult(uncertain)
		if stats, _ := store.IndexStats(); !stats.Dirty {
			t.Fatal("the snapshot is not dirty after a write that ended without a known commit boundary")
		}
		return action, attempt
	}

	t.Run("commit record never written is rolled back by the next writer", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "actions")
		store := openIndexed(t, root)
		kept, _ := seedPairs(t, store, 1)
		lost, _ := pending(t, store, root, false)

		if _, err := store.GetAction(ctx, kept[0].ActionID); !errors.Is(err, jsonlbatch.ErrRecoveryRequired) {
			t.Fatalf("read while the WAL is pending = %v, want ErrRecoveryRequired (fail closed, like the full read)", err)
		}
		next, nextAttempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "next", supportBase.Add(2*time.Hour))
		if err := savePair(ctx, store, next, nextAttempt); err != nil {
			t.Fatalf("write after recovery: %v", err)
		}
		if _, err := store.GetAction(ctx, lost.ActionID); !errors.Is(err, domainaction.ErrNotFound) {
			t.Fatalf("the rolled-back Action = %v, want not found", err)
		}
		if _, err := store.GetAction(ctx, next.ActionID); err != nil {
			t.Fatal(err)
		}
		if stats, _ := store.IndexStats(); stats.Dirty {
			t.Fatal("the snapshot is still dirty after the WAL was settled")
		}
		requireSnapshotEqualsLog(t, store)
	})

	t.Run("commit record written is seen by the next read", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "actions")
		store := openIndexed(t, root)
		seedPairs(t, store, 1)
		committed, _ := pending(t, store, root, true)
		got, err := store.GetAction(ctx, committed.ActionID)
		if err != nil || got.ActionID != committed.ActionID {
			t.Fatalf("committed-but-unannounced Action = %v, %v", got.ActionID, err)
		}
		requireSnapshotEqualsLog(t, store)
	})

	t.Run("commit record written is seen by the next write", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "actions")
		store := openIndexed(t, root)
		seedPairs(t, store, 1)
		committed, _ := pending(t, store, root, true)
		// Creating the uncertain Action again must find it; this is the double
		// create the snapshot must never allow.
		err := store.Transaction(ctx, func(tx domainaction.Store) error {
			_, err := tx.GetAction(ctx, committed.ActionID)
			return err
		})
		if err != nil {
			t.Fatalf("transaction after an uncertain commit does not see the committed Action: %v", err)
		}
		requireSnapshotEqualsLog(t, store)
	})

	t.Run("nothing was appended", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "actions")
		store := openIndexed(t, root)
		kept, _ := seedPairs(t, store, 2)
		requireIndexed(t, store).noteWriteResult(uncertain)
		if _, err := store.GetAction(ctx, kept[1].ActionID); err != nil {
			t.Fatal(err)
		}
		stats, _ := store.IndexStats()
		if stats.Dirty || stats.Rebuilds != 0 {
			t.Fatalf("stats = %+v; a log that did not move needs no rebuild, only the dirty mark cleared", stats)
		}
	})
}
