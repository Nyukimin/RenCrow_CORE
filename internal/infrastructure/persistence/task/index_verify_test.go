package task

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func healthyPersistedStore(t testing.TB, n int) string {
	t.Helper()
	root := t.TempDir()
	store := openPersisted(t, root, manualPolicy(), nil)
	populate(t, store, 0, n)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

func logBytes(t testing.TB, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for kind := fileKind(0); kind < kindCount; kind++ {
		data, err := os.ReadFile(filepath.Join(root, kindFilename[kind]))
		if err != nil {
			t.Fatal(err)
		}
		out[kindFilename[kind]] = data
	}
	return out
}

func TestVerifyPassesOnAHealthyStore(t *testing.T) {
	root := healthyPersistedStore(t, 25)
	before := listing(t, root)
	report, err := VerifyTaskStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || report.Mismatches != 0 || len(report.Problems) != 0 {
		t.Fatalf("healthy store reported %+v", report)
	}
	if report.Sidecar != verifySidecarMatches {
		t.Fatalf("sidecar status = %q, want %q", report.Sidecar, verifySidecarMatches)
	}
	if report.Tasks != 25 || report.Runs == 0 || report.Notifications == 0 || report.Contexts == 0 {
		t.Fatalf("counts = tasks %d runs %d notifications %d contexts %d", report.Tasks, report.Runs, report.Notifications, report.Contexts)
	}
	if len(report.Files) != int(kindCount) {
		t.Fatalf("%d files in the report, want %d", len(report.Files), kindCount)
	}
	for _, file := range report.Files {
		if file.Lines != file.IndexLines || file.Bytes != file.IndexBytes {
			t.Errorf("%s: file has %d lines/%d bytes, index %d/%d", file.Name, file.Lines, file.Bytes, file.IndexLines, file.IndexBytes)
		}
	}
	after := listing(t, root)
	for name, state := range before {
		if after[name] != state {
			t.Fatalf("verify changed %s", name)
		}
	}
	if len(after) != len(before) {
		t.Fatal("verify created or removed files")
	}

	if err := os.Remove(sidecarFile(root)); err != nil {
		t.Fatal(err)
	}
	report, err = VerifyTaskStore(context.Background(), root)
	if err != nil || !report.OK || report.Sidecar != verifySidecarMissing {
		t.Fatalf("store without a sidecar: report=%+v err=%v", report, err)
	}
}

// The sidecar holds a CRC32C of every line from when it was written. A log line
// changed afterwards is invisible to the log itself (the JSON is still valid)
// but not to the comparison of the sidecar with a build from the log.
func TestVerifyDetectsALogLineChangedBehindTheSidecar(t *testing.T) {
	root := healthyPersistedStore(t, 12)
	path := filepath.Join(root, stateFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(data, []byte("indexed task 5"), []byte("indexed task X"), 1)
	if bytes.Equal(changed, data) {
		t.Fatal("test data did not contain the title to change")
	}
	if err := os.WriteFile(path, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := VerifyTaskStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if report.OK || report.Mismatches == 0 || report.Sidecar != verifySidecarDiffers {
		t.Fatalf("a changed log line went unnoticed: %+v", report)
	}
	for _, problem := range report.Problems {
		if strings.Contains(problem, "indexed task") {
			t.Fatalf("a problem quotes record content: %q", problem)
		}
	}
}

func TestVerifyReportsAnUnusableSidecarWithoutFailingTheLog(t *testing.T) {
	root := healthyPersistedStore(t, 8)
	data, _ := os.ReadFile(sidecarFile(root))
	data[len(data)/2] ^= 0x40
	if err := os.WriteFile(sidecarFile(root), data, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := VerifyTaskStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || report.Sidecar != verifySidecarUnusable || report.SidecarReason != rebuildCorrupt {
		t.Fatalf("report = %+v, want a healthy log with an unusable (corrupt) sidecar", report)
	}
}

func TestVerifyReportsALogTheIndexCannotAbsorb(t *testing.T) {
	root := healthyPersistedStore(t, 6)
	file, err := os.OpenFile(filepath.Join(root, stateFilename), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{\"not\":\"a task\"}\n"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	report, err := VerifyTaskStore(context.Background(), root)
	if err != nil {
		t.Fatalf("a bad log line is a finding, not an error: %v", err)
	}
	if report.OK || report.Mismatches == 0 || len(report.Problems) == 0 {
		t.Fatalf("report = %+v, want a failed verification", report)
	}
}

// compareIndexWithLog is where the index is held against an independent fold of
// the log; a doctored index must not get through it.
func TestComparisonCatchesADoctoredIndex(t *testing.T) {
	root := healthyPersistedStore(t, 15)
	cases := map[string]func(ix *taskIndex){
		"a task dropped from the key map": func(ix *taskIndex) {
			for key := range ix.taskMap {
				delete(ix.taskMap, key)
				return
			}
		},
		"a file line count off by one": func(ix *taskIndex) { ix.lines[kindRun]-- },
		"a task pointing at another task's line": func(ix *taskIndex) {
			ix.tasks[0].stateTail = ix.tasks[1].stateTail
			ix.tasks[0].stateHead = ix.tasks[1].stateTail
		},
		"a run's latest line replaced by an older one": func(ix *taskIndex) {
			for i := range ix.runs {
				if ix.runs[i].head != ix.runs[i].tail {
					ix.runs[i].tail = ix.runs[i].head
					return
				}
			}
			t.Fatal("no run with two lines in the corpus")
		},
		"a missing notification": func(ix *taskIndex) { ix.notifs = ix.notifs[1:] },
		"a receipt invented":     func(ix *taskIndex) { ix.receipts["nobody-wrote-this"] = receiptRec{} },
	}
	for name, doctor := range cases {
		t.Run(name, func(t *testing.T) {
			ix, err := buildTaskIndex(root)
			if err != nil {
				t.Fatal(err)
			}
			defer ix.close()
			var clean VerifyReport
			if err := compareIndexWithLog(context.Background(), root, ix, &clean); err != nil || clean.Mismatches != 0 {
				t.Fatalf("an undoctored index does not pass: err=%v report=%+v", err, clean)
			}
			doctor(ix)
			var report VerifyReport
			if err := compareIndexWithLog(context.Background(), root, ix, &report); err != nil {
				t.Fatal(err)
			}
			if report.Mismatches == 0 {
				t.Fatal("doctored index passed the comparison")
			}
		})
	}
}

func TestRebuildIndexReplacesTheSidecarAndLeavesTheLogAlone(t *testing.T) {
	root := healthyPersistedStore(t, 20)
	logsBefore := logBytes(t, root)

	t.Run("damaged sidecar", func(t *testing.T) {
		data, _ := os.ReadFile(sidecarFile(root))
		data[10] ^= 0x01
		if err := os.WriteFile(sidecarFile(root), data, 0o600); err != nil {
			t.Fatal(err)
		}
		report, err := RebuildTaskIndex(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		if report.Tasks != 20 || !strings.HasPrefix(report.Previous, "unusable") || report.SidecarBytes == 0 {
			t.Fatalf("report = %+v", report)
		}
		requireSidecarEqualsLog(t, root)
	})
	t.Run("no sidecar and no index directory", func(t *testing.T) {
		if err := os.RemoveAll(filepath.Join(root, sidecarDirName)); err != nil {
			t.Fatal(err)
		}
		report, err := RebuildTaskIndex(context.Background(), root)
		if err != nil || report.Previous != "missing" {
			t.Fatalf("report = %+v err=%v", report, err)
		}
		requireSidecarEqualsLog(t, root)
	})
	t.Run("a valid sidecar is replaced too", func(t *testing.T) {
		report, err := RebuildTaskIndex(context.Background(), root)
		if err != nil || report.Previous != "usable" {
			t.Fatalf("report = %+v err=%v", report, err)
		}
		requireSidecarEqualsLog(t, root)
	})
	for name, data := range logBytes(t, root) {
		if !bytes.Equal(data, logsBefore[name]) {
			t.Fatalf("rebuild changed %s", name)
		}
	}
	// A restart after the rebuild restores from the new sidecar.
	store := openPersisted(t, root, manualPolicy(), nil)
	if stats, _ := store.IndexStats(); stats.Source != sourceSidecar {
		t.Fatalf("source after the rebuild = %q", stats.Source)
	}
}

func TestRebuildIndexRefusesWhileAWriterIsRunning(t *testing.T) {
	root := t.TempDir()
	store := openPersisted(t, root, manualPolicy(), nil)
	populate(t, store, 0, 3)
	_, err := RebuildTaskIndex(context.Background(), root)
	if !errors.Is(err, ErrTaskWriterBusy) {
		t.Fatalf("rebuild beside a live writer = %v, want ErrTaskWriterBusy", err)
	}
}
