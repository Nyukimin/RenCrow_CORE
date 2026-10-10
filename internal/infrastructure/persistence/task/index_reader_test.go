package task

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// readerAnswers asks a reader every question the CLI asks and returns the
// answers as JSON, so two readers can be compared.
func readerAnswers(t testing.TB, reader *JSONLStore) map[string]string {
	t.Helper()
	ctx := context.Background()
	answers := map[string]string{}
	record := func(name string, value any, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		data, merr := json.Marshal(value)
		if merr != nil {
			t.Fatal(merr)
		}
		answers[name] = string(data)
	}
	tasks, err := reader.ListTasks(ctx, domaintask.Filter{})
	record("tasks", tasks, err)
	limited, err := reader.ListTasks(ctx, domaintask.Filter{Limit: 7})
	record("tasks(limit 7)", limited, err)
	running, err := reader.ListTasks(ctx, domaintask.Filter{Status: domaintask.StatusRunning, Limit: 5})
	record("tasks(running)", running, err)
	runs, err := reader.ListRuns(ctx, domaintask.RunFilter{})
	record("runs", runs, err)
	notifications, err := reader.ListNotifications(ctx, 50, false)
	record("notifications", notifications, err)
	for _, task := range tasks {
		got, err := reader.GetTask(ctx, task.TaskID)
		record("task "+string(task.TaskID), got, err)
		shared, err := reader.GetContext(ctx, task.TaskID)
		if err != nil && !errors.Is(err, domaintask.ErrNotFound) {
			t.Fatalf("context of %s: %v", task.TaskID, err)
		}
		record("context "+string(task.TaskID), shared, nil)
		taskRuns, err := reader.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
		record("runs of "+string(task.TaskID), taskRuns, err)
	}
	return answers
}

func requireSameAnswers(t testing.TB, want, got map[string]string) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("%d answers vs %d", len(want), len(got))
	}
	names := make([]string, 0, len(want))
	for name := range want {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if want[name] != got[name] {
			t.Fatalf("answer %q differs:\n legacy : %s\n indexed: %s", name, want[name], got[name])
		}
	}
}

// listing records every file under root with its size and modification time.
func listing(t testing.TB, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		out[rel] = info.ModTime().String() + "/" + info.Mode().String() + "/" + sizeString(info.Size())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sizeString(n int64) string {
	data, _ := json.Marshal(n)
	return string(data)
}

// withoutSidecar copies the log of root into a new directory without the index/
// directory, so a reader there takes the original (fold) path.
func withoutSidecar(t testing.TB, root string) string {
	t.Helper()
	dst := t.TempDir()
	copyDir(t, root, dst, func(name string) bool { return name != sidecarDirName && name != ".writer.lock" })
	return dst
}

func TestReaderWithoutASidecarKeepsReadingTheLogDirectly(t *testing.T) {
	root := t.TempDir()
	writer, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true}) // an index that is never persisted
	if err != nil {
		t.Fatal(err)
	}
	populate(t, writer, 0, 10)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := NewJSONLReader(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, ok := reader.IndexStats(); ok {
		t.Fatal("a reader of a store without a sidecar built an index")
	}
	if _, err := reader.ListTasks(context.Background(), domaintask.Filter{}); err != nil {
		t.Fatal(err)
	}
}

func TestReaderRestoresTheSidecarAndNeverWrites(t *testing.T) {
	root := t.TempDir()
	writer := openPersisted(t, root, manualPolicy(), nil)
	populate(t, writer, 0, 40)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	legacyRoot := withoutSidecar(t, root)
	legacy, err := NewJSONLReader(legacyRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	want := readerAnswers(t, legacy)

	before := listing(t, root)
	reader, err := NewJSONLReader(root)
	if err != nil {
		t.Fatal(err)
	}
	stats, ok := reader.IndexStats()
	if !ok || stats.Source != sourceSidecar {
		t.Fatalf("reader index = %+v ok=%v, want one restored from the sidecar", stats, ok)
	}
	requireSameAnswers(t, want, readerAnswers(t, reader))
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if after := listing(t, root); len(after) != len(before) {
		t.Fatalf("the reader changed the directory: %v -> %v", before, after)
	} else {
		for name, state := range before {
			if after[name] != state {
				t.Fatalf("the reader changed %s", name)
			}
		}
	}
}

func TestReaderReplaysTheLogWrittenAfterTheSidecar(t *testing.T) {
	root := t.TempDir()
	writer := openPersisted(t, root, manualPolicy(), nil)
	populate(t, writer, 0, 12)
	if err := writer.idx.ck.checkpointNow("test"); err != nil {
		t.Fatal(err)
	}
	populate(t, writer, 12, 30)
	if err := writer.closeStore(false); err != nil {
		t.Fatal(err)
	}
	legacy, err := NewJSONLReader(withoutSidecar(t, root))
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	want := readerAnswers(t, legacy)

	before, _ := os.ReadFile(sidecarFile(root))
	reader, err := NewJSONLReader(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if stats, _ := reader.IndexStats(); stats.Source != sourceSidecar {
		t.Fatalf("source = %q, want the sidecar plus a replay", stats.Source)
	}
	requireSameAnswers(t, want, readerAnswers(t, reader))
	if after, _ := os.ReadFile(sidecarFile(root)); string(after) != string(before) {
		t.Fatal("the reader rewrote the sidecar after replaying")
	}
}

func TestReaderBuildsInMemoryWhenTheSidecarIsDamagedAndLeavesItAlone(t *testing.T) {
	root := t.TempDir()
	writer := openPersisted(t, root, manualPolicy(), nil)
	populate(t, writer, 0, 15)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	legacy, err := NewJSONLReader(withoutSidecar(t, root))
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	want := readerAnswers(t, legacy)

	data, _ := os.ReadFile(sidecarFile(root))
	data[len(data)/3] ^= 0x01
	if err := os.WriteFile(sidecarFile(root), data, 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewJSONLReader(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	stats, ok := reader.IndexStats()
	if !ok || stats.Source != sourceRebuilt || stats.RebuildReason != rebuildCorrupt {
		t.Fatalf("reader index = %+v, want rebuilt in memory (corrupt)", stats)
	}
	requireSameAnswers(t, want, readerAnswers(t, reader))
	if after, _ := os.ReadFile(sidecarFile(root)); string(after) != string(data) {
		t.Fatal("the reader repaired the sidecar; only the writer may write it")
	}
}

func TestIndexedReaderBuildsAnIndexEvenWithoutASidecar(t *testing.T) {
	root := t.TempDir()
	writer, err := NewJSONLStoreWithOptions(root, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	populate(t, writer, 0, 6)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := NewIndexedJSONLReader(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	stats, ok := reader.IndexStats()
	if !ok || stats.Source != sourceRebuilt || stats.RebuildReason != rebuildMissing {
		t.Fatalf("indexed reader = %+v ok=%v", stats, ok)
	}
	if _, err := os.Stat(sidecarFile(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an indexed reader created a sidecar")
	}
}

func TestReaderSeesTheStoreAsItWasWhenOpened(t *testing.T) {
	root := t.TempDir()
	writer := openPersisted(t, root, manualPolicy(), nil)
	populate(t, writer, 0, 5)
	reader, err := NewIndexedJSONLReader(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	populate(t, writer, 5, 9) // after the reader opened
	ctx := context.Background()
	seen, err := reader.ListTasks(ctx, domaintask.Filter{})
	if err != nil || len(seen) != 5 {
		t.Fatalf("reader opened before the later writes sees %d tasks (err=%v), want its snapshot of 5", len(seen), err)
	}
	fresh, err := NewIndexedJSONLReader(root)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if seen, err := fresh.ListTasks(ctx, domaintask.Filter{}); err != nil || len(seen) != 9 {
		t.Fatalf("a reader opened later sees %d tasks (err=%v), want 9", len(seen), err)
	}
}

// Run with -race: readers open and read while the writer commits and
// checkpoints. Every reader must get a snapshot in which each listed Task can
// be fetched (R-12).
func TestReadersGetConsistentSnapshotsWhileTheWriterRuns(t *testing.T) {
	root := t.TempDir()
	policy := &checkpointPolicy{interval: 3 * time.Millisecond, lineThreshold: 4, retryBase: time.Millisecond, retryMax: 4 * time.Millisecond}
	writer := openPersisted(t, root, policy, nil)
	populate(t, writer, 0, 4)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 4; n < 40; n++ {
			populate(t, writer, n, n+1)
		}
		close(stop)
	}()
	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			for {
				reader, err := NewJSONLReader(root)
				if err != nil {
					t.Errorf("open reader: %v", err)
					return
				}
				tasks, err := reader.ListTasks(ctx, domaintask.Filter{})
				if err != nil {
					t.Errorf("list: %v", err)
					_ = reader.Close()
					return
				}
				for _, task := range tasks {
					if _, err := reader.GetTask(ctx, task.TaskID); err != nil {
						t.Errorf("task listed but not gettable: %v", err)
					}
				}
				_ = reader.Close()
				select {
				case <-stop:
					return
				default:
				}
			}
		}()
	}
	wg.Wait()
}

func TestTaskHistoryReturnsEveryStoredLineOfTheTask(t *testing.T) {
	root := t.TempDir()
	writer := openPersisted(t, root, manualPolicy(), nil)
	ctx := context.Background()
	generation, _ := writer.WriterGeneration()
	now := indexTestNow
	task := indexedTestTask(1, now)
	other := indexedTestTask(2, now)
	for _, value := range []domaintask.Task{task, other} {
		if err := writer.SaveTask(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	run := indexedTestRun(task, 1, generation, now)
	if err := writer.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	closed := run
	done := now.Add(time.Second)
	closed.Status, closed.CompletedAt, closed.Summary = domaintask.RunStatusSucceeded, &done, "done"
	if err := writer.SaveRun(ctx, closed); err != nil {
		t.Fatal(err)
	}
	if err := writer.SaveContext(ctx, domaintask.SharedRoleContext{TaskID: task.TaskID, CurrentPlan: "one", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := writer.SaveContext(ctx, domaintask.SharedRoleContext{TaskID: task.TaskID, CurrentPlan: "two", UpdatedAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := writer.SaveNotification(ctx, domaintask.NewNotification(task, now)); err != nil {
		t.Fatal(err)
	}
	if err := writer.SaveNotification(ctx, domaintask.NewNotification(other, now)); err != nil {
		t.Fatal(err)
	}
	task.Status, task.UpdatedAt = domaintask.StatusRunning, now.Add(time.Minute)
	if err := writer.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := NewIndexedJSONLReader(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	history, err := reader.TaskHistory(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	var lastFile string
	var lastOffset int64 = -1
	for _, line := range history {
		counts[line.File]++
		if line.File == lastFile && line.Offset <= lastOffset {
			t.Fatalf("lines of %s are not in log order: %d after %d", line.File, line.Offset, lastOffset)
		}
		lastFile, lastOffset = line.File, line.Offset
		var probe struct {
			TaskID modulecore.TaskID `json:"task_id"`
		}
		if err := json.Unmarshal(line.Line, &probe); err != nil || probe.TaskID != task.TaskID {
			t.Fatalf("history line of %s does not belong to the task: %s (%v)", line.File, line.Line, err)
		}
	}
	want := map[string]int{stateFilename: 2, runFilename: 2, contextFilename: 2, notificationsFilename: 1}
	for file, n := range want {
		if counts[file] != n {
			t.Errorf("%s: %d lines, want %d", file, counts[file], n)
		}
	}
	if len(counts) != len(want) {
		t.Errorf("unexpected files in the history: %v", counts)
	}
	if _, err := reader.TaskHistory(ctx, indexedTestTask(99, now).TaskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("history of an unknown task = %v, want ErrNotFound", err)
	}
	plain, err := NewJSONLReader(root)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if _, err := plain.TaskHistory(ctx, task.TaskID); err != nil {
		t.Fatalf("a reader of a store with a sidecar has the index and must answer: %v", err)
	}
}
