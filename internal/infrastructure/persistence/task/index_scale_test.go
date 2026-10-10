package task

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// The scale tests generate a synthetic history shaped like the production
// store (about 2.4 state, 1.4 run, 0.9 context and 0.7 notification lines per
// Task) and are skipped unless RENCROW_TASKINDEX_SCALE is set, because they
// write hundreds of megabytes. They never use real records.

func scaleEnvInt(t testing.TB, name string, fallback int) int {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		t.Fatalf("%s=%q is not a positive integer", name, raw)
	}
	return n
}

// generateSyntheticLog writes a valid history of n Tasks into dir. The last
// `running` Tasks keep an active Run.
func generateSyntheticLog(t testing.TB, dir string, n, running int, seed int64) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	open := func(name string) (*os.File, *bufio.Writer) {
		file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		return file, bufio.NewWriterSize(file, 1<<20)
	}
	stateFile, state := open(stateFilename)
	runFile, runs := open(runFilename)
	ctxFile, contexts := open(contextFilename)
	notifFile, notifs := open(notificationsFilename)
	write := func(w *bufio.Writer, value any) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
		w.WriteByte('\n')
	}
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	routes := []domaintask.Route{domaintask.RouteGeneral, domaintask.RouteCode, domaintask.RouteOperations}
	pad := func(k int) string {
		buf := make([]byte, k)
		for i := range buf {
			buf[i] = 'a' + byte(rng.Intn(26))
		}
		return string(buf)
	}
	for i := 0; i < n; i++ {
		created := base.Add(time.Duration(i) * 1200 * time.Millisecond)
		task := domaintask.Task{
			TaskID: modulecore.TaskID("tsk_" + testUUID("scale", i)), Title: "Memory Promotion " + strconv.Itoa(i%59),
			ModuleID: "core", Route: routes[i%len(routes)], Assignee: []string{"Shiro", "Mio", "Kuro", "Midori"}[i%4],
			Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked,
			CreatedAt: created, UpdatedAt: created, Summary: pad(60),
		}
		write(state, task)
		keepRunning := i >= n-running
		hasRun := keepRunning || rng.Intn(100) < 75
		final := task
		if hasRun || rng.Intn(100) < 40 {
			task.Status = domaintask.StatusRunning
			started := created.Add(time.Second)
			task.StartedAt, task.UpdatedAt = &started, started
			write(state, task)
			final = task
		}
		if hasRun {
			run := domaintask.Run{
				RunID: modulecore.RunID("run_" + testUUID("scale-run", i)), TaskID: task.TaskID,
				StartReason: domaintask.RunStartReasonFirst, Assignee: task.Assignee, Status: domaintask.RunStatusRunning, StartedAt: created.Add(time.Second),
			}
			write(runs, run)
			if !keepRunning {
				completed := run.StartedAt.Add(30 * time.Second)
				run.Status, run.CompletedAt, run.Summary = domaintask.RunStatusSucceeded, &completed, pad(80)
				write(runs, run)
			}
		}
		if !keepRunning && hasRun {
			finished := created.Add(time.Minute)
			final.Status, final.FinishedAt, final.UpdatedAt = domaintask.StatusSucceeded, &finished, finished
			write(state, final)
		}
		if rng.Intn(100) < 94 {
			write(contexts, domaintask.SharedRoleContext{TaskID: task.TaskID, CurrentPlan: pad(60), UpdatedAt: final.UpdatedAt})
		}
		if rng.Intn(100) < 67 {
			note := domaintask.NewNotification(final, final.UpdatedAt)
			note.Summary = pad(100)
			write(notifs, note)
		}
	}
	for _, w := range []*bufio.Writer{state, runs, contexts, notifs} {
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []*os.File{stateFile, runFile, ctxFile, notifFile} {
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func durationPercentile(values []time.Duration, p float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(float64(len(sorted)-1) * p)
	return sorted[index]
}

func heapInUse() uint64 {
	runtime.GC()
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapAlloc
}

// txTimings collects the jsonlbatch phase timings of write transactions.
type txTimings struct {
	exec, commit []time.Duration
}

func (c *txTimings) observe(_ context.Context, tx jsonlbatch.TxObservation) {
	if tx.Kind == jsonlbatch.TxKindWrite && tx.Acquired {
		c.exec = append(c.exec, tx.Exec)
		if tx.Err == nil {
			c.commit = append(c.commit, tx.Commit)
		}
	}
}

// succeedLikeTransaction is the shape of Manager.Succeed: read the Task and its
// Runs, open and close a Run, update the Task, record a notification. When
// abort is set it returns an error at the end, which exercises the callback
// (everything the exec phase does) without paying for a commit.
func succeedLikeTransaction(ctx context.Context, store *JSONLStore, id modulecore.TaskID, n int, abort bool) error {
	return store.TaskTransaction(ctx, id, func(tx domaintask.Store) error {
		task, err := tx.GetTask(ctx, id)
		if err != nil {
			return err
		}
		if _, err := tx.ListRuns(ctx, domaintask.RunFilter{TaskID: id}); err != nil {
			return err
		}
		generation, err := tx.WriterGeneration()
		if err != nil {
			return err
		}
		started := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Second)
		run := domaintask.Run{
			WriterGeneration: generation, RunID: modulecore.RunID("run_" + testUUID("scale-exec", n)), TaskID: id,
			StartReason: domaintask.RunStartReasonExplicitRerun, Status: domaintask.RunStatusRunning, StartedAt: started,
		}
		if err := tx.SaveRun(ctx, run); err != nil {
			return err
		}
		completed := started.Add(time.Second)
		run.Status, run.CompletedAt = domaintask.RunStatusSucceeded, &completed
		if err := tx.SaveRun(ctx, run); err != nil {
			return err
		}
		task.Status, task.UpdatedAt = domaintask.StatusSucceeded, completed
		if err := tx.SaveTask(ctx, task); err != nil {
			return err
		}
		if err := tx.SaveNotification(ctx, domaintask.NewNotification(task, completed)); err != nil {
			return err
		}
		if abort {
			return errScaleAbort
		}
		return nil
	})
}

var errScaleAbort = errors.New("scale test: abort after the callback")

// scaleRun builds a synthetic history of n Tasks, opens it with the index and
// measures open, memory and write-transaction timing.
type scaleResult struct {
	tasks          int
	buildDuration  time.Duration
	heapPerTask    float64
	heapBytes      uint64
	approxBytes    int64
	execAbortP50   time.Duration
	execRealP50    time.Duration
	execRealP95    time.Duration
	commitRealP50  time.Duration
	getTaskP50     time.Duration
	getTaskP99     time.Duration
	listTasks200   time.Duration
	listRunsActive time.Duration
	listNotes100   time.Duration
}

func runScale(t *testing.T, n int, real int) scaleResult {
	t.Helper()
	dir := t.TempDir()
	generateSyntheticLog(t, dir, n, 300, int64(n))
	before := heapInUse()
	started := time.Now()
	store, err := NewJSONLStoreWithOptions(dir, OpenOptions{Index: true})
	if err != nil {
		t.Fatalf("open indexed store with %d tasks: %v", n, err)
	}
	defer store.Close()
	result := scaleResult{tasks: n, buildDuration: time.Since(started)}
	result.heapBytes = heapInUse() - before
	result.heapPerTask = float64(result.heapBytes) / float64(n)
	stats, _ := store.IndexStats()
	result.approxBytes = stats.ApproxBytes

	ctx := context.Background()
	timings := &txTimings{}
	store.batch.SetTxObserver(timings.observe)
	rng := rand.New(rand.NewSource(99))
	pickID := func() modulecore.TaskID {
		return modulecore.TaskID("tsk_" + testUUID("scale", rng.Intn(n)))
	}
	// Transactions open a new Run, so they must not hit a Task that still has one.
	pickIdle := func() modulecore.TaskID {
		return modulecore.TaskID("tsk_" + testUUID("scale", rng.Intn(n-300)))
	}
	// Warm the page cache and the allocator, then measure.
	for i := 0; i < 20; i++ {
		_ = succeedLikeTransaction(ctx, store, pickIdle(), 1_000_000+i, true)
	}
	timings.exec, timings.commit = nil, nil
	const aborted = 300
	for i := 0; i < aborted; i++ {
		if err := succeedLikeTransaction(ctx, store, pickIdle(), 2_000_000+i, true); err != nil && !errors.Is(err, errScaleAbort) {
			t.Fatalf("aborted transaction %d: %v", i, err)
		}
	}
	result.execAbortP50 = durationPercentile(timings.exec, 0.5)
	timings.exec, timings.commit = nil, nil
	for i := 0; i < real; i++ {
		if err := succeedLikeTransaction(ctx, store, pickIdle(), 3_000_000+i, false); err != nil {
			t.Fatalf("real transaction %d: %v", i, err)
		}
	}
	result.execRealP50, result.execRealP95 = durationPercentile(timings.exec, 0.5), durationPercentile(timings.exec, 0.95)
	result.commitRealP50 = durationPercentile(timings.commit, 0.5)

	var gets []time.Duration
	for i := 0; i < 5000; i++ {
		id := pickID()
		start := time.Now()
		if _, err := store.GetTask(ctx, id); err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		gets = append(gets, time.Since(start))
	}
	result.getTaskP50, result.getTaskP99 = durationPercentile(gets, 0.5), durationPercentile(gets, 0.99)
	start := time.Now()
	if _, err := store.ListTasks(ctx, domaintask.Filter{Limit: 200}); err != nil {
		t.Fatal(err)
	}
	result.listTasks200 = time.Since(start)
	start = time.Now()
	if _, err := store.ListRuns(ctx, domaintask.RunFilter{Status: domaintask.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	result.listRunsActive = time.Since(start)
	start = time.Now()
	if _, err := store.ListNotifications(ctx, 100, false); err != nil {
		t.Fatal(err)
	}
	result.listNotes100 = time.Since(start)
	return result
}

func (r scaleResult) log(t *testing.T) {
	t.Helper()
	t.Logf("tasks=%d build=%s heap=%.1fMB (%.0f B/Task) approx=%.1fMB | write exec p50 aborted=%s real=%s p95=%s commit p50=%s | GetTask p50=%s p99=%s | ListTasks(200)=%s ListRuns(running)=%s ListNotifications(100)=%s",
		r.tasks, r.buildDuration.Round(time.Millisecond), float64(r.heapBytes)/1e6, r.heapPerTask, float64(r.approxBytes)/1e6,
		r.execAbortP50, r.execRealP50, r.execRealP95, r.commitRealP50, r.getTaskP50, r.getTaskP99, r.listTasks200, r.listRunsActive, r.listNotes100)
}

// TestExecTimeDoesNotGrowWithHistory is the spec's AC-3/R-1 exec part: the same
// transaction on a history ten times larger takes about as long. The commit
// still hashes the whole file prefix before rotation exists, so it is reported
// but not asserted.
func TestExecTimeDoesNotGrowWithHistory(t *testing.T) {
	if os.Getenv("RENCROW_TASKINDEX_SCALE") == "" {
		t.Skip("set RENCROW_TASKINDEX_SCALE=1 to generate a synthetic 72k and 720k Task history")
	}
	small := scaleEnvInt(t, "RENCROW_TASKINDEX_SCALE_SMALL", 72000)
	large := scaleEnvInt(t, "RENCROW_TASKINDEX_SCALE_LARGE", 720000)
	real := scaleEnvInt(t, "RENCROW_TASKINDEX_SCALE_REAL_TXS", 25)
	a := runScale(t, small, real)
	a.log(t)
	b := runScale(t, large, real)
	b.log(t)
	ratio := float64(b.execRealP50) / float64(a.execRealP50)
	abortRatio := float64(b.execAbortP50) / float64(a.execAbortP50)
	t.Logf("exec p50 ratio large/small: real=%.2f aborted=%.2f (limit 1.2)", ratio, abortRatio)
	if ratio > 1.2 || abortRatio > 1.2 {
		t.Fatalf("write transaction exec time grows with history: real %.2f, aborted %.2f", ratio, abortRatio)
	}
}
