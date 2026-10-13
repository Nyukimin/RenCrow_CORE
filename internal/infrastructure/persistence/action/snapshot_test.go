package action

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"
	"unsafe"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestSnapshotReadsAreServedFromMemory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX file modes and a non-root user")
	}
	root := filepath.Join(t.TempDir(), "actions")
	store := openIndexed(t, root)
	actions, attempts := seedPairs(t, store, 12)
	requireIndexed(t, store)

	// The logs cannot be opened any more; stat still works, which is all the
	// freshness check of a read needs.
	for _, path := range []string{store.actionPath, store.attemptPath} {
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	}
	ctx := context.Background()
	got, err := store.GetAction(ctx, actions[3].ActionID)
	if err != nil || got.ActionID != actions[3].ActionID {
		t.Fatalf("GetAction from the snapshot = %+v, %v", got.ActionID, err)
	}
	if _, err := store.GetAttempt(ctx, attempts[3].AttemptID); err != nil {
		t.Fatalf("GetAttempt from the snapshot: %v", err)
	}
	listed, err := store.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: actions[3].ActionID})
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListAttempts from the snapshot = %d, %v", len(listed), err)
	}
	byRun, err := store.ListActions(ctx, domainaction.Filter{RunID: actions[3].RunID})
	if err != nil || len(byRun) != 4 {
		t.Fatalf("ListActions by Run from the snapshot = %d, %v; want the Run's 4 Actions", len(byRun), err)
	}
}

func TestSnapshotStatsDescribeTheSnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "actions")
	if _, ok := openFull(t, root).IndexStats(); ok {
		t.Fatal("a store opened without Index reports a snapshot")
	}
	store := openIndexed(t, root)
	seedPairs(t, store, 5)
	stats, ok := store.IndexStats()
	if !ok {
		t.Fatal("a store opened with Index reports no snapshot")
	}
	if stats.Actions != 5 || stats.Attempts != 5 {
		t.Fatalf("stats count %d Actions and %d Attempts, want 5 and 5", stats.Actions, stats.Attempts)
	}
	if stats.ActionBytes != fileSize(t, store.actionPath) || stats.AttemptBytes != fileSize(t, store.attemptPath) {
		t.Fatalf("stats cover %d and %d bytes, the logs are %d and %d", stats.ActionBytes, stats.AttemptBytes, fileSize(t, store.actionPath), fileSize(t, store.attemptPath))
	}
	if stats.Rebuilds != 0 || stats.Dirty || stats.Broken {
		t.Fatalf("a store that only ever followed its own commits reports %+v", stats)
	}
	requireSnapshotEqualsLog(t, store)
}

func TestListActionsSelectsByKindAndExactName(t *testing.T) {
	for name, open := range map[string]func(testing.TB, string) *JSONLStore{"full read": openFull, "snapshot": openIndexed} {
		t.Run(name, func(t *testing.T) {
			store := open(t, filepath.Join(t.TempDir(), "actions"))
			ctx := context.Background()
			task, run := modulecore.NewTaskID(), modulecore.NewRunID()
			specs := []struct {
				kind domainaction.Kind
				name string
			}{
				{domainaction.KindPlayback, "playback_receipt:a:u:0"},
				{domainaction.KindPlayback, "playback_receipt:a:u:1"},
				{domainaction.KindTool, "playback_receipt:a:u:0"},
				{domainaction.KindMemoryPromotion, "memory_promotion"},
				{domainaction.KindTool, ""},
				{domainaction.KindTool, " padded"},
			}
			for i, spec := range specs {
				action, attempt := newOpenPair(task, run, spec.kind, spec.name, supportBase.Add(time.Duration(i)*time.Second))
				if err := savePair(ctx, store, action, attempt); err != nil {
					t.Fatal(err)
				}
			}
			count := func(f domainaction.Filter) int {
				got, err := store.ListActions(ctx, f)
				if err != nil {
					t.Fatalf("ListActions(%+v): %v", f, err)
				}
				return len(got)
			}
			for _, tc := range []struct {
				label  string
				filter domainaction.Filter
				want   int
			}{
				{"kind alone", domainaction.Filter{Kind: domainaction.KindPlayback}, 2},
				{"kind and name", domainaction.Filter{Kind: domainaction.KindPlayback, Name: "playback_receipt:a:u:0"}, 1},
				{"name alone spans kinds", domainaction.Filter{Name: "playback_receipt:a:u:0"}, 2},
				{"no match", domainaction.Filter{Kind: domainaction.KindPlayback, Name: "playback_receipt:a:u:9"}, 0},
				{"name is not normalized", domainaction.Filter{Name: "padded"}, 0},
				{"padded name matches exactly", domainaction.Filter{Name: " padded"}, 1},
				{"kind with run and status", domainaction.Filter{RunID: run, Status: domainaction.StatusOpen, Kind: domainaction.KindTool}, 3},
				{"kind and limit", domainaction.Filter{Kind: domainaction.KindTool, Limit: 2}, 2},
			} {
				if got := count(tc.filter); got != tc.want {
					t.Errorf("%s: %d Actions, want %d", tc.label, got, tc.want)
				}
			}
			if _, err := store.ListActions(ctx, domainaction.Filter{Kind: "no_such_kind"}); err == nil {
				t.Error("an invalid Kind in a filter was accepted")
			}
		})
	}
}

// A write that reads the whole history would trip over a damaged old line; the
// snapshot's write does not read it, and says so by committing. The store opened
// without the snapshot refuses, as it always has. This is the accepted change of
// 5.4: damage to old bytes that keeps the file's size is found by reopening
// (every record is checked then) and by verify, not by the next transaction.
func TestSnapshotWriteDoesNotReadTheHistory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "actions")
	store := openIndexed(t, root)
	seedPairs(t, store, 6)
	oracleRoot := filepath.Join(t.TempDir(), "actions")
	copyStoreDir(t, root, oracleRoot)

	damage := func(path string) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// Break the first line's opening brace; the size does not change.
		data[0] = '['
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	damage(store.actionPath)
	damage(filepath.Join(oracleRoot, actionFilename))

	// Same size: the freshness check of the snapshot sees nothing to do.
	action, attempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "late", supportBase.Add(time.Hour))
	if err := savePair(context.Background(), store, action, attempt); err != nil {
		t.Fatalf("the snapshot's write failed on a damaged old line it never reads: %v", err)
	}
	full, err := NewJSONLStoreWithOptions(oracleRoot, OpenOptions{})
	if err == nil {
		_ = full
		t.Fatal("a store that reads the whole log opened over a damaged line")
	}
	if _, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true}); err == nil {
		t.Fatal("reopening over a damaged line succeeded; open must check every record")
	}
}

// The mirror of the test above for reads: what the snapshot holds is served
// without opening the log, so the cost of a read is the size of the answer.
func TestSnapshotReadAndWriteCostDoNotGrowWithHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("builds two stores of different size")
	}
	ctx := context.Background()
	measure := func(open func(testing.TB, string) *JSONLStore, n int) (read, write time.Duration) {
		store := open(t, filepath.Join(t.TempDir(), "actions"))
		task := modulecore.NewTaskID()
		// One transaction that saves n pairs keeps building the history cheap.
		var last domainaction.Action
		if err := store.Transaction(ctx, func(tx domainaction.Store) error {
			for i := 0; i < n; i++ {
				action, attempt := newOpenPair(task, modulecore.NewRunID(), domainaction.KindTool, "bulk", supportBase.Add(time.Duration(i)*time.Millisecond))
				if err := tx.SaveAction(ctx, action); err != nil {
					return err
				}
				if err := tx.SaveAttempt(ctx, attempt); err != nil {
					return err
				}
				last = action
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var reads, writes []time.Duration
		for i := 0; i < 5; i++ {
			started := time.Now()
			if _, err := store.GetAction(ctx, last.ActionID); err != nil {
				t.Fatal(err)
			}
			reads = append(reads, time.Since(started))
			action, attempt := newOpenPair(task, modulecore.NewRunID(), domainaction.KindTool, "probe", supportBase.Add(time.Hour+time.Duration(i)*time.Second))
			started = time.Now()
			if err := savePair(ctx, store, action, attempt); err != nil {
				t.Fatal(err)
			}
			writes = append(writes, time.Since(started))
		}
		sort.Slice(reads, func(i, j int) bool { return reads[i] < reads[j] })
		sort.Slice(writes, func(i, j int) bool { return writes[i] < writes[j] })
		return reads[len(reads)/2], writes[len(writes)/2]
	}
	const small, large = 500, 8000
	fullSmallRead, _ := measure(openFull, small)
	fullLargeRead, _ := measure(openFull, large)
	snapSmallRead, _ := measure(openIndexed, small)
	snapLargeRead, _ := measure(openIndexed, large)
	t.Logf("read p50: full read %v -> %v, snapshot %v -> %v (history %dx)", fullSmallRead, fullLargeRead, snapSmallRead, snapLargeRead, large/small)
	if fullLargeRead < 3*fullSmallRead {
		t.Fatalf("the full read did not grow with the history (%v -> %v); the test measures nothing", fullSmallRead, fullLargeRead)
	}
	if snapLargeRead*10 > fullLargeRead {
		t.Fatalf("a read from the snapshot (%v) is not at least 10x cheaper than the full read (%v) on a %d-Action history", snapLargeRead, fullLargeRead, large)
	}
	if snapLargeRead > 5*time.Millisecond {
		t.Fatalf("a read from the snapshot took %v on a %d-Action history", snapLargeRead, large)
	}
}

func TestSnapshotOpenFailsClosedOnAnInvalidHistory(t *testing.T) {
	ctx := context.Background()
	build := func(t *testing.T) (string, domainaction.Action, domainaction.Attempt) {
		root := filepath.Join(t.TempDir(), "actions")
		store := openFull(t, root)
		action, attempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "x", supportBase)
		if err := savePair(ctx, store, action, attempt); err != nil {
			t.Fatal(err)
		}
		return root, action, attempt
	}
	appendLine := func(t *testing.T, path string, v any) {
		t.Helper()
		appendJSONLine(t, path, v)
	}
	cases := map[string]func(t *testing.T, root string, action domainaction.Action, attempt domainaction.Attempt){
		"undecodable line": func(t *testing.T, root string, _ domainaction.Action, _ domainaction.Attempt) {
			f, err := os.OpenFile(filepath.Join(root, actionFilename), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := f.WriteString("{not json}\n"); err != nil {
				t.Fatal(err)
			}
		},
		"attempt of an Action that does not exist": func(t *testing.T, root string, _ domainaction.Action, attempt domainaction.Attempt) {
			stray := attempt
			stray.AttemptID = modulecore.NewAttemptID()
			stray.ActionID = modulecore.NewActionID()
			appendLine(t, filepath.Join(root, attemptFilename), stray)
		},
		"two running attempts of one Action": func(t *testing.T, root string, _ domainaction.Action, attempt domainaction.Attempt) {
			second := attempt
			second.AttemptID = modulecore.NewAttemptID()
			appendLine(t, filepath.Join(root, attemptFilename), second)
		},
		"immutable field changed": func(t *testing.T, root string, action domainaction.Action, _ domainaction.Attempt) {
			changed := action
			changed.RunID = modulecore.NewRunID()
			appendLine(t, filepath.Join(root, actionFilename), changed)
		},
		"current attempt that does not exist": func(t *testing.T, root string, action domainaction.Action, _ domainaction.Attempt) {
			changed := action
			changed.CurrentAttemptID = modulecore.NewAttemptID()
			appendLine(t, filepath.Join(root, actionFilename), changed)
		},
		"unterminated last line": func(t *testing.T, root string, action domainaction.Action, _ domainaction.Attempt) {
			f, err := os.OpenFile(filepath.Join(root, actionFilename), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			data := bytes.TrimSuffix(mustJSONLine(t, action), []byte("\n"))
			if _, err := f.Write(data); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, damage := range cases {
		t.Run(name, func(t *testing.T) {
			root, action, attempt := build(t)
			damage(t, root, action, attempt)
			if _, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true}); err == nil {
				t.Fatal("the snapshot was built over an invalid history")
			}
		})
	}
}

func TestSnapshotMatchesTheLogAfterReopen(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "actions")
	store := openIndexed(t, root)
	actions, attempts := seedPairs(t, store, 9)
	closedAction, closedAttempt := closePair(t, actions[2], attempts[2], supportBase.Add(time.Hour), domainaction.StatusSucceeded, domainaction.AttemptStatusSucceeded)
	if err := savePair(ctx, store, closedAction, closedAttempt); err != nil {
		t.Fatal(err)
	}
	requireSnapshotEqualsLog(t, store)
	reopened := openIndexed(t, root)
	requireSnapshotEqualsLog(t, reopened)
	requireSameAnswers(t, openFull(t, root), reopened)
	requireSameAnswers(t, openFull(t, root), store)
}

func TestSnapshotRejectsWhatTheFullReadRejects(t *testing.T) {
	ctx := context.Background()
	for name, open := range map[string]func(testing.TB, string) *JSONLStore{"full read": openFull, "snapshot": openIndexed} {
		t.Run(name, func(t *testing.T) {
			store := open(t, filepath.Join(t.TempDir(), "actions"))
			action, attempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "x", supportBase)
			if err := savePair(ctx, store, action, attempt); err != nil {
				t.Fatal(err)
			}
			before := fileSize(t, store.actionPath) + fileSize(t, store.attemptPath)

			err := store.Transaction(ctx, func(tx domainaction.Store) error {
				// Closing the Action while its Attempt still runs leaves a store no
				// reader may see.
				closed, closeErr := action.Close(domainaction.StatusSucceeded, supportBase.Add(time.Minute), "")
				if closeErr != nil {
					return closeErr
				}
				return tx.SaveAction(ctx, closed)
			})
			if err == nil {
				t.Fatal("a terminal Action with a running current Attempt was committed")
			}
			if after := fileSize(t, store.actionPath) + fileSize(t, store.attemptPath); after != before {
				t.Fatalf("a rejected transaction changed the logs (%d -> %d bytes)", before, after)
			}
			if _, err := store.GetAction(ctx, action.ActionID); err != nil {
				t.Fatalf("the store does not answer after a rejected transaction: %v", err)
			}
			wantErr := errors.New("callback failed")
			if err := store.Transaction(ctx, func(domainaction.Store) error { return wantErr }); !errors.Is(err, wantErr) {
				t.Fatalf("callback error = %v", err)
			}
		})
	}
}

func TestSnapshotTransactionSeesItsOwnWritesOnTopOfTheSnapshot(t *testing.T) {
	ctx := context.Background()
	store := openIndexed(t, filepath.Join(t.TempDir(), "actions"))
	task, run := modulecore.NewTaskID(), modulecore.NewRunID()
	first, firstAttempt := newOpenPair(task, run, domainaction.KindTool, "a", supportBase)
	if err := savePair(ctx, store, first, firstAttempt); err != nil {
		t.Fatal(err)
	}
	second, secondAttempt := newOpenPair(task, run, domainaction.KindTool, "b", supportBase.Add(time.Second))
	err := store.Transaction(ctx, func(tx domainaction.Store) error {
		if err := tx.SaveAction(ctx, second); err != nil {
			return err
		}
		if err := tx.SaveAttempt(ctx, secondAttempt); err != nil {
			return err
		}
		// The Action committed earlier and the one saved just now are both listed
		// for the Run, in creation order; the first is closed in this transaction,
		// so it no longer shows under Status open.
		closedFirst, closedAttempt := closePair(t, first, firstAttempt, supportBase.Add(time.Minute), domainaction.StatusSucceeded, domainaction.AttemptStatusSucceeded)
		if err := tx.SaveAttempt(ctx, closedAttempt); err != nil {
			return err
		}
		if err := tx.SaveAction(ctx, closedFirst); err != nil {
			return err
		}
		all, err := tx.ListActions(ctx, domainaction.Filter{RunID: run})
		if err != nil || len(all) != 2 || all[0].ActionID != first.ActionID || all[1].ActionID != second.ActionID {
			t.Errorf("Actions of the Run inside the transaction = %d, %v", len(all), err)
		}
		open, err := tx.ListActions(ctx, domainaction.Filter{Status: domainaction.StatusOpen})
		if err != nil || len(open) != 1 || open[0].ActionID != second.ActionID {
			t.Errorf("open Actions inside the transaction = %d, %v; want only the new one", len(open), err)
		}
		attempts, err := tx.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: first.ActionID})
		if err != nil || len(attempts) != 1 || attempts[0].Status != domainaction.AttemptStatusSucceeded {
			t.Errorf("Attempts of the closed Action inside the transaction = %+v, %v", attempts, err)
		}
		got, err := tx.GetAttempt(ctx, secondAttempt.AttemptID)
		if err != nil || got.AttemptID != secondAttempt.AttemptID {
			t.Errorf("GetAttempt of an Attempt saved in the transaction = %v, %v", got.AttemptID, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	requireSnapshotEqualsLog(t, store)
}

// The snapshot keeps one copy of an ID that several of its records name and one
// copy of each small enumerated value, which is what keeps its heap near the size
// of the decoded records and not above it (AC-11).
func TestSnapshotKeepsOneCopyOfSharedStrings(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "actions")
	store := openIndexed(t, root)
	actions, attempts := seedPairs(t, store, 6)
	closedAction, closedAttempt := closePair(t, actions[0], attempts[0], supportBase.Add(time.Hour), domainaction.StatusSucceeded, domainaction.AttemptStatusSucceeded)
	if err := savePair(ctx, store, closedAction, closedAttempt); err != nil {
		t.Fatal(err)
	}
	// Rebuild from the log, the way an open does, so every record was decoded from a line.
	reopened := openIndexed(t, root)
	st := reopened.idx.st
	for id, action := range st.actions.latest {
		for _, attemptID := range st.attemptsOf[id] {
			attempt := st.attempts.latest[attemptID]
			if unsafe.StringData(string(attempt.ActionID)) != unsafe.StringData(string(action.ActionID)) {
				t.Fatalf("an Attempt keeps its own copy of the ID of its Action")
			}
			if attempt.AttemptID == action.CurrentAttemptID && unsafe.StringData(string(attempt.AttemptID)) != unsafe.StringData(string(action.CurrentAttemptID)) {
				t.Fatalf("an Attempt keeps its own copy of the ID its Action names as current")
			}
		}
	}
	first, second := st.actions.latest[actions[1].ActionID], st.actions.latest[actions[2].ActionID]
	if unsafe.StringData(string(first.Kind)) != unsafe.StringData(string(second.Kind)) || unsafe.StringData(string(first.Status)) != unsafe.StringData(string(second.Status)) {
		t.Fatal("two Actions of one Kind and Status keep separate copies of those words")
	}
	// Sharing changes no value.
	requireSnapshotEqualsLog(t, reopened)
	requireSameAnswers(t, openFull(t, root), reopened)

	// A value that is different every time is not kept for sharing without bound,
	// and filling the table of names does not stop an enumerated value from being
	// shared.
	for i := 0; i < maxNameValues+50; i++ {
		st.name(fmt.Sprintf("playback_receipt:%d", i))
	}
	for i := 0; i < maxEnumValues+50; i++ {
		st.enum(fmt.Sprintf("odd-%d", i))
	}
	if len(st.names) > maxNameValues || len(st.enums) > maxEnumValues {
		t.Fatalf("the tables of shared words grew to %d names and %d enumerated values", len(st.names), len(st.enums))
	}
	// An enumerated value that arrives after the table of names filled up is
	// still shared with the copy the content already holds.
	if unsafe.StringData(st.enum(string(domainaction.StatusOpen))) != unsafe.StringData(string(first.Status)) {
		t.Fatal("an enumerated value is no longer shared once the table of names is full")
	}
}
