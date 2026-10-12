package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// The tasks commands on a store with segments (layout v2): the storage commands
// and the reap command must work on it exactly as on the plain layout, and
// `layout concat` must return it.

// moveToSegments opens the store at root with rotation on, which moves a layout
// v1 store to layout v2, and closes it again.
func moveToSegments(t *testing.T, root string) {
	t.Helper()
	store, err := taskpersistence.NewJSONLStoreWithOptions(root, taskpersistence.OpenOptions{Index: true, Persist: true, Rotation: true})
	if err != nil {
		t.Fatalf("move the store to segments: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// unfreezeForCleanup makes the read-only files of segment 0 removable again.
func unfreezeForCleanup(t *testing.T, root string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && !entry.IsDir() {
				_ = os.Chmod(path, 0o644)
			}
			return nil
		})
	})
}

func TestStorageSubcommandsIncludeLayout(t *testing.T) {
	for args, want := range map[string]bool{"layout show": true, "LAYOUT concat --apply": true, "--compact layout": true} {
		if got := isTaskStorageSubcommand(strings.Fields(args)); got != want {
			t.Errorf("isTaskStorageSubcommand(%q) = %v, want %v", args, got, want)
		}
	}
}

func TestTasksLayoutShowSaysWhatTheStoreIsMadeOf(t *testing.T) {
	root, _ := seedIndexedTaskStore(t, 6)
	unfreezeForCleanup(t, root)
	var before taskpersistence.LayoutReport
	code, stdout, stderr := runStorage([]string{"layout", "show"}, root)
	if code != 0 {
		t.Fatalf("layout show exit = %d stderr=%s", code, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &before); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout)
	}
	if before.Layout != "v1" || len(before.Segments) != 1 || before.Segments[0].Name != "legacy" || before.Segments[0].Bytes == 0 || before.Sidecar != "present" {
		t.Fatalf("layout of a plain store = %+v", before)
	}

	moveToSegments(t, root)
	var after taskpersistence.LayoutReport
	code, stdout, stderr = runStorage([]string{"layout", "show"}, root)
	if code != 0 {
		t.Fatalf("layout show exit = %d stderr=%s", code, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &after); err != nil {
		t.Fatal(err)
	}
	if after.Layout != "v2" || len(after.Segments) != 2 || !after.Segment0ReadOnly || !after.Segment0MatchesLayout || after.Segments[0].Bytes != before.Segments[0].Bytes {
		t.Fatalf("layout of a store with segments = %+v (before: %+v)", after, before)
	}
	if strings.Contains(stdout, "cli index task") {
		t.Fatal("layout show printed record content")
	}
	if code, _, stderr := runStorage([]string{"layout"}, root); code != 1 || !strings.Contains(stderr, "usage:") {
		t.Fatalf("layout without a subcommand: exit=%d stderr=%q", code, stderr)
	}
	if code, _, stderr := runStorage([]string{"layout", "frobnicate"}, root); code != 1 || !strings.Contains(stderr, "unknown layout subcommand") {
		t.Fatalf("layout with an unknown subcommand: exit=%d stderr=%q", code, stderr)
	}
}

func TestStorageCommandsWorkOnAStoreWithSegments(t *testing.T) {
	root, taskID := seedIndexedTaskStore(t, 8)
	unfreezeForCleanup(t, root)
	moveToSegments(t, root)

	// New writes go to the first segment, so a Task has history in both.
	store, err := taskpersistence.NewJSONLStoreWithOptions(root, taskpersistence.OpenOptions{Index: true, Persist: true, Rotation: true})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	current.Status, current.UpdatedAt = domaintask.StatusSucceeded, current.UpdatedAt.Add(2*60*1e9)
	if err := store.SaveTask(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runStorage([]string{"verify"}, root)
	if code != 0 {
		t.Fatalf("verify exit = %d stderr=%s stdout=%s", code, stderr, stdout)
	}
	var report struct {
		OK       bool   `json:"ok"`
		Layout   string `json:"layout"`
		Segments int    `json:"segments"`
		Sidecar  string `json:"sidecar"`
		Tasks    int    `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatal(err)
	}
	if !report.OK || report.Layout != "v2" || report.Segments != 2 || report.Sidecar != "matches_log" || report.Tasks != 8 {
		t.Fatalf("verify report = %+v", report)
	}

	code, stdout, stderr = runStorage([]string{"history", string(taskID)}, root)
	if code != 0 {
		t.Fatalf("history exit = %d stderr=%s", code, stderr)
	}
	var history struct {
		Lines []struct {
			File    string `json:"file"`
			Segment string `json:"segment"`
		} `json:"lines"`
	}
	if err := json.Unmarshal([]byte(stdout), &history); err != nil {
		t.Fatal(err)
	}
	segments := map[string]int{}
	states := 0
	for _, line := range history.Lines {
		segments[line.Segment]++
		if line.File == "task_state.jsonl" {
			states++
		}
	}
	if len(segments) != 2 || segments["legacy"] == 0 || states != 3 {
		t.Fatalf("history spans %v with %d state lines, want the original segment and the new one, 3 versions", segments, states)
	}

	// The sidecar is replaced as the store's own: rebuild-index writes the file of
	// layout v2 and leaves the frozen segment's alone.
	frozen, err := os.ReadFile(filepath.Join(root, "index", "seg-legacy.tsi"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "index", "tasks.tsi")); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runStorage([]string{"rebuild-index"}, root)
	if code != 0 || !strings.Contains(stdout, `"previous_sidecar": "missing"`) {
		t.Fatalf("rebuild-index exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "index", "tasks.tsi")); err != nil {
		t.Fatalf("rebuild-index did not write the sidecar of layout v2: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(root, "index", "seg-legacy.tsi"))
	if err != nil || string(after) != string(frozen) {
		t.Fatal("rebuild-index changed the sidecar of segment 0")
	}
	if code, _, stderr := runStorage([]string{"verify"}, root); code != 0 {
		t.Fatalf("verify after rebuild-index: exit=%d stderr=%s", code, stderr)
	}

	// The readers the other tasks commands use see all of it.
	reader, err := taskpersistence.NewJSONLReader(root)
	if err != nil {
		t.Fatalf("reader of a store with segments: %v", err)
	}
	defer reader.Close()
	if got, err := reader.GetTask(context.Background(), taskID); err != nil || got.Status != domaintask.StatusSucceeded {
		t.Fatalf("reader: %+v, %v", got, err)
	}
	listed, err := reader.ListTasks(context.Background(), domaintask.Filter{})
	if err != nil || len(listed) != 8 {
		t.Fatalf("reader lists %d tasks, %v", len(listed), err)
	}
}

func TestTasksReapQueuedWorksOnAStoreWithSegments(t *testing.T) {
	world := seedReapCLIStore(t)
	unfreezeForCleanup(t, world.root)
	moveToSegments(t, world.root)

	code, stdout, stderr := runReap([]string{"reap-queued", "--created-before", reapCLICutoff}, world.root)
	if code != 0 {
		t.Fatalf("dry run exit = %d stderr=%s stdout=%s", code, stderr, stdout)
	}
	if out := decodeReapOutput(t, stdout); out.Report == nil || out.Report.Candidates != len(world.candidates) {
		t.Fatalf("dry run report = %+v", out.Report)
	}
	code, stdout, stderr = runReap([]string{"reap-queued", "--created-before", reapCLICutoff, "--apply"}, world.root)
	if code != 0 {
		t.Fatalf("apply exit = %d stderr=%s stdout=%s", code, stderr, stdout)
	}
	reader, err := taskpersistence.NewJSONLReader(world.root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ctx := context.Background()
	for _, id := range world.candidates {
		task, err := reader.GetTask(ctx, id)
		if err != nil || task.Status != domaintask.StatusCancelled {
			t.Fatalf("candidate %s after apply: %v %v", id, task.Status, err)
		}
	}
	for _, id := range world.kept {
		task, err := reader.GetTask(ctx, id)
		if err != nil || task.Status == domaintask.StatusCancelled {
			t.Fatalf("a Task that is not a candidate was touched: %s %v %v", id, task.Status, err)
		}
	}
	_ = taskmanager.DefaultParallelLimits
}

func TestTasksLayoutConcatReturnsTheStoreToThePlainLayout(t *testing.T) {
	root, taskID := seedIndexedTaskStore(t, 6)
	unfreezeForCleanup(t, root)
	moveToSegments(t, root)

	// A running CORE holds the writer lock: the concat refuses.
	writer, err := taskpersistence.NewJSONLStoreWithOptions(root, taskpersistence.OpenOptions{Index: true, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runStorage([]string{"layout", "concat", "--apply"}, root)
	if code != 1 || !strings.Contains(stderr, "writer is busy") || !strings.Contains(stderr, "stop the running CORE") {
		t.Fatalf("concat with CORE running: exit=%d stderr=%q", code, stderr)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runStorage([]string{"layout", "concat"}, root)
	if code != 0 || !strings.Contains(stdout, `"state": "plan"`) {
		t.Fatalf("plan: exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "LAYOUT")); err != nil {
		t.Fatal("a plan changed the store")
	}
	if code, _, stderr := runStorage([]string{"layout", "concat", "--force"}, root); code != 1 || !strings.Contains(stderr, "unexpected argument") {
		t.Fatalf("concat with an unknown flag: exit=%d stderr=%q", code, stderr)
	}

	code, stdout, stderr = runStorage([]string{"layout", "concat", "--apply"}, root)
	if code != 0 || !strings.Contains(stdout, `"state": "done"`) {
		t.Fatalf("concat: exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "LAYOUT")); !os.IsNotExist(err) {
		t.Fatalf("LAYOUT after concat: %v", err)
	}
	code, stdout, stderr = runStorage([]string{"layout", "show"}, root)
	if code != 0 || !strings.Contains(stdout, `"layout": "v1"`) {
		t.Fatalf("layout after concat: exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	// A store that never had segments: nothing to do, exit 0.
	code, stdout, _ = runStorage([]string{"layout", "concat", "--apply"}, root)
	if code != 0 || !strings.Contains(stdout, `"state": "layout_v1"`) {
		t.Fatalf("concat of a plain store: exit=%d stdout=%s", code, stdout)
	}
	reader, err := taskpersistence.NewJSONLReader(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.GetTask(context.Background(), modulecore.TaskID(taskID)); err != nil {
		t.Fatalf("task after concat: %v", err)
	}
}
