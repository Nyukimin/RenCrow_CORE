package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Return to layout v1 (spec 10.4 R1, 14.1 R-11, AC-14).

func readSegmentFiles(t testing.TB, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, name := range sizedFilenames {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		out[name] = data
	}
	return out
}

// buildSegmentedStore makes a store with a frozen segment 0 and three more
// segments (three days), closes it, and returns the expected concatenation of
// each file: segment 0 followed by every segment in order.
func buildSegmentedStore(t *testing.T, root string, clock *testClock) (expected map[string][]byte, segmentBytes map[string]map[string][]byte) {
	t.Helper()
	stageOneStore(t, root, 6)
	original := readSegmentFiles(t, root)
	store := openRotating(t, root, clock, 0, nil)
	populate(t, store, 6, 10)
	clock.Set("2026-10-14T08:00:00Z")
	populate(t, store, 10, 14)
	clock.Set("2026-10-15T08:00:00Z")
	populate(t, store, 14, 18)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	expected = map[string][]byte{}
	segmentBytes = map[string]map[string][]byte{}
	for name, data := range original {
		expected[name] = append([]byte(nil), data...)
	}
	for _, seg := range segmentNames(t, root) {
		files := readSegmentFiles(t, segmentDir(root, seg))
		segmentBytes[seg] = files
		for name, data := range files {
			expected[name] = append(expected[name], data...)
		}
	}
	return expected, segmentBytes
}

func TestConcatReturnsTheStoreToLayoutV1WithEveryLineInOrder(t *testing.T) {
	root := t.TempDir()
	clock := newTestClock("2026-10-13T08:00:00Z")
	expected, segmentBytes := buildSegmentedStore(t, root, clock)
	if len(segmentBytes) != 3 {
		t.Fatalf("segments = %d, want 3", len(segmentBytes))
	}

	plan, err := ConcatTaskLayout(context.Background(), root, false)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.State != "plan" {
		t.Fatalf("plan state = %q", plan.State)
	}
	if layout, _ := readLayout(root); !layout.v2 {
		t.Fatal("a plan changed the layout")
	}

	report, err := ConcatTaskLayout(context.Background(), root, true)
	if err != nil {
		t.Fatalf("concat: %v", err)
	}
	if report.State != "done" {
		t.Fatalf("concat state = %q, want done", report.State)
	}
	if layout, err := readLayout(root); err != nil || layout.v2 {
		t.Fatalf("LAYOUT after concat: %+v, %v", layout, err)
	}
	if _, err := os.Stat(layoutPath(root)); !os.IsNotExist(err) {
		t.Fatalf("LAYOUT is still there: %v", err)
	}
	for _, name := range sizedFilenames {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, expected[name]) {
			t.Fatalf("%s after concat is %d bytes, want the original followed by every segment in order (%d bytes)", name, len(got), len(expected[name]))
		}
		if mode := fileMode(t, filepath.Join(root, name)); mode&0o200 == 0 {
			t.Fatalf("%s is %o: layout v1 writes to it", name, mode)
		}
	}
	if mode := fileMode(t, filepath.Join(root, ".jsonlbatch.wal")); mode&0o200 == 0 {
		t.Fatalf("the WAL is %o", mode)
	}
	// Nothing is deleted: the segments are where logs/ was renamed to.
	if _, err := os.Stat(logsDir(root)); !os.IsNotExist(err) {
		t.Fatalf("logs/ is still there: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(root, "logs.rolled-back-*"))
	if len(matches) != 1 {
		t.Fatalf("rolled-back logs = %v", matches)
	}
	for seg, files := range segmentBytes {
		for name, want := range files {
			got, err := os.ReadFile(filepath.Join(matches[0], seg, name))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("segment %s %s changed by the concat (%v)", seg, name, err)
			}
		}
	}
	if m, _ := filepath.Glob(filepath.Join(root, "LAYOUT.rolled-back-*")); len(m) != 1 {
		t.Fatalf("rolled-back LAYOUT = %v", m)
	}

	// The old implementation (fold of the whole log, no index) opens it and sees
	// everything; so does the indexed one, from a rebuild of the stale sidecar.
	legacy, err := NewJSONLStoreWithOptions(root, OpenOptions{})
	if err != nil {
		t.Fatalf("open the concatenated store without the index: %v", err)
	}
	ctx := context.Background()
	for n := 0; n < 18; n++ {
		id := indexedTestTask(n, indexTestNow).TaskID
		if _, err := legacy.GetTask(ctx, id); err != nil {
			t.Fatalf("task %d after concat (full fold): %v", n, err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	indexed := openPersisted(t, root, manualPolicy(), nil)
	requireStoreEqualsFullBuild(t, indexed, root)

	// And it can go to segments again, on top of what it holds.
	if err := indexed.Close(); err != nil {
		t.Fatal(err)
	}
	clock.Set("2026-10-20T08:00:00Z")
	again := openRotating(t, root, clock, 0, nil)
	populate(t, again, 18, 20)
	for n := 0; n < 20; n++ {
		if _, err := again.GetTask(ctx, indexedTestTask(n, indexTestNow).TaskID); err != nil {
			t.Fatalf("task %d after concat and a second migration: %v", n, err)
		}
	}
	requireSegmentedStoreEqualsFullBuild(t, again, root)
}

func TestConcatLegacyV2WithoutReportFileThenRestartPreservesTaskRunAndReport(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	segments := buildLegacyV2Fixture(t, root, 3)
	if len(segments) != 3 {
		t.Fatalf("legacy v2 segments=%v, want 3", segments)
	}
	reportPath := filepath.Join(root, taskDelegationReportFilename)
	if _, err := os.Stat(reportPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy v2 segment 0 has a report file: %v", err)
	}

	clock := newTestClock("2026-10-15T08:00:00Z")
	store, err := NewJSONLStoreWithOptions(root, rotationOptions(clock, 0, nil))
	if err != nil {
		t.Fatalf("open legacy v2 store: %v", err)
	}
	taskID := indexedTestTask(2, indexTestNow).TaskID
	task, err := store.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	runID := indexedTestRun(task, 2, 1, indexTestNow).RunID
	run, err := store.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("fixture Run status=%s, want succeeded", run.Status)
	}
	report := saveConcatFixtureReport(t, store, task, run)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(reportPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("writing the report created a segment 0 report file: %v", err)
	}

	if _, err := ConcatTaskLayout(ctx, root, true); err != nil {
		t.Fatalf("concat authentic six-file v2 store with a new segment report: %v", err)
	}
	if _, err := os.Stat(reportPath); err != nil {
		t.Fatalf("concat did not create the report destination: %v", err)
	}

	restarted, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true, Persist: true, checkpoint: manualPolicy()})
	if err != nil {
		t.Fatalf("restart after concat: %v", err)
	}
	defer restarted.Close()
	restoredTask, err := restarted.GetTask(ctx, taskID)
	if err != nil || restoredTask.Status != domaintask.StatusSucceeded {
		t.Fatalf("Task after concat/restart: status=%s err=%v", restoredTask.Status, err)
	}
	restoredRun, err := restarted.GetRun(ctx, runID)
	if err != nil || restoredRun.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("Run after concat/restart: status=%s err=%v", restoredRun.Status, err)
	}
	reports, err := restarted.ListPendingTaskDelegationReports(ctx)
	if err != nil || len(reports) != 1 || reports[0].ReportID != report.ReportID || reports[0].Envelope.Result != report.Envelope.Result {
		t.Fatalf("report after concat/restart: %#v err=%v", reports, err)
	}
}

func TestConcatRejectsMissingNonzeroFrozenReportFile(t *testing.T) {
	root := t.TempDir()
	buildLegacyV2Fixture(t, root, 3)
	layout, err := readLayout(root)
	if err != nil || !layout.v2 {
		t.Fatalf("read legacy v2 layout: %+v err=%v", layout, err)
	}
	layout.frozen[sizedFileCount-1] = 1
	if err := os.WriteFile(layoutPath(root), formatLayout(layout.frozen), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, taskDelegationReportFilename)
	if _, err := ConcatTaskLayout(context.Background(), root, true); !errors.Is(err, ErrSealedSegmentChanged) {
		t.Fatalf("concat with missing nonzero frozen report file: %v, want ErrSealedSegmentChanged", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("concat created a missing nonzero frozen report file: %v", err)
	}
}

func saveConcatFixtureReport(t *testing.T, store *JSONLStore, current domaintask.Task, run domaintask.Run) domaintask.DelegationReport {
	t.Helper()
	ctx := context.Background()
	reportID := modulecore.NewResponseID()
	generation, err := store.WriterGeneration()
	if err != nil {
		t.Fatal(err)
	}
	current.Status = domaintask.StatusSucceeded
	current.UpdatedAt = time.Now().UTC()
	operationID := "delegation-report:" + string(reportID)
	report := domaintask.DelegationReport{
		ReportID: reportID, TaskID: current.TaskID, RunID: run.RunID,
		TaskStatus: domaintask.StatusSucceeded, TrustedSender: "shiro",
		Envelope: domainconversation.ReportEnvelope{
			ReportID: reportID, ReportRevision: 1,
			DelegationID: modulecore.NewRequestID(), DelegationRevision: 1,
			WorkTaskID: current.TaskID, WorkRunID: run.RunID,
			Status:  domainconversation.ReportStatusSucceeded,
			Summary: "completed", Result: "report survives concat",
		},
		OperationID: operationID, RequestHash: strings.Repeat("a", 64),
		WriterGeneration: generation, State: domaintask.DelegationReportPending,
		CreatedAt: time.Now().UTC().UnixNano(),
	}
	result, err := json.Marshal(struct {
		ReportID       modulecore.ResponseID `json:"report_id"`
		ReportRevision uint64                `json:"report_revision"`
		TaskID         modulecore.TaskID     `json:"task_id"`
		RunID          modulecore.RunID      `json:"run_id"`
	}{ReportID: reportID, ReportRevision: report.Envelope.ReportRevision, TaskID: current.TaskID, RunID: run.RunID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ExecuteIdempotentTaskOperationWithReport(ctx, current.TaskID, reportID, operationID, strings.Repeat("a", 64),
		func(tx domaintask.Store) (domaintask.DelegationReport, json.RawMessage, error) {
			if err := tx.SaveTask(ctx, current); err != nil {
				return domaintask.DelegationReport{}, nil, err
			}
			return report, result, nil
		})
	if err != nil {
		t.Fatalf("persist report in legacy v2 store: %v", err)
	}
	return report
}

func TestConcatOfAV1StoreChangesNothing(t *testing.T) {
	root := t.TempDir()
	stageOneStore(t, root, 4)
	before := fingerprintDir(t, root)
	report, err := ConcatTaskLayout(context.Background(), root, true)
	if err != nil {
		t.Fatalf("concat of a layout v1 store: %v", err)
	}
	if report.State != "layout_v1" {
		t.Fatalf("state = %q, want layout_v1", report.State)
	}
	requireFingerprintsEqual(t, "a layout v1 store after concat", before, fingerprintDir(t, root))
}

func TestConcatRefusesWhileTheWriterIsRunning(t *testing.T) {
	root := t.TempDir()
	clock := newTestClock("2026-10-13T08:00:00Z")
	stageOneStore(t, root, 3)
	store := openRotating(t, root, clock, 0, nil)
	populate(t, store, 3, 5)
	if _, err := ConcatTaskLayout(context.Background(), root, true); err == nil || !strings.Contains(err.Error(), ErrTaskWriterBusy.Error()) {
		t.Fatalf("concat with a live writer: %v, want ErrTaskWriterBusy", err)
	}
	if layout, _ := readLayout(root); !layout.v2 {
		t.Fatal("a refused concat changed the layout")
	}
}

// A concat that stops anywhere and is run again ends in the same place.
func TestConcatStoppedAtEveryStepAndRunAgainEndsTheSame(t *testing.T) {
	// Count the steps first.
	count := 0
	{
		root := t.TempDir()
		clock := newTestClock("2026-10-13T08:00:00Z")
		buildSegmentedStore(t, root, clock)
		if _, err := concatTaskLayout(context.Background(), root, true, func(string) { count++ }); err != nil {
			t.Fatal(err)
		}
	}
	if count < 15 {
		t.Fatalf("only %d failpoints fired in a concat of three segments", count)
	}
	for stop := 1; stop <= count; stop++ {
		root := t.TempDir()
		clock := newTestClock("2026-10-13T08:00:00Z")
		expected, _ := buildSegmentedStore(t, root, clock)

		n := 0
		crashed := didCrash(func() {
			_, _ = concatTaskLayout(context.Background(), root, true, func(stage string) {
				n++
				if n == stop {
					panic(simulatedCrash{stage})
				}
			})
		})
		if !crashed {
			t.Fatalf("stop %d: the failpoint did not fire", stop)
		}
		// A store caught half way is not opened as either layout by a writer.
		if layout, _ := readLayout(root); layout.v2 {
			if store, err := NewJSONLStoreWithOptions(root, rotationOptions(clock, 0, nil)); err == nil {
				names, _ := listSegmentDirs(root)
				seg0, _ := segmentFileSizes(root)
				_ = store.Close()
				if len(names) > 0 && seg0 != layoutFrozenOf(t, root) {
					t.Fatalf("stop %d: a store caught in the middle of a concat was opened as layout v2", stop)
				}
			}
		}
		if _, err := ConcatTaskLayout(context.Background(), root, true); err != nil {
			t.Fatalf("stop %d: running the concat again: %v", stop, err)
		}
		for _, name := range sizedFilenames {
			got, err := os.ReadFile(filepath.Join(root, name))
			if err != nil || !bytes.Equal(got, expected[name]) {
				t.Fatalf("stop %d: %s differs from the original followed by the segments (%v)", stop, name, err)
			}
		}
		if layout, err := readLayout(root); err != nil || layout.v2 {
			t.Fatalf("stop %d: layout after the second run: %+v, %v", stop, layout, err)
		}
		legacy, err := NewJSONLStoreWithOptions(root, OpenOptions{})
		if err != nil {
			t.Fatalf("stop %d: open after the second run: %v", stop, err)
		}
		_ = legacy.Close()
	}
	_ = time.Second
}

func layoutFrozenOf(t testing.TB, root string) [sizedFileCount]int64 {
	t.Helper()
	layout, err := readLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	return layout.frozen
}

// What is past the last whole segment in segment 0 must be the start of the next
// one, as an append that stopped leaves it. Anything else was written by something
// else and is neither cut away nor built on.
func TestConcatCutsBackAnUnfinishedAppendButNotForeignBytes(t *testing.T) {
	build := func(t *testing.T) (string, map[string][]byte, string) {
		root := t.TempDir()
		t.Cleanup(func() { makeWritable(t, root) })
		clock := newTestClock("2026-10-13T08:00:00Z")
		expected, _ := buildSegmentedStore(t, root, clock)
		first := segmentNames(t, root)[0]
		return root, expected, first
	}
	appendBytes := func(t *testing.T, path string, extra []byte) {
		t.Helper()
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(extra); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("the start of the next segment", func(t *testing.T) {
		root, expected, first := build(t)
		head := readSegmentFiles(t, segmentDir(root, first))[stateFilename][:7]
		appendBytes(t, filepath.Join(root, stateFilename), head)
		if _, err := ConcatTaskLayout(context.Background(), root, true); err != nil {
			t.Fatalf("concat after an unfinished append: %v", err)
		}
		got, _ := os.ReadFile(filepath.Join(root, stateFilename))
		if !bytes.Equal(got, expected[stateFilename]) {
			t.Fatal("segment 0 does not hold the original followed by the segments")
		}
	})

	t.Run("bytes that are not", func(t *testing.T) {
		root, _, _ := build(t)
		appendBytes(t, filepath.Join(root, stateFilename), []byte("not a record"))
		before, _ := os.ReadFile(filepath.Join(root, stateFilename))
		_, err := ConcatTaskLayout(context.Background(), root, true)
		if err == nil || !errors.Is(err, ErrSealedSegmentChanged) {
			t.Fatalf("concat over foreign bytes: %v, want ErrSealedSegmentChanged", err)
		}
		after, _ := os.ReadFile(filepath.Join(root, stateFilename))
		if !bytes.Equal(before, after) {
			t.Fatal("the foreign bytes were cut away")
		}
		if layout, _ := readLayout(root); !layout.v2 {
			t.Fatal("LAYOUT was set aside although the concat did not finish")
		}
	})
}
