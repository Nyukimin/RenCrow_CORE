package task

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// populate writes Tasks n in [from, to) with the other record kinds the log
// holds: Runs (opened and closed), contexts and notifications.
func populate(t testing.TB, store *JSONLStore, from, to int) {
	t.Helper()
	ctx := context.Background()
	generation, err := store.WriterGeneration()
	if err != nil {
		t.Fatal(err)
	}
	for n := from; n < to; n++ {
		now := indexTestNow.Add(time.Duration(n) * time.Second)
		task := indexedTestTask(n, now)
		if n%4 == 0 {
			task.ParentTaskID = indexedTestTask(0, now).TaskID
			if n == 0 {
				task.ParentTaskID = ""
			}
		}
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatalf("populate task %d: %v", n, err)
			}
		}
		must(store.SaveTask(ctx, task))
		if n%2 == 0 {
			run := indexedTestRun(task, n, generation, now)
			must(store.SaveRun(ctx, run))
			closed := run
			completed := now.Add(time.Second)
			closed.Status, closed.CompletedAt, closed.Summary = domaintask.RunStatusSucceeded, &completed, "done"
			must(store.SaveRun(ctx, closed))
		}
		if n%3 == 0 {
			must(store.SaveContext(ctx, domaintask.SharedRoleContext{TaskID: task.TaskID, CurrentPlan: "plan", UpdatedAt: now}))
		}
		if n%2 == 1 {
			must(store.SaveNotification(ctx, domaintask.NewNotification(task, now)))
		}
		task.Status, task.UpdatedAt = domaintask.StatusRunning, now.Add(time.Minute)
		must(store.SaveTask(ctx, task))
	}
}

// requireStoreEqualsFullBuild is the property every restore must have: the index
// the store holds is the one a build from the log alone gives, and it covers the
// whole log.
func requireStoreEqualsFullBuild(t testing.TB, store *JSONLStore, root string) {
	t.Helper()
	built, err := buildTaskIndex(root)
	if err != nil {
		t.Fatalf("build from the log: %v", err)
	}
	defer built.close()
	if err := equivalentIndex(store.idx, built); err != nil {
		t.Fatalf("the store's index differs from a build from the log: %v", err)
	}
	requireIndexCoversLog(t, store, root)
}

func TestRestoreFromSidecarGivesTheIndexOfAFullBuild(t *testing.T) {
	root := t.TempDir()
	first := openPersisted(t, root, manualPolicy(), nil)
	populate(t, first, 0, 30)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := openPersisted(t, root, manualPolicy(), nil)
	stats, _ := second.IndexStats()
	if stats.Source != sourceSidecar || stats.RebuildReason != "" || second.idx.replayedLines != 0 {
		t.Fatalf("source=%q reason=%q replayed=%d, want a plain restore from the sidecar", stats.Source, stats.RebuildReason, second.idx.replayedLines)
	}
	requireStoreEqualsFullBuild(t, second, root)
	if stats.Checkpoints != 0 {
		t.Fatalf("an unchanged restore rewrote the sidecar (%d checkpoints)", stats.Checkpoints)
	}
}

func TestRestoreReplaysTheLogWrittenAfterTheSidecar(t *testing.T) {
	root := t.TempDir()
	first := openPersisted(t, root, manualPolicy(), nil)
	populate(t, first, 0, 10)
	if err := first.idx.ck.checkpointNow("test"); err != nil {
		t.Fatal(err)
	}
	covered := readSidecarHeaderLines(t, root)
	populate(t, first, 10, 25)
	// The writer dies after its last commit without writing a final checkpoint.
	if err := first.closeStore(false); err != nil {
		t.Fatal(err)
	}
	if now := readSidecarHeaderLines(t, root); now != covered {
		t.Fatalf("sidecar changed although no checkpoint ran: %v -> %v", covered, now)
	}

	second := openPersisted(t, root, manualPolicy(), nil)
	stats, _ := second.IndexStats()
	if stats.Source != sourceSidecar {
		t.Fatalf("source = %q, want a restore from the sidecar plus a replay", stats.Source)
	}
	var behind int64
	for k := range covered {
		behind += second.idx.lines[k] - covered[k]
	}
	if behind == 0 || second.idx.replayedLines != behind {
		t.Fatalf("replayed %d lines, the log is %d lines ahead of the sidecar", second.idx.replayedLines, behind)
	}
	requireStoreEqualsFullBuild(t, second, root)
	if stats.Checkpoints != 1 {
		t.Fatalf("checkpoints after a replay = %d, want one written at open", stats.Checkpoints)
	}
	requireSidecarEqualsLog(t, root)

	// The restored index must serve writes that depend on lines it only knows by
	// position: updates of old Tasks, Runs, contexts.
	ctx := context.Background()
	for n := 2; n < 9; n++ {
		old, err := second.GetTask(ctx, indexedTestTask(n, indexTestNow).TaskID)
		if err != nil {
			t.Fatalf("task %d known only from the sidecar: %v", n, err)
		}
		old.Status, old.UpdatedAt = domaintask.StatusSucceeded, old.UpdatedAt.Add(time.Hour)
		if err := second.SaveTask(ctx, old); err != nil {
			t.Fatalf("update task %d: %v", n, err)
		}
		if err := second.SaveContext(ctx, domaintask.SharedRoleContext{TaskID: old.TaskID, CurrentPlan: "revised", UpdatedAt: old.UpdatedAt}); err != nil {
			t.Fatalf("context of task %d: %v", n, err)
		}
		if _, err := second.ListRuns(ctx, domaintask.RunFilter{TaskID: old.TaskID}); err != nil {
			t.Fatalf("runs of task %d: %v", n, err)
		}
	}
	requireStoreEqualsFullBuild(t, second, root)
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	third := openPersisted(t, root, manualPolicy(), nil)
	requireStoreEqualsFullBuild(t, third, root)
}

func readSidecarHeaderLines(t testing.TB, root string) [kindCount]int64 {
	t.Helper()
	_, header := readSidecar(t, root)
	return header.Lines
}

func TestMissingSidecarRebuildsAndWritesOne(t *testing.T) {
	root := t.TempDir()
	first := openPersisted(t, root, manualPolicy(), nil)
	populate(t, first, 0, 12)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sidecarFile(root)); err != nil {
		t.Fatal(err)
	}
	second := openPersisted(t, root, manualPolicy(), nil)
	stats, _ := second.IndexStats()
	if stats.Source != sourceRebuilt || stats.RebuildReason != rebuildMissing {
		t.Fatalf("source=%q reason=%q, want rebuilt/missing", stats.Source, stats.RebuildReason)
	}
	requireStoreEqualsFullBuild(t, second, root)
	requireSidecarEqualsLog(t, root) // written right after the rebuild
}

func TestUnusableSidecarsFallBackToARebuildWithTheRightReason(t *testing.T) {
	root0 := t.TempDir()
	seed := openPersisted(t, root0, manualPolicy(), nil)
	populate(t, seed, 0, 20)
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	good, err := os.ReadFile(sidecarFile(root0))
	if err != nil {
		t.Fatal(err)
	}
	headerAt := func(field string, kind fileKind) int {
		switch field {
		case "applied":
			return 36 + 8*int(kind)
		case "lines":
			return 36 + 8*int(kindCount) + 8*int(kind)
		}
		t.Fatalf("unknown header field %s", field)
		return 0
	}
	bump := func(at int, delta int64) func([]byte) []byte {
		return func(b []byte) []byte {
			out := append([]byte(nil), b...)
			binary.LittleEndian.PutUint64(out[at:], uint64(int64(binary.LittleEndian.Uint64(out[at:]))+delta))
			return resealSidecar(out)
		}
	}
	cases := []struct {
		name   string
		mutate func([]byte) []byte
		reason string
	}{
		{"bit flipped", func(b []byte) []byte { out := append([]byte(nil), b...); out[len(out)/2] ^= 0x04; return out }, rebuildCorrupt},
		{"truncated", func(b []byte) []byte { return b[:len(b)/2] }, rebuildCorrupt},
		{"empty", func([]byte) []byte { return nil }, rebuildCorrupt},
		{"torn end", func(b []byte) []byte { return b[:len(b)-3] }, rebuildCorrupt},
		{"another version", func(b []byte) []byte {
			out := append([]byte(nil), b...)
			binary.LittleEndian.PutUint16(out[4:], 9)
			return resealSidecar(out)
		}, rebuildVersion},
		{"covers more than the file", bump(headerAt("applied", kindState), 100_000), rebuildSize},
		{"prefix ends mid-line", bump(headerAt("applied", kindNotification), -1), rebuildCount},
		{"wrong line count", bump(headerAt("lines", kindRun), 1), rebuildCount},
		{"wrong line count in notifications", bump(headerAt("lines", kindNotification), 1), rebuildCount},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			// The same log, so a rebuild is checked against the same truth.
			copyDir(t, root0, root, func(name string) bool { return name != sidecarDirName })
			if err := os.MkdirAll(filepath.Join(root, sidecarDirName), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sidecarFile(root), tc.mutate(good), 0o600); err != nil {
				t.Fatal(err)
			}
			store := openPersisted(t, root, manualPolicy(), nil)
			stats, _ := store.IndexStats()
			if stats.Source != sourceRebuilt || stats.RebuildReason != tc.reason {
				t.Fatalf("source=%q reason=%q, want rebuilt/%s", stats.Source, stats.RebuildReason, tc.reason)
			}
			requireStoreEqualsFullBuild(t, store, root)
			requireSidecarEqualsLog(t, root) // replaced by a good one
		})
	}
}

// copyDir copies the regular files of src (and its subdirectories) that keep
// accepts into dst.
func copyDir(t testing.TB, src, dst string, keep func(name string) bool) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !keep(entry.Name()) || !entry.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, entry.Name()), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// A sidecar written for a longer history must never win over a log that is
// shorter now: the log is the truth, so the index is rebuilt from it.
func TestSidecarOfALongerHistoryYieldsToTheLog(t *testing.T) {
	root := t.TempDir()
	first := openPersisted(t, root, manualPolicy(), nil)
	populate(t, first, 0, 15)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, notificationsFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(data), "\n") // the last element is empty
	if len(lines) < 4 {
		t.Fatalf("only %d notification lines to cut", len(lines))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines[:len(lines)-3], "")), 0o600); err != nil { // two lines fewer
		t.Fatal(err)
	}
	second := openPersisted(t, root, manualPolicy(), nil)
	stats, _ := second.IndexStats()
	if stats.Source != sourceRebuilt || stats.RebuildReason != rebuildSize {
		t.Fatalf("source=%q reason=%q, want rebuilt/size", stats.Source, stats.RebuildReason)
	}
	requireStoreEqualsFullBuild(t, second, root)
	if got := second.idx.lines[kindNotification]; got != int64(len(lines)-3) {
		t.Fatalf("notification lines in the index = %d, the shortened log has %d", got, len(lines)-3)
	}
}

// INV-5: a writer never takes a generation that a persisted Run already holds,
// whether the index comes from the sidecar, from the log written after it, or
// from a rebuild.
func TestWriterGenerationFenceHoldsAcrossRestore(t *testing.T) {
	rollBackCounter := func(t *testing.T, root string, to string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, ".writer.lock"), []byte(to), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const refused = "writer generation does not advance persisted Run ownership"

	t.Run("generation recorded in the sidecar", func(t *testing.T) {
		root := t.TempDir()
		first := openPersisted(t, root, manualPolicy(), nil)
		populate(t, first, 0, 4) // Runs of generation 1
		if err := first.Close(); err != nil {
			t.Fatal(err)
		}
		rollBackCounter(t, root, "")
		if store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true, Persist: true, checkpoint: manualPolicy()}); err == nil || !strings.Contains(err.Error(), refused) {
			if store != nil {
				_ = store.Close()
			}
			t.Fatalf("open with a rolled-back counter = %v, want %q", err, refused)
		}
	})

	t.Run("generation only in the log written after the sidecar", func(t *testing.T) {
		root := t.TempDir()
		first := openPersisted(t, root, manualPolicy(), nil)
		populate(t, first, 0, 2) // generation 1
		if err := first.Close(); err != nil {
			t.Fatal(err)
		}
		second := openPersisted(t, root, manualPolicy(), nil) // generation 2
		populate(t, second, 2, 6)
		if err := second.closeStore(false); err != nil { // no checkpoint: the sidecar knows only generation 1
			t.Fatal(err)
		}
		if _, header := readSidecar(t, root); header.MaxRunGeneration != 1 {
			t.Fatalf("sidecar max run generation = %d, want 1", header.MaxRunGeneration)
		}
		rollBackCounter(t, root, "1\n")
		if store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true, Persist: true, checkpoint: manualPolicy()}); err == nil || !strings.Contains(err.Error(), refused) {
			if store != nil {
				_ = store.Close()
			}
			t.Fatalf("open with a rolled-back counter = %v, want %q", err, refused)
		}
	})

	t.Run("sidecar gone", func(t *testing.T) {
		root := t.TempDir()
		first := openPersisted(t, root, manualPolicy(), nil)
		populate(t, first, 0, 4)
		if err := first.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(sidecarFile(root)); err != nil {
			t.Fatal(err)
		}
		rollBackCounter(t, root, "")
		if store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true, Persist: true, checkpoint: manualPolicy()}); err == nil || !strings.Contains(err.Error(), refused) {
			if store != nil {
				_ = store.Close()
			}
			t.Fatalf("open with a rolled-back counter = %v, want %q", err, refused)
		}
	})
}

func TestReplayStoppedHalfwayLeavesNoTraceAndTheNextOpenIsComplete(t *testing.T) {
	root := t.TempDir()
	first := openPersisted(t, root, manualPolicy(), nil)
	populate(t, first, 0, 8)
	if err := first.idx.ck.checkpointNow("test"); err != nil {
		t.Fatal(err)
	}
	populate(t, first, 8, 16)
	if err := first.closeStore(false); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(sidecarFile(root))
	if err != nil {
		t.Fatal(err)
	}

	for _, stage := range []string{"replay-" + stateFilename, "replay-" + runFilename, "replay-" + contextFilename, "replay-" + notificationsFilename} {
		t.Run(stage, func(t *testing.T) {
			stopped := func() (stopped bool) {
				defer func() {
					if recover() != nil {
						stopped = true
					}
				}()
				store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true, Persist: true, checkpoint: manualPolicy(), failpoint: func(s string) {
					if s == stage {
						panic("process killed during the replay")
					}
				}})
				if err == nil {
					_ = store.Close()
				}
				return false
			}()
			if !stopped {
				t.Fatal("the failpoint did not fire")
			}
			after, err := os.ReadFile(sidecarFile(root))
			if err != nil || string(after) != string(before) {
				t.Fatalf("an interrupted replay changed the sidecar (err=%v)", err)
			}
		})
	}
	final := openPersisted(t, root, manualPolicy(), nil)
	requireStoreEqualsFullBuild(t, final, root)
}

// A sidecar that is intact and plausible but belongs to other bytes must not be
// believed: the last line of every file it covers is read back and its CRC32C
// compared with the one the sidecar recorded.
func TestSidecarThatDoesNotDescribeTheLastLinesOfTheLogIsRebuilt(t *testing.T) {
	root := t.TempDir()
	store := openPersisted(t, root, manualPolicy(), nil)
	populate(t, store, 0, 10)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// For each file, text that appears in its last line and a same-length
	// replacement: the line stays valid JSON of the same size, so only its
	// checksum can tell it apart.
	edits := map[string][2]string{
		stateFilename:         {"indexed task", "indexdd task"},
		runFilename:           {`"summary":"done"`, `"summary":"dine"`},
		contextFilename:       {`"plan"`, `"plam"`},
		notificationsFilename: {"indexed task", "indexdd task"},
	}
	for _, file := range []string{stateFilename, runFilename, contextFilename, notificationsFilename} {
		t.Run(file, func(t *testing.T) {
			path := filepath.Join(root, file)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.WriteFile(path, original, 0o600) }()
			lines := bytes.SplitAfter(original, []byte("\n")) // the final element is empty
			last := lines[len(lines)-2]
			edited := bytes.Replace(last, []byte(edits[file][0]), []byte(edits[file][1]), 1)
			if bytes.Equal(edited, last) || len(edited) != len(last) {
				t.Fatalf("the edit of the last line of %s did not apply (or changed its length)", file)
			}
			changed := append(append([]byte(nil), original[:len(original)-len(last)]...), edited...)
			if err := os.WriteFile(path, changed, 0o600); err != nil {
				t.Fatal(err)
			}
			reopened := openPersisted(t, root, manualPolicy(), nil)
			stats, _ := reopened.IndexStats()
			if stats.Source != sourceRebuilt || stats.RebuildReason != rebuildContent {
				t.Fatalf("source=%q reason=%q, want rebuilt/%s", stats.Source, stats.RebuildReason, rebuildContent)
			}
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
