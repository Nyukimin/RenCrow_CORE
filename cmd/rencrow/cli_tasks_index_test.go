package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var cliIndexNow = time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)

// seedIndexedTaskStore writes a small store with the index sidecar enabled and
// returns its root and the ID of a Task that has several stored lines.
func seedIndexedTaskStore(t *testing.T, tasks int) (string, modulecore.TaskID) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "tasks")
	store, err := taskpersistence.NewJSONLStoreWithOptions(root, taskpersistence.OpenOptions{Index: true, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	generation, err := store.WriterGeneration()
	if err != nil {
		t.Fatal(err)
	}
	var first modulecore.TaskID
	for n := 0; n < tasks; n++ {
		now := cliIndexNow.Add(time.Duration(n) * time.Second)
		task := domaintask.Task{
			TaskID: modulecore.NewTaskID(), Title: "cli index task", Route: domaintask.RouteGeneral, Status: domaintask.StatusQueued,
			Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now,
		}
		if n == 0 {
			first = task.TaskID
		}
		if err := store.SaveTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		run := domaintask.Run{
			WriterGeneration: generation, RunID: modulecore.NewRunID(), TaskID: task.TaskID,
			StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now,
		}
		if err := store.SaveRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		task.Status, task.UpdatedAt = domaintask.StatusRunning, now.Add(time.Minute)
		if err := store.SaveTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveNotification(ctx, domaintask.NewNotification(task, now)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return root, first
}

func runStorage(args []string, root string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = runTaskStorageCommand(args, root, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestStorageSubcommandsAreRecognizedBeforeTheManagerIsOpened(t *testing.T) {
	for args, want := range map[string]bool{
		"verify": true, "rebuild-index": true, "history tsk_x": true, "VERIFY --compact": true, "--compact history x": true,
		"list": false, "show tsk_x": false, "status": false, "": false, "create --title x": false,
	} {
		if got := isTaskStorageSubcommand(strings.Fields(args)); got != want {
			t.Errorf("isTaskStorageSubcommand(%q) = %v, want %v", args, got, want)
		}
	}
}

func TestTasksVerifyPassesOnAHealthyStoreAndPrintsABoundedReport(t *testing.T) {
	root, _ := seedIndexedTaskStore(t, 12)
	code, stdout, stderr := runStorage([]string{"verify"}, root)
	if code != 0 {
		t.Fatalf("verify exit = %d stderr=%s stdout=%s", code, stderr, stdout)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("verify output is not JSON: %v\n%s", err, stdout)
	}
	if report["ok"] != true || report["mismatches"].(float64) != 0 || report["sidecar"] != "matches_log" || report["tasks"].(float64) != 12 {
		t.Fatalf("report = %v", report)
	}
	if strings.Contains(stdout, "cli index task") {
		t.Fatal("verify printed record content")
	}
}

func TestTasksVerifyFailsWhenALogLineChangedBehindTheSidecar(t *testing.T) {
	root, _ := seedIndexedTaskStore(t, 8)
	path := filepath.Join(root, "task_state.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Replace(data, []byte("cli index task"), []byte("cli index tusk"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runStorage([]string{"verify", "--compact"}, root)
	if code != 1 || !strings.Contains(stderr, "mismatches") {
		t.Fatalf("verify exit = %d stderr=%q, want 1 and a mismatch count", code, stderr)
	}
	if strings.Count(strings.TrimSpace(stdout), "\n") != 0 || !strings.Contains(stdout, `"ok":false`) {
		t.Fatalf("--compact output = %q", stdout)
	}
}

func TestTasksRebuildIndexReplacesADamagedSidecar(t *testing.T) {
	root, _ := seedIndexedTaskStore(t, 8)
	sidecar := filepath.Join(root, "index", "seg-legacy.tsi")
	data, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0x08
	if err := os.WriteFile(sidecar, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runStorage([]string{"verify"}, root); code != 0 || stderr != "" {
		t.Fatalf("a damaged sidecar alone must not fail verify: exit=%d stderr=%s", code, stderr)
	}
	code, stdout, stderr := runStorage([]string{"rebuild-index"}, root)
	if code != 0 {
		t.Fatalf("rebuild-index exit = %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"previous_sidecar": "unusable: corrupt"`) {
		t.Fatalf("report = %s", stdout)
	}
	_, stdout, _ = runStorage([]string{"verify"}, root)
	if !strings.Contains(stdout, `"sidecar": "matches_log"`) {
		t.Fatalf("sidecar is not valid after the rebuild: %s", stdout)
	}
}

func TestTasksRebuildIndexRefusesWhileCoreHoldsTheWriterLock(t *testing.T) {
	root, _ := seedIndexedTaskStore(t, 2)
	writer, err := taskpersistence.NewJSONLStoreWithOptions(root, taskpersistence.OpenOptions{Index: true, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	code, _, stderr := runStorage([]string{"rebuild-index"}, root)
	if code != 1 || !strings.Contains(stderr, "writer is busy") || !strings.Contains(stderr, "stop the running CORE") {
		t.Fatalf("exit=%d stderr=%q, want the busy error", code, stderr)
	}
}

func TestTasksHistoryPrintsEveryStoredLineOfOneTask(t *testing.T) {
	root, taskID := seedIndexedTaskStore(t, 4)
	code, stdout, stderr := runStorage([]string{"history", string(taskID)}, root)
	if code != 0 {
		t.Fatalf("history exit = %d stderr=%s", code, stderr)
	}
	var output struct {
		TaskID string `json:"task_id"`
		Lines  []struct {
			File   string          `json:"file"`
			Offset int64           `json:"offset"`
			Line   json.RawMessage `json:"line"`
		} `json:"lines"`
	}
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("history output is not JSON: %v", err)
	}
	counts := map[string]int{}
	for _, line := range output.Lines {
		counts[line.File]++
		if !bytes.Contains(line.Line, []byte(taskID)) {
			t.Fatalf("a history line of %s does not mention the task", line.File)
		}
	}
	if output.TaskID != string(taskID) || counts["task_state.jsonl"] != 2 || counts["task_run.jsonl"] != 1 || counts["task_notifications.jsonl"] != 1 {
		t.Fatalf("history of %s = %v", taskID, counts)
	}
}

func TestTasksHistoryAndStorageCommandsFailClearly(t *testing.T) {
	root, _ := seedIndexedTaskStore(t, 2)
	for name, tc := range map[string]struct {
		args []string
		root string
		want string
	}{
		"no task id":        {[]string{"history"}, root, "usage: rencrow tasks history"},
		"malformed task id": {[]string{"history", "job_old"}, root, "invalid task_id"},
		"unknown task":      {[]string{"history", string(modulecore.NewTaskID())}, root, "was not found"},
		"no workspace":      {[]string{"verify"}, "", "workspace_dir is not configured"},
		"no store":          {[]string{"verify"}, filepath.Join(t.TempDir(), "absent"), "failed to verify"},
	} {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := runStorage(tc.args, tc.root)
			if code != 1 || !strings.Contains(stderr, tc.want) || strings.TrimSpace(stdout) != "" {
				t.Fatalf("exit=%d stderr=%q stdout=%q, want exit 1 with %q", code, stderr, stdout, tc.want)
			}
		})
	}
}
