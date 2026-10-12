package task

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"os"
	"strconv"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Segment performance (spec 1.4 AC-2, AC-3, AC-5, AC-11, 14.3). The scale tests
// write a synthetic segment 0 of hundreds of megabytes and are skipped unless
// RENCROW_TASKINDEX_SCALE is set. They never use real records.

// segmentTxTimes runs `real` Succeed-shaped transactions against the store and
// returns the exec and commit phase of each.
func segmentTxTimes(t *testing.T, store *JSONLStore, idle func() modulecore.TaskID, base, real int) (exec, commit []time.Duration) {
	t.Helper()
	timings := &txTimings{}
	store.batch.SetTxObserver(timings.observe)
	ctx := context.Background()
	for i := 0; i < real; i++ {
		if err := succeedLikeTransaction(ctx, store, idle(), base+i, false); err != nil {
			t.Fatalf("transaction %d: %v", i, err)
		}
	}
	if len(timings.exec) != len(timings.commit) {
		t.Fatalf("%d exec and %d commit observations", len(timings.exec), len(timings.commit))
	}
	return timings.exec, timings.commit
}

func sumPerTx(exec, commit []time.Duration) []time.Duration {
	out := make([]time.Duration, len(exec))
	for i := range exec {
		out[i] = exec[i] + commit[i]
	}
	return out
}

// openSyntheticSegmented writes a synthetic segment 0 of n Tasks, opens it with
// rotation on (which freezes it and starts the first segment) and returns the
// store and the clock it reads.
func openSyntheticSegmented(t *testing.T, n int) (*JSONLStore, *testClock, string) {
	t.Helper()
	dir := t.TempDir()
	generateSyntheticLog(t, dir, n, 300, int64(n))
	clock := newTestClock("2026-10-20T00:00:00Z")
	store, err := NewJSONLStoreWithOptions(dir, OpenOptions{Index: true, Persist: true, Rotation: true, checkpoint: manualPolicy(), now: clock.Now})
	if err != nil {
		t.Fatalf("open the synthetic store with %d Tasks: %v", n, err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		makeWritable(t, dir)
	})
	return store, clock, dir
}

// TestSegmentCommitTimeDoesNotGrowWithHistory is AC-5 (and R-1): the same write
// transaction in the current segment takes about as long whether segment 0 holds
// 72 thousand or 720 thousand Tasks. Before rotation the commit hashed the whole
// file (about two seconds at 72 thousand).
func TestSegmentCommitTimeDoesNotGrowWithHistory(t *testing.T) {
	if os.Getenv("RENCROW_TASKINDEX_SCALE") == "" {
		t.Skip("set RENCROW_TASKINDEX_SCALE=1 to generate a synthetic 72k and 720k Task history")
	}
	small := scaleEnvInt(t, "RENCROW_TASKINDEX_SCALE_SMALL", 72000)
	large := scaleEnvInt(t, "RENCROW_TASKINDEX_SCALE_LARGE", 720000)
	real := scaleEnvInt(t, "RENCROW_TASKINDEX_SCALE_REAL_TXS", 60)

	measure := func(n int) (p50, p95, commit50 time.Duration, migrated time.Duration) {
		started := time.Now()
		store, _, _ := openSyntheticSegmented(t, n)
		migrated = time.Since(started)
		rng := rand.New(rand.NewSource(7))
		idle := func() modulecore.TaskID { return modulecore.TaskID("tsk_" + testUUID("scale", rng.Intn(n-300))) }
		segmentTxTimes(t, store, idle, 1_000_000, 10) // warm up
		exec, commit := segmentTxTimes(t, store, idle, 2_000_000, real)
		all := sumPerTx(exec, commit)
		return durationPercentile(all, 0.5), durationPercentile(all, 0.95), durationPercentile(commit, 0.5), migrated
	}
	smallP50, smallP95, smallCommit, smallOpen := measure(small)
	t.Logf("segment 0 = %d Tasks: open+migrate=%s, write tx (exec+commit) p50=%s p95=%s, commit p50=%s", small, smallOpen.Round(time.Millisecond), smallP50, smallP95, smallCommit)
	largeP50, largeP95, largeCommit, largeOpen := measure(large)
	t.Logf("segment 0 = %d Tasks: open+migrate=%s, write tx (exec+commit) p50=%s p95=%s, commit p50=%s", large, largeOpen.Round(time.Millisecond), largeP50, largeP95, largeCommit)
	ratio := float64(largeP50) / float64(smallP50)
	t.Logf("write tx p50 ratio large/small = %.2f (limit 1.2)", ratio)
	if ratio > 1.2 {
		t.Fatalf("the write transaction time grows with the history in segment 0: ratio %.2f", ratio)
	}
	if smallCommit > 100*time.Millisecond {
		t.Fatalf("commit p50 = %s in a fresh segment, want 100ms or less (AC-2)", smallCommit)
	}
}

// fillSegment appends valid records to the store's current segment, directly
// through its batch (so the index follows them exactly as it follows a commit),
// until the six files together hold about `bytes`. The share of each file follows
// the production mix.
func fillSegment(t *testing.T, store *JSONLStore, bytes int, seed int) {
	t.Helper()
	ctx := context.Background()
	rng := rand.New(rand.NewSource(int64(seed)))
	generation, err := store.WriterGeneration()
	if err != nil {
		t.Fatal(err)
	}
	line := func(v any) []byte {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return append(data, '\n')
	}
	pad := func(k int) string {
		buf := make([]byte, k)
		for i := range buf {
			buf[i] = 'a' + byte(rng.Intn(26))
		}
		return string(buf)
	}
	var written int
	for n := 0; written < bytes; {
		payloads := map[string][]byte{}
		var batchBytes int
		for batchBytes < 1<<20 && written+batchBytes < bytes {
			n++
			now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC).Add(time.Duration(seed*100000+n) * time.Second)
			task := domaintask.Task{
				TaskID: modulecore.TaskID("tsk_" + testUUID("fill", seed*1_000_000+n)), Title: "Memory Promotion", ModuleID: "core", Route: domaintask.RouteGeneral,
				Assignee: "Shiro", Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked,
				CreatedAt: now, UpdatedAt: now, Summary: pad(60),
			}
			add := func(name string, data []byte) {
				payloads[name] = append(payloads[name], data...)
				batchBytes += len(data)
			}
			add(stateFilename, line(task))
			task.Status, task.UpdatedAt = domaintask.StatusRunning, now.Add(time.Second)
			add(stateFilename, line(task))
			if rng.Intn(100) < 70 {
				run := domaintask.Run{
					WriterGeneration: generation, RunID: modulecore.RunID("run_" + testUUID("fill-run", seed*1_000_000+n)), TaskID: task.TaskID,
					StartReason: domaintask.RunStartReasonFirst, Assignee: "Shiro", Status: domaintask.RunStatusRunning, StartedAt: now,
				}
				add(runFilename, line(run))
				completed := now.Add(30 * time.Second)
				run.Status, run.CompletedAt, run.Summary = domaintask.RunStatusSucceeded, &completed, pad(80)
				add(runFilename, line(run))
			}
			if rng.Intn(100) < 40 {
				add(contextFilename, line(domaintask.SharedRoleContext{TaskID: task.TaskID, CurrentPlan: pad(60), UpdatedAt: now}))
			}
			if rng.Intn(100) < 35 {
				note := domaintask.NewNotification(task, now)
				note.Summary = pad(100)
				add(notificationsFilename, line(note))
			}
		}
		err := store.batch.Write(ctx, func() (map[string][]byte, error) {
			if err := store.idx.ensureSynced(); err != nil {
				return nil, err
			}
			return payloads, nil
		})
		if err != nil {
			t.Fatalf("fill the segment: %v", err)
		}
		written += batchBytes
	}
}

// TestSegmentCommitTimeAcrossTheSizeValve is AC-2 and AC-3 at the sizes that
// matter: the commit time of a write transaction in a segment that is empty, a
// quarter full, half full and at the size valve. The 100ms target is for the
// production host; the numbers printed are for this one.
func TestSegmentCommitTimeAcrossTheSizeValve(t *testing.T) {
	if os.Getenv("RENCROW_TASKINDEX_SCALE") == "" {
		t.Skip("set RENCROW_TASKINDEX_SCALE=1 to generate a synthetic history")
	}
	n := scaleEnvInt(t, "RENCROW_TASKINDEX_SCALE_SMALL", 72000)
	real := scaleEnvInt(t, "RENCROW_TASKINDEX_SCALE_REAL_TXS", 40)
	store, _, _ := openSyntheticSegmented(t, n)
	rng := rand.New(rand.NewSource(11))
	idle := func() modulecore.TaskID { return modulecore.TaskID("tsk_" + testUUID("scale", rng.Intn(n-300))) }
	filled := 0
	for step, target := range []int{0, 2 << 20, 4 << 20, 6 << 20, int(DefaultRotationSizeValve) - (256 << 10)} {
		if target > filled {
			fillSegment(t, store, target-filled, step+1)
			filled = target
		}
		stats, _ := store.IndexStats()
		var segBytes int64
		for _, v := range stats.AppliedBytes {
			segBytes += v
		}
		exec, commit := segmentTxTimes(t, store, idle, 4_000_000+step*1000, real)
		all := sumPerTx(exec, commit)
		t.Logf("segment holds %.1f MiB: write tx p50=%s p95=%s | exec p50=%s | commit p50=%s p95=%s", float64(segBytes)/(1<<20),
			durationPercentile(all, 0.5), durationPercentile(all, 0.95), durationPercentile(exec, 0.5), durationPercentile(commit, 0.5), durationPercentile(commit, 0.95))
	}
}

// TestSegmentRotationPause is AC-11: the time the writer is stopped while a
// segment changes is short, and a rotation that fails leaves the writer where it
// was.
func TestSegmentRotationPause(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeWritable(t, root) })
	clock := newTestClock("2026-10-13T08:00:00Z")
	stageOneStore(t, root, 40)
	var pauses []time.Duration
	opts := rotationOptions(clock, 0, nil)
	opts.rotationPause = func(d time.Duration) { pauses = append(pauses, d) }
	store, err := NewJSONLStoreWithOptions(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rounds := scaleEnvInt(t, "RENCROW_SEGMENT_ROTATIONS", 120)
	for i := 0; i < rounds; i++ {
		clock.Advance(25 * time.Hour)
		populate(t, store, 100+i*2, 100+i*2+2)
	}
	if len(pauses) < rounds {
		t.Fatalf("%d rotations were observed for %d day changes", len(pauses), rounds)
	}
	p50, p99, worst := durationPercentile(pauses, 0.5), durationPercentile(pauses, 0.99), durationPercentile(pauses, 1)
	t.Logf("%d rotations: pause p50=%s p99=%s max=%s", len(pauses), p50, p99, worst)
	if p99 > 200*time.Millisecond {
		t.Fatalf("rotation pause p99 = %s, want 200ms or less (AC-11)", p99)
	}
	requireSegmentedStoreEqualsFullBuild(t, store, root)
}

// A rotation that cannot create the new segment leaves the writer on the segment
// it has, keeps accepting writes, and tries again after the back-off.
func TestAFailedRotationLeavesTheWriterWhereItWas(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeWritable(t, root) })
	clock := newTestClock("2026-10-13T08:00:00Z")
	stageOneStore(t, root, 4)
	store := openRotating(t, root, clock, 0, nil)
	populate(t, store, 4, 6)
	store.rot.retryAfter = time.Hour

	// A file where the new segment's directory should be makes jsonlbatch.New fail.
	blocker := segmentDir(root, "20261014-01")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	clock.Set("2026-10-14T08:00:00Z")
	populate(t, store, 6, 9)
	if got := store.idx.currentSegmentName(); got != "20261013-01" {
		t.Fatalf("current segment = %s after a failed rotation, want 20261013-01", got)
	}
	for n := 0; n < 9; n++ {
		if _, err := store.GetTask(context.Background(), indexedTestTask(n, indexTestNow).TaskID); err != nil {
			t.Fatalf("task %d after a failed rotation: %v", n, err)
		}
	}

	// While it backs off the store does not try again on every write.
	attempts := 0
	store.rot.failpoint = func(stage string) {
		if stage == "rotate-settled" {
			attempts++
		}
	}
	populate(t, store, 9, 12)
	if attempts != 0 {
		t.Fatalf("%d rotation attempts inside the back-off", attempts)
	}

	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	store.rot.succeeded() // the back-off is over
	populate(t, store, 12, 14)
	if got := store.idx.currentSegmentName(); got != "20261014-01" {
		t.Fatalf("current segment = %s after the cause went away, want 20261014-01", got)
	}
	requireSegmentedStoreEqualsFullBuild(t, store, root)
	_ = errors.Is
	_ = strconv.Itoa
}
