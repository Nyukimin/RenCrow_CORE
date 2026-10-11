package task

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// restoreBudget is the acceptance bound of IU-2 for a restore from the sidecar
// on a production-sized store, measured on the development Mac. The production
// host is slower by the factor of the earlier single-point comparison (about
// 4.6), which keeps AC-9 (1.0 s) within reach.
const restoreBudget = 250 * time.Millisecond

// TestSidecarAcceptanceOnProductionCopy measures what AC-9 asks of IU-2 on real
// data and checks AC-12/AC-13 on it: the time to build from the log, the time
// to restore from the sidecar (median of five opens), the time to replay a tail
// after a crash, and a full verification. Only counts, sizes and durations are
// logged.
func TestSidecarAcceptanceOnProductionCopy(t *testing.T) {
	src := os.Getenv(prodCopyEnv)
	if src == "" {
		t.Skipf("set %s to a copy of a production Task store", prodCopyEnv)
	}
	ctx := context.Background()
	dir := copyStoreFiles(t, src)
	opts := OpenOptions{Index: true, Persist: true}

	// No sidecar yet: the first open reads the whole log and writes one.
	started := time.Now()
	first, err := NewJSONLStoreWithOptions(dir, opts)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	buildOpen := time.Since(started)
	stats, _ := first.IndexStats()
	if stats.Source != sourceRebuilt || stats.RebuildReason != rebuildMissing {
		t.Fatalf("first open: source=%q reason=%q, want rebuilt/missing", stats.Source, stats.RebuildReason)
	}
	closeStarted := time.Now()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("full build open: %s (tasks=%d runs=%d notifications=%d lines=%d); close with final checkpoint: %s",
		buildOpen.Round(time.Millisecond), stats.Tasks, stats.Runs, stats.Notifications, totalLines(stats.Lines), time.Since(closeStarted).Round(time.Millisecond))
	info, err := os.Stat(sidecarFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sidecar: %.1f MB for %d tasks (%.0f B/Task)", float64(info.Size())/1e6, stats.Tasks, float64(info.Size())/float64(stats.Tasks))

	// Restore from the sidecar.
	var opens []time.Duration
	for i := 0; i < 5; i++ {
		started := time.Now()
		store, err := NewJSONLStoreWithOptions(dir, opts)
		d := time.Since(started)
		if err != nil {
			t.Fatalf("restore open %d: %v", i, err)
		}
		if s, _ := store.IndexStats(); s.Source != sourceSidecar || store.idx.replayedLines != 0 {
			t.Fatalf("restore open %d: source=%q (%s) replayed=%d", i, s.Source, s.RebuildReason, store.idx.replayedLines)
		}
		opens = append(opens, d)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	sort.Slice(opens, func(i, j int) bool { return opens[i] < opens[j] })
	median := opens[len(opens)/2]
	t.Logf("restore from the sidecar (whole open, 5 runs): min=%s median=%s max=%s (budget %s)", opens[0].Round(time.Millisecond), median.Round(time.Millisecond), opens[len(opens)-1].Round(time.Millisecond), restoreBudget)
	if median > restoreBudget {
		t.Errorf("restore median %s exceeds %s", median, restoreBudget)
	}

	// A crash after some commits: the sidecar lags the log by a tail.
	store, err := NewJSONLStoreWithOptions(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
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
	rng := rand.New(rand.NewSource(11))
	written := 0
	for written < 300 {
		task := folded[rng.Intn(len(folded))]
		if _, busy := running[task.TaskID]; busy {
			continue
		}
		if err := succeedLikeTransaction(ctx, store, task.TaskID, 5_000_000+written, false); err != nil {
			continue // a Task whose state does not allow the transition is skipped
		}
		written++
	}
	if err := store.closeStore(false); err != nil {
		t.Fatal(err)
	}
	started = time.Now()
	again, err := NewJSONLStoreWithOptions(dir, opts)
	if err != nil {
		t.Fatalf("open after a crash: %v", err)
	}
	replayOpen := time.Since(started)
	defer again.Close()
	if s, _ := again.IndexStats(); s.Source != sourceSidecar || again.idx.replayedLines == 0 {
		t.Fatalf("open after a crash: source=%q (%s) replayed=%d", s.Source, s.RebuildReason, again.idx.replayedLines)
	}
	t.Logf("open after a crash with %d transactions beyond the sidecar: %s (replayed %d lines)", written, replayOpen.Round(time.Millisecond), again.idx.replayedLines)
	requireStoreEqualsFullBuild(t, again, dir)
	if err := again.Close(); err != nil {
		t.Fatal(err)
	}

	// Verification and rebuild, as the CLI runs them.
	started = time.Now()
	report, err := VerifyTaskStore(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("verify: ok=%t mismatches=%d sidecar=%s tasks=%d runs=%d contexts=%d notifications=%d in %s",
		report.OK, report.Mismatches, report.Sidecar, report.Tasks, report.Runs, report.Contexts, report.Notifications, time.Since(started).Round(time.Millisecond))
	if !report.OK || report.Sidecar != verifySidecarMatches {
		t.Fatalf("verification of the production copy failed: %+v", report)
	}
	started = time.Now()
	rebuild, err := RebuildTaskIndex(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rebuild-index: %d tasks, %d lines, sidecar %.1f MB in %s", rebuild.Tasks, rebuild.Lines, float64(rebuild.SidecarBytes)/1e6, time.Since(started).Round(time.Millisecond))
}
