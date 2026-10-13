package action

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/segmentname"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// TestMeasureActionStoreAcceptance measures the acceptance criteria of the
// ActionStore design (docs/調査/*ActionStore全件読み*設計案.md, 1.2) on a copy of a
// real store: the open (build) time, the heap the snapshot keeps, and the p50 of a
// write transaction (and its exec and commit parts), of a read, and of one Tool
// call's worth of work, with the store opened without the snapshot and with it.
//
// It is skipped unless ACTIDX_MEASURE_ROOT names a directory that holds
// action_state.jsonl and action_attempt.jsonl, or ACTIDX_MEASURE_SYNTH gives a
// number of Actions to generate a store of the shape the production store has
// (most of them short Memory-promotion Actions, two lines each in both logs). It
// never touches the directory ACTIDX_MEASURE_ROOT names: the two logs are copied
// into a temporary directory first and the measurement appends to the copy. ACTIDX_MEASURE_ROUNDS (default 10) is the number of Tool
// calls measured per mode. The numbers are logged with t.Logf, one line per mode;
// run it with -v. Measure on the host the store lives on, with the host's usual
// load: the criteria are ratios to the same host's full read.
//
// The "rotation" mode opens the copy with Rotation, which moves it to segments
// (the open then includes the migration), and measures the writes into the new,
// empty segment. When ACTIDX_MEASURE_SEGMENT_FILL_BYTES is set, the
// "rotation_loaded" mode measures the writes into a segment that already holds
// about that many bytes (up to the 8 MiB valve), which is what a commit costs late
// in a busy day. Both rotation modes end with a rotation by day and report how
// long it stopped the store.
func TestMeasureActionStoreAcceptance(t *testing.T) {
	src := os.Getenv("ACTIDX_MEASURE_ROOT")
	if src == "" {
		n, _ := strconv.Atoi(os.Getenv("ACTIDX_MEASURE_SYNTH"))
		if n < 1 {
			t.Skip("set ACTIDX_MEASURE_ROOT to a copy of an Action store (or ACTIDX_MEASURE_SYNTH to a count of Actions) to measure it")
		}
		src = filepath.Join(t.TempDir(), "synthetic")
		writeSyntheticStore(t, src, n)
	}
	rounds := 10
	if v := os.Getenv("ACTIDX_MEASURE_ROUNDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("ACTIDX_MEASURE_ROUNDS = %q", v)
		}
		rounds = n
	}
	type measureMode struct {
		name     string
		index    bool
		rotation bool
		fill     int64
	}
	modes := []measureMode{{"full", false, false, 0}, {"snapshot", true, false, 0}, {"rotation", true, true, 0}}
	if v := os.Getenv("ACTIDX_MEASURE_SEGMENT_FILL_BYTES"); v != "" {
		fill, err := strconv.ParseInt(v, 10, 64)
		if err != nil || fill < 1 || fill >= DefaultRotationSizeValve {
			t.Fatalf("ACTIDX_MEASURE_SEGMENT_FILL_BYTES = %q; want a size under the %d byte valve", v, DefaultRotationSizeValve)
		}
		modes = append(modes, measureMode{"rotation_loaded", true, true, fill})
	}
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "actions")
			t.Cleanup(func() { makeAllWritable(dir) })
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{actionFilename, attemptFilename} {
				copyFileForMeasure(t, filepath.Join(src, name), filepath.Join(dir, name))
			}
			if mode.fill > 0 {
				prepareLoadedSegment(t, dir, mode.fill)
			}
			heapBefore := heapAlloc()
			openStarted := time.Now()
			var pauses []time.Duration
			store, err := newJSONLStore(dir, OpenOptions{Index: mode.index, Rotation: mode.rotation, rotationPause: func(d time.Duration) { pauses = append(pauses, d) }}, nil)
			if err != nil {
				t.Fatalf("open the copy: %v", err)
			}
			opened := time.Since(openStarted)
			heapAfter := heapAlloc()

			var writes []jsonlbatch.TxObservation
			store.batch.SetTxObserver(func(_ context.Context, obs jsonlbatch.TxObservation) {
				if obs.Kind == jsonlbatch.TxKindWrite {
					writes = append(writes, obs)
				}
			})
			manager := actionmanager.New(store)
			ctx := context.Background()
			task, run := modulecore.NewTaskID(), modulecore.NewRunID()

			var toolCall, createWall, completeWall, checkWall, getWall []time.Duration
			for i := 0; i < rounds; i++ {
				started := time.Now()
				action, attempt, err := manager.CreateAction(ctx, actionmanager.CreateInput{TaskID: task, RunID: run, Kind: domainaction.KindTool, Name: "measure"})
				if err != nil {
					t.Fatal(err)
				}
				created := time.Since(started)
				createWall = append(createWall, created)

				// The check a Tool call makes between creating and ending its Action:
				// one read transaction naming the Action and the Attempt.
				started = time.Now()
				if err := store.ReadTransaction(ctx, func(tx domainaction.Store) error {
					if _, err := tx.GetAction(ctx, action.ActionID); err != nil {
						return err
					}
					_, err := tx.GetAttempt(ctx, attempt.AttemptID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				checked := time.Since(started)
				checkWall = append(checkWall, checked)

				started = time.Now()
				if _, err := manager.GetAction(ctx, action.ActionID); err != nil {
					t.Fatal(err)
				}
				getWall = append(getWall, time.Since(started))

				started = time.Now()
				if _, _, err := manager.CompleteAttempt(ctx, action.ActionID, attempt.AttemptID, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "measured"); err != nil {
					t.Fatal(err)
				}
				completed := time.Since(started)
				completeWall = append(completeWall, completed)
				toolCall = append(toolCall, created+checked+completed)
			}

			// The questions the startup recoveries and the Playback receipt ask, each
			// timed over the whole history: the open Actions, the native delegations, the
			// Actions of one Run, and an exact Kind and name.
			timeList := func(filter domainaction.Filter) time.Duration {
				var runs []time.Duration
				for i := 0; i < 5; i++ {
					started := time.Now()
					if _, err := store.ListActions(ctx, filter); err != nil {
						t.Fatal(err)
					}
					runs = append(runs, time.Since(started))
				}
				return p50(runs)
			}
			listOpen := timeList(domainaction.Filter{Status: domainaction.StatusOpen})
			listNative := timeList(domainaction.Filter{Kind: domainaction.KindDelegation, Name: domainaction.NativeDelegationActionName})
			listRun := timeList(domainaction.Filter{RunID: run})
			listKindName := timeList(domainaction.Filter{Kind: domainaction.KindPlayback, Name: "playback_receipt:x:y:0"})

			var execs, commits, totals []time.Duration
			for _, w := range writes {
				execs = append(execs, w.Exec)
				commits = append(commits, w.Commit)
				totals = append(totals, w.LockWait+w.Settle+w.Exec+w.Commit)
			}
			stats := IndexStats{}
			if s, ok := store.IndexStats(); ok {
				stats = s
			}
			segmentBytes := int64(0)
			if mode.rotation {
				segmentBytes = store.idx.currentBytes()
				// A rotation by day: the clock says it is tomorrow, and the next write
				// closes the segment. The pause is how long the store was stopped.
				tomorrow := time.Now().Add(26 * time.Hour)
				store.rot.now = func() time.Time { return tomorrow }
				if _, _, err := manager.CreateAction(ctx, actionmanager.CreateInput{TaskID: task, RunID: run, Kind: domainaction.KindTool, Name: "measure"}); err != nil {
					t.Fatal(err)
				}
				if len(pauses) != 1 {
					t.Fatalf("the day rotation happened %d times", len(pauses))
				}
			}
			writeWall := append(append([]time.Duration(nil), createWall...), completeWall...)
			t.Logf("MEASURE mode=%s action_lines=%s action_bytes=%d attempt_bytes=%d open=%v heap_delta=%.1fMB "+
				"write_wall_p50=%v write_total_p50=%v write_exec_p50=%v write_commit_p50=%v read_tx_p50=%v get_action_p50=%v tool_call_p50=%v list_open_p50=%v list_native_p50=%v list_run_p50=%v list_kind_name_p50=%v build=%v rebuilds=%d rounds=%d segments=%d segment_bytes=%d rotate_pause=%v",
				mode.name, countLines(t, filepath.Join(src, actionFilename)), fileSizeForMeasure(t, filepath.Join(src, actionFilename)), fileSizeForMeasure(t, filepath.Join(src, attemptFilename)),
				opened.Round(time.Millisecond), float64(int64(heapAfter)-int64(heapBefore))/(1<<20),
				p50(writeWall), p50(totals), p50(execs), p50(commits), p50(checkWall), p50(getWall), p50(toolCall), listOpen, listNative, listRun, listKindName, stats.BuildDuration.Round(time.Millisecond), stats.Rebuilds, rounds, stats.Segments, segmentBytes, firstOrZero(pauses))
			if mode.index {
				requireSnapshotEqualsLog(t, store)
			}
		})
	}
}

func firstOrZero(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	return values[0].Round(time.Microsecond)
}

// prepareLoadedSegment turns the plain store in dir into a store on layout v2 whose
// only segment already holds about fill bytes of synthetic records: segment 0 is
// recorded and frozen as it is, and the segment of today is written by the same
// generator the synthetic store uses.
func prepareLoadedSegment(t *testing.T, dir string, fill int64) {
	t.Helper()
	frozen, err := segmentFileSizes(dir)
	if err != nil {
		t.Fatal(err)
	}
	segDir := segmentDir(dir, segmentname.Format(segmentname.UTCDay(time.Now()), 1))
	writeSyntheticStore(t, segDir, int(fill/1375)+1) // about 1.4 KB of the two logs per Action
	if _, err := writeFileAtomically(layoutPath(dir), formatSizeRecord(layoutVersionLine, layoutFrozenWord, frozen), nil, "layout"); err != nil {
		t.Fatal(err)
	}
	if err := freezeSegmentZero(dir); err != nil {
		t.Fatal(err)
	}
}

func p50(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

func heapAlloc() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

func copyFileForMeasure(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func fileSizeForMeasure(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

// countLines counts the records of an Action log without decoding them.
func countLines(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	return fmt.Sprintf("%d", n)
}

// writeSyntheticStore writes n Actions in the shape of the production store: each
// Action has a creation line and a terminal line, each Attempt a running line and a
// terminal line, so the logs hold 2n records each and about 1.3 KB per Action.
func writeSyntheticStore(t *testing.T, dir string, n int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	actions, err := os.Create(filepath.Join(dir, actionFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer actions.Close()
	attempts, err := os.Create(filepath.Join(dir, attemptFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer attempts.Close()
	kinds := []domainaction.Kind{domainaction.KindMemoryPromotion, domainaction.KindVerification, domainaction.KindLLM, domainaction.KindTTS}
	enc := func(f *os.File, v any) {
		if _, err := f.Write(mustJSONLine(t, v)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i++ {
		kind := kinds[0]
		if i%19 == 0 {
			kind = kinds[1+i%3]
		}
		at := supportBase.Add(time.Duration(i) * 30 * time.Second)
		action, attempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), kind, string(kind), at)
		enc(actions, action)
		enc(attempts, attempt)
		closedAction, closedAttempt := closePair(t, action, attempt, at.Add(4*time.Second), domainaction.StatusSucceeded, domainaction.AttemptStatusSucceeded)
		closedAction.Summary, closedAttempt.Summary = "completed without an error; no further detail was recorded for this Action", "completed without an error; no further detail was recorded for this Attempt"
		enc(actions, closedAction)
		enc(attempts, closedAttempt)
	}
}
