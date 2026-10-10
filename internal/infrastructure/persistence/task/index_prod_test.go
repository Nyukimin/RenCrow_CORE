package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// prodCopyEnv names a directory holding a read-only copy of a production Task
// store (the Task, Run, context and notification JSONL files). The tests below
// copy it into a temporary directory, never write to the original and never log
// a record: only counts, durations and sizes.
const prodCopyEnv = "RENCROW_TASKINDEX_PROD_COPY"

func copyStoreFiles(t testing.TB, src string) string {
	t.Helper()
	dst := t.TempDir()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read %s: %v", prodCopyEnv, err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		in, err := os.Open(filepath.Join(src, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out, err := os.OpenFile(filepath.Join(dst, entry.Name()), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			_ = in.Close()
			t.Fatal(err)
		}
		if _, err := io.Copy(out, in); err != nil {
			t.Fatal(err)
		}
		_ = in.Close()
		if err := out.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func digestOf(t testing.TB, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// TestIndexMatchesFullFoldOnProductionCopy is the oracle differential test on
// real data (spec R-2, R-3, R-9): every Task, Run and context the log holds is
// fetched through the index and compared with the full fold of the same files,
// and the list queries are compared with the answers of the store that folds
// everything per transaction.
func TestIndexMatchesFullFoldOnProductionCopy(t *testing.T) {
	src := os.Getenv(prodCopyEnv)
	if src == "" {
		t.Skipf("set %s to a copy of a production Task store", prodCopyEnv)
	}
	ctx := context.Background()
	dir := copyStoreFiles(t, src)

	// The oracle: the production fold functions applied to the raw files.
	taskRecords, err := readJSONLLines[domaintask.Task](ctx, filepath.Join(dir, stateFilename))
	if err != nil {
		t.Fatal(err)
	}
	foldedTasks, err := foldTaskRecords(taskRecords)
	if err != nil {
		t.Fatal(err)
	}
	runRecords, err := readJSONLLines[domaintask.Run](ctx, filepath.Join(dir, runFilename))
	if err != nil {
		t.Fatal(err)
	}
	foldedRuns, err := foldRunRecords(runRecords, 0)
	if err != nil {
		t.Fatal(err)
	}
	contextRecords, err := readJSONLLines[domaintask.SharedRoleContext](ctx, filepath.Join(dir, contextFilename))
	if err != nil {
		t.Fatal(err)
	}
	latestContext := map[modulecore.TaskID]domaintask.SharedRoleContext{}
	for _, record := range contextRecords {
		latestContext[record.TaskID] = record
	}
	t.Logf("log: %d task lines -> %d tasks, %d run lines -> %d runs, %d contexts", len(taskRecords), len(foldedTasks), len(runRecords), len(foldedRuns), len(latestContext))

	// The legacy store answers the list queries with its own code path.
	assignee, module, route := mostCommon(foldedTasks)
	taskFilters := []domaintask.Filter{
		{}, {Limit: 200}, {Limit: 1}, {Status: domaintask.StatusRunning}, {Status: domaintask.StatusQueued, Limit: 50},
		{Assignee: assignee}, {Assignee: toggleCase(assignee), Limit: 77}, {ModuleID: module, Limit: 300}, {Route: route},
		{Status: domaintask.StatusSucceeded, Route: route, Limit: 500},
	}
	runFilters := []domaintask.RunFilter{{}, {Limit: 100}, {Status: domaintask.RunStatusRunning}, {Status: domaintask.RunStatusSucceeded, Limit: 40}}
	if len(foldedRuns) > 0 {
		runFilters = append(runFilters, domaintask.RunFilter{TaskID: foldedRuns[len(foldedRuns)/2].TaskID})
	}
	type listExpectation struct {
		tasks []string
		runs  []string
		notes []string
	}
	var expected listExpectation
	legacy, err := NewJSONLStoreWithOptions(dir, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = legacy.ReadTransaction(ctx, func(tx domaintask.Store) error {
		for _, filter := range taskFilters {
			items, err := tx.ListTasks(ctx, filter)
			if err != nil {
				return err
			}
			expected.tasks = append(expected.tasks, digestOf(t, items))
		}
		for _, filter := range runFilters {
			items, err := tx.ListRuns(ctx, filter)
			if err != nil {
				return err
			}
			expected.runs = append(expected.runs, digestOf(t, items))
		}
		for _, args := range [][2]any{{0, false}, {100, false}, {100, true}, {7, true}} {
			items, err := tx.ListNotifications(ctx, args[0].(int), args[1].(bool))
			if err != nil {
				return err
			}
			expected.notes = append(expected.notes, digestOf(t, items))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("legacy list queries: %v", err)
	}
	t.Logf("legacy answered %d list queries in %s", len(taskFilters)+len(runFilters)+4, time.Since(started).Round(time.Millisecond))
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	before := heapInUse()
	openStart := time.Now()
	store, err := NewJSONLStoreWithOptions(dir, OpenOptions{Index: true})
	if err != nil {
		t.Fatalf("open indexed store on the production copy: %v", err)
	}
	defer store.Close()
	buildTime := time.Since(openStart)
	heap := heapInUse() - before
	stats, _ := store.IndexStats()
	perTask := float64(heap) / float64(stats.Tasks)
	t.Logf("index: tasks=%d runs=%d notifications=%d build+open=%s heap=%.1fMB (%.0f B/Task) approx=%.1fMB", stats.Tasks, stats.Runs, stats.Notifications, buildTime.Round(time.Millisecond), float64(heap)/1e6, perTask, float64(stats.ApproxBytes)/1e6)
	if perTask > 450 {
		t.Errorf("index heap %.0f B/Task exceeds 450", perTask)
	}
	if stats.Tasks <= 80000 && heap > 32<<20 {
		t.Errorf("index heap %.1fMB exceeds 32MB for %d Tasks", float64(heap)/1e6, stats.Tasks)
	}

	// INV-2 on real data: every ID in the log is reachable and equals the fold.
	for _, want := range foldedTasks {
		got, err := store.GetTask(ctx, want.TaskID)
		if err != nil {
			t.Fatalf("GetTask(%s): %v", want.TaskID, err)
		}
		if !reflect.DeepEqual(got, cloneTransactionTask(want)) {
			t.Fatalf("GetTask(%s) differs from the fold", want.TaskID)
		}
	}
	for _, want := range foldedRuns {
		got, err := store.GetRun(ctx, want.RunID)
		if err != nil {
			t.Fatalf("GetRun(%s): %v", want.RunID, err)
		}
		if !reflect.DeepEqual(got, cloneTransactionRun(want)) {
			t.Fatalf("GetRun(%s) differs from the fold", want.RunID)
		}
	}
	for id, want := range latestContext {
		got, err := store.GetContext(ctx, id)
		if err != nil {
			t.Fatalf("GetContext(%s): %v", id, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("GetContext(%s) differs from the fold", id)
		}
	}
	requireIndexCoversLog(t, store, dir)

	for i, filter := range taskFilters {
		items, err := store.ListTasks(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		if got := digestOf(t, items); got != expected.tasks[i] {
			t.Errorf("ListTasks(%+v): %d items, digest differs from the legacy store", filter, len(items))
		}
	}
	for i, filter := range runFilters {
		items, err := store.ListRuns(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		if got := digestOf(t, items); got != expected.runs[i] {
			t.Errorf("ListRuns(%+v): %d items, digest differs from the legacy store", filter, len(items))
		}
	}
	for i, args := range [][2]any{{0, false}, {100, false}, {100, true}, {7, true}} {
		items, err := store.ListNotifications(ctx, args[0].(int), args[1].(bool))
		if err != nil {
			t.Fatal(err)
		}
		if got := digestOf(t, items); got != expected.notes[i] {
			t.Errorf("ListNotifications(%v): %d items, digest differs from the legacy store", args, len(items))
		}
	}

	// Restart: the index rebuilt from the same bytes answers identically.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := NewJSONLStoreWithOptions(dir, OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	requireIndexCoversLog(t, again, dir)
}

// TestIndexPerformanceOnProductionCopy reports the acceptance numbers for the
// index on real data: GetTask latency, list latency, write-transaction exec time
// and the heap per Task.
func TestIndexPerformanceOnProductionCopy(t *testing.T) {
	src := os.Getenv(prodCopyEnv)
	if src == "" {
		t.Skipf("set %s to a copy of a production Task store", prodCopyEnv)
	}
	ctx := context.Background()
	dir := copyStoreFiles(t, src)
	store, err := NewJSONLStoreWithOptions(dir, OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stats, _ := store.IndexStats()

	taskRecords, err := readJSONLLines[domaintask.Task](ctx, filepath.Join(dir, stateFilename))
	if err != nil {
		t.Fatal(err)
	}
	folded, err := foldTaskRecords(taskRecords)
	if err != nil {
		t.Fatal(err)
	}
	running := map[modulecore.TaskID]struct{}{}
	runs, err := store.ListRuns(ctx, domaintask.RunFilter{Status: domaintask.RunStatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		running[run.TaskID] = struct{}{}
	}
	var idle []modulecore.TaskID
	for _, task := range folded {
		if _, busy := running[task.TaskID]; !busy {
			idle = append(idle, task.TaskID)
		}
	}
	rng := rand.New(rand.NewSource(7))

	const lookups = 20000
	gets := make([]time.Duration, 0, lookups)
	for i := 0; i < lookups; i++ {
		id := folded[rng.Intn(len(folded))].TaskID
		start := time.Now()
		if _, err := store.GetTask(ctx, id); err != nil {
			t.Fatal(err)
		}
		gets = append(gets, time.Since(start))
	}
	getP50, getP99 := durationPercentile(gets, 0.5), durationPercentile(gets, 0.99)

	timeOnce := func(fn func() error) time.Duration {
		best := time.Hour
		for i := 0; i < 5; i++ {
			start := time.Now()
			if err := fn(); err != nil {
				t.Fatal(err)
			}
			if d := time.Since(start); d < best {
				best = d
			}
		}
		return best
	}
	listTasks := timeOnce(func() error { _, err := store.ListTasks(ctx, domaintask.Filter{Limit: 200}); return err })
	listRuns := timeOnce(func() error {
		_, err := store.ListRuns(ctx, domaintask.RunFilter{Status: domaintask.RunStatusRunning})
		return err
	})
	listNotes := timeOnce(func() error { _, err := store.ListNotifications(ctx, 100, false); return err })

	timings := &txTimings{}
	store.batch.SetTxObserver(timings.observe)
	pick := func() modulecore.TaskID { return idle[rng.Intn(len(idle))] }
	for i := 0; i < 30; i++ {
		_ = succeedLikeTransaction(ctx, store, pick(), 1_000_000+i, true)
	}
	timings.exec, timings.commit = nil, nil
	for i := 0; i < 300; i++ {
		_ = succeedLikeTransaction(ctx, store, pick(), 2_000_000+i, true)
	}
	abortedP50, abortedP95 := durationPercentile(timings.exec, 0.5), durationPercentile(timings.exec, 0.95)
	timings.exec, timings.commit = nil, nil
	for i := 0; i < 10; i++ {
		if err := succeedLikeTransaction(ctx, store, pick(), 3_000_000+i, false); err != nil {
			t.Fatal(err)
		}
	}
	realP50, realP95 := durationPercentile(timings.exec, 0.5), durationPercentile(timings.exec, 0.95)
	commitP50 := durationPercentile(timings.commit, 0.5)

	t.Logf("tasks=%d GetTask p50=%s p99=%s | ListTasks(200)=%s ListRuns(running)=%s ListNotifications(100)=%s | write exec p50 aborted=%s (p95 %s) real=%s (p95 %s) commit p50=%s",
		stats.Tasks, getP50, getP99, listTasks, listRuns, listNotes, abortedP50, abortedP95, realP50, realP95, commitP50)
	if getP50 > 100*time.Microsecond {
		t.Errorf("GetTask p50 %s exceeds 100µs", getP50)
	}
	if abortedP50 > 5*time.Millisecond || realP50 > 5*time.Millisecond {
		t.Errorf("write exec p50 aborted=%s real=%s exceeds 5ms", abortedP50, realP50)
	}
}

func mostCommon(tasks []domaintask.Task) (assignee, module string, route domaintask.Route) {
	assignees, modules, routes := map[string]int{}, map[string]int{}, map[domaintask.Route]int{}
	for _, task := range tasks {
		assignees[task.Assignee]++
		modules[task.ModuleID]++
		routes[task.Route]++
	}
	best := func(m map[string]int) string {
		top, topN := "", -1
		for k, n := range m {
			if k != "" && (n > topN || (n == topN && k < top)) {
				top, topN = k, n
			}
		}
		return top
	}
	assignee, module = best(assignees), best(modules)
	topN := -1
	for r, n := range routes {
		if n > topN || (n == topN && r < route) {
			route, topN = r, n
		}
	}
	return assignee, module, route
}

func toggleCase(value string) string {
	out := []byte(value)
	for i, c := range out {
		if c >= 'a' && c <= 'z' {
			out[i] = c - 32
		} else if c >= 'A' && c <= 'Z' {
			out[i] = c + 32
		}
	}
	return string(out)
}
