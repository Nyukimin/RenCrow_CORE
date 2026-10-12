package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Failure knowledge: docs/調査/*Run無しqueued_Taskの滞留*_FailureKnowledge.md.
// `rencrow tasks reap-queued` cancels the Run-less queued Tasks that old
// creation paths left behind. These tests drive the command on stores shaped
// like the production store (index and sidecar on).

var (
	reapCLIOld    = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	reapCLICutoff = "2026-10-12T02:39:00Z"
)

type reapCLIWorld struct {
	root string
	// candidates are the Tasks the command must cancel.
	candidates []modulecore.TaskID
	// kept are queued Tasks it must leave alone.
	kept []modulecore.TaskID
}

// seedReapCLIStore writes an indexed store with a sidecar: three candidates and
// five queued Tasks that are not candidates.
func seedReapCLIStore(t *testing.T) reapCLIWorld {
	t.Helper()
	root := filepath.Join(t.TempDir(), "tasks")
	store, err := taskpersistence.NewJSONLStoreWithOptions(root, taskpersistence.OpenOptions{Index: true, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	manager := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	ctx := context.Background()
	world := reapCLIWorld{root: root}
	create := func(task domaintask.Task) modulecore.TaskID {
		t.Helper()
		task.CreatedAt = reapCLIOld
		created, err := manager.Create(ctx, task, domaintask.SharedRoleContext{CurrentPlan: "secret plan text"})
		if err != nil {
			t.Fatal(err)
		}
		return created.TaskID
	}
	world.candidates = []modulecore.TaskID{
		create(domaintask.Task{Title: "IdleChat conversation", Route: domaintask.RouteGeneral, Assignee: "Shiro"}),
		create(domaintask.Task{Title: "Memory Promotion", Route: domaintask.RouteResearch, Assignee: "actor-a", OwnerID: "actor-a"}),
		create(domaintask.Task{Title: "Background job failure: some_job", Route: domaintask.RouteOperations, Assignee: "shiro"}),
	}
	userTask := domaintask.Task{Title: "IdleChat conversation", Route: domaintask.RouteGeneral, Assignee: "Shiro", OriginSessionID: modulecore.NewSessionID()}
	late := domaintask.Task{Title: "IdleChat conversation", Route: domaintask.RouteGeneral, Assignee: "Shiro"}
	hasRun := create(domaintask.Task{Title: "IdleChat conversation", Route: domaintask.RouteGeneral, Assignee: "Shiro"})
	generation, err := store.WriterGeneration()
	if err != nil {
		t.Fatal(err)
	}
	completed := reapCLIOld.Add(time.Minute)
	if err := store.SaveRun(ctx, domaintask.Run{
		WriterGeneration: generation, RunID: modulecore.NewRunID(), TaskID: hasRun, StartReason: domaintask.RunStartReasonFirst,
		Status: domaintask.RunStatusInterrupted, StartedAt: reapCLIOld, CompletedAt: &completed,
	}); err != nil {
		t.Fatal(err)
	}
	world.kept = []modulecore.TaskID{
		create(userTask),
		hasRun,
		create(domaintask.Task{Title: "ordinary work", Route: domaintask.RouteGeneral, Assignee: "Shiro"}),
	}
	late.CreatedAt = time.Date(2026, 10, 12, 5, 0, 0, 0, time.UTC)
	lateCreated, err := manager.Create(ctx, late, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	world.kept = append(world.kept, lateCreated.TaskID)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return world
}

func runReap(args []string, root string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = runTasksReapCommand(args, root, &out, &errOut)
	return code, out.String(), errOut.String()
}

type reapCLIOutput struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Store  struct {
		Writer      bool   `json:"writer"`
		Index       bool   `json:"index"`
		IndexSource string `json:"index_source"`
	} `json:"store"`
	Report *taskmanager.ReapReport `json:"report"`
}

func decodeReapOutput(t *testing.T, stdout string) reapCLIOutput {
	t.Helper()
	var output reapCLIOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, stdout)
	}
	return output
}

func snapshotReapTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		sum := sha256.Sum256(data)
		files[filepath.ToSlash(relative)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func requireSameReapTree(t *testing.T, before, after map[string]string) {
	t.Helper()
	var diffs []string
	for name, digest := range before {
		if got, ok := after[name]; !ok {
			diffs = append(diffs, "removed "+name)
		} else if got != digest {
			diffs = append(diffs, "changed "+name)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			diffs = append(diffs, "created "+name)
		}
	}
	sort.Strings(diffs)
	if len(diffs) > 0 {
		t.Fatalf("the store changed: %s", strings.Join(diffs, ", "))
	}
}

func TestTasksReapSubcommandIsRecognizedBeforeTheManagerIsOpened(t *testing.T) {
	for args, want := range map[string]bool{
		"reap-queued --created-before x": true, "REAP-QUEUED": true, "--compact reap-queued": true,
		"list": false, "verify": false, "cancel tsk_x": false, "": false,
	} {
		if got := isTasksReapSubcommand(strings.Fields(args)); got != want {
			t.Errorf("isTasksReapSubcommand(%q) = %v, want %v", args, got, want)
		}
	}
}

func TestTasksReapRejectsABadCommandLineBeforeOpeningTheStore(t *testing.T) {
	world := seedReapCLIStore(t)
	before := snapshotReapTree(t, world.root)
	for name, args := range map[string][]string{
		"no cutoff":           {"reap-queued"},
		"cutoff without zone": {"reap-queued", "--created-before", "2026-10-12T02:39:00"},
		"cutoff not a time":   {"reap-queued", "--created-before", "yesterday"},
		"negative min age":    {"reap-queued", "--created-before", reapCLICutoff, "--min-age", "-1h"},
		"min age not a time":  {"reap-queued", "--created-before", reapCLICutoff, "--min-age", "soon"},
		"zero batch size":     {"reap-queued", "--created-before", reapCLICutoff, "--batch-size", "0"},
		"huge batch size":     {"reap-queued", "--created-before", reapCLICutoff, "--batch-size", "100000"},
		"unknown flag":        {"reap-queued", "--created-before", reapCLICutoff, "--force"},
		"positional argument": {"reap-queued", "--created-before", reapCLICutoff, "tsk_x"},
	} {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := runReap(args, world.root)
			if code != 2 || strings.TrimSpace(stdout) != "" || strings.TrimSpace(stderr) == "" {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want exit 2 with a usage message on stderr only", code, stdout, stderr)
			}
		})
	}
	requireSameReapTree(t, before, snapshotReapTree(t, world.root))
}

func TestTasksReapRefusesWithoutAWorkspace(t *testing.T) {
	code, stdout, stderr := runReap([]string{"reap-queued", "--created-before", reapCLICutoff}, "")
	if code != 1 || !strings.Contains(stderr, "workspace_dir is not configured") || decodeReapOutput(t, stdout).Status != "failed" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestTasksReapDryRunReportsCountsAndChangesNothing(t *testing.T) {
	world := seedReapCLIStore(t)
	before := snapshotReapTree(t, world.root)
	code, stdout, stderr := runReap([]string{"reap-queued", "--created-before", reapCLICutoff}, world.root)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr, stdout)
	}
	output := decodeReapOutput(t, stdout)
	report := output.Report
	if output.Status != "ok" || report == nil || report.Mode != "dry_run" || output.Store.Writer {
		t.Fatalf("output = %s", stdout)
	}
	if report.Candidates != 3 || report.CandidatesByKind[taskmanager.ReapKindIdleChatConversation] != 1 ||
		report.CandidatesByKind[taskmanager.ReapKindMemoryPromotion] != 1 || report.CandidatesByKind[taskmanager.ReapKindBackgroundJobFailure] != 1 {
		t.Fatalf("candidates = %d by kind %v", report.Candidates, report.CandidatesByKind)
	}
	for reason, want := range map[taskmanager.ReapExclusion]int{
		taskmanager.ExcludedUserOrigin: 1, taskmanager.ExcludedHasRun: 1, taskmanager.ExcludedKindNotAllowlisted: 1, taskmanager.ExcludedCreatedAfterCutoff: 1,
	} {
		if report.ExcludedByReason[reason] != want {
			t.Errorf("excluded %s = %d, want %d", reason, report.ExcludedByReason[reason], want)
		}
	}
	if !output.Store.Index || output.Store.IndexSource == "" {
		t.Errorf("the dry-run must read through the index and say where it came from: %+v", output.Store)
	}
	requireSameReapTree(t, before, snapshotReapTree(t, world.root))
}

func TestTasksReapOutputCarriesNoRecordContent(t *testing.T) {
	world := seedReapCLIStore(t)
	for _, args := range [][]string{
		{"reap-queued", "--created-before", reapCLICutoff},
		{"reap-queued", "--created-before", reapCLICutoff, "--apply"},
	} {
		_, stdout, stderr := runReap(args, world.root)
		for _, secret := range []string{"IdleChat", "Memory Promotion", "Background job failure", "some_job", "ordinary work", "secret plan text", "actor-a"} {
			if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
				t.Fatalf("%v printed record content %q:\n%s\n%s", args, secret, stdout, stderr)
			}
		}
		for _, taskID := range append(append([]modulecore.TaskID{}, world.candidates...), world.kept...) {
			if strings.Contains(stdout, string(taskID)) {
				t.Fatalf("%v printed a Task ID", args)
			}
		}
	}
}

func TestTasksReapApplyCancelsTheCandidatesAndVerifyStaysClean(t *testing.T) {
	world := seedReapCLIStore(t)
	code, stdout, stderr := runReap([]string{"reap-queued", "--created-before", reapCLICutoff, "--apply", "--batch-size", "2"}, world.root)
	if code != 0 {
		t.Fatalf("apply exit=%d stderr=%s stdout=%s", code, stderr, stdout)
	}
	output := decodeReapOutput(t, stdout)
	report := output.Report
	if output.Status != "ok" || report.Mode != "apply" || !output.Store.Writer || !output.Store.Index || report.Applied != 3 || len(report.Batches) != 2 {
		t.Fatalf("output = %s", stdout)
	}
	for _, batch := range report.Batches {
		if !strings.HasPrefix(batch.OperationID, "reap-queued-") || batch.Cancelled != batch.Tasks {
			t.Fatalf("batch = %+v", batch)
		}
	}

	reader, err := taskpersistence.NewIndexedJSONLReader(world.root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, taskID := range world.candidates {
		task, err := reader.GetTask(context.Background(), taskID)
		if err != nil || task.Status != domaintask.StatusCancelled || !strings.HasPrefix(task.Summary, "reaped: stale queued without run (") {
			t.Fatalf("candidate %s: %+v, %v", taskID, task, err)
		}
	}
	for _, taskID := range world.kept {
		task, err := reader.GetTask(context.Background(), taskID)
		if err != nil || task.Status != domaintask.StatusQueued {
			t.Fatalf("kept Task %s: %+v, %v", taskID, task, err)
		}
	}
	notifications, err := reader.ListNotifications(context.Background(), 0, false)
	if err != nil || len(notifications) != 0 {
		t.Fatalf("notifications = %d, %v: cancelling must not notify", len(notifications), err)
	}

	// The index is derived data. After a write through the tool, `tasks verify`
	// agrees with the log: the old sidecar is a prefix and the tail replays.
	code, verifyOut, verifyErr := runStorage([]string{"verify"}, world.root)
	if code != 0 {
		t.Fatalf("verify exit=%d stderr=%s stdout=%s", code, verifyErr, verifyOut)
	}
	var verify map[string]any
	if err := json.Unmarshal([]byte(verifyOut), &verify); err != nil {
		t.Fatal(err)
	}
	if verify["ok"] != true || verify["mismatches"].(float64) != 0 || verify["sidecar"] != "matches_log" {
		t.Fatalf("verify = %v", verify)
	}
}

func TestTasksReapSecondApplyFindsNothingAndSaysSo(t *testing.T) {
	world := seedReapCLIStore(t)
	args := []string{"reap-queued", "--created-before", reapCLICutoff, "--apply"}
	if code, stdout, stderr := runReap(args, world.root); code != 0 {
		t.Fatalf("first apply exit=%d stderr=%s stdout=%s", code, stderr, stdout)
	}
	state := mustReadFile(t, filepath.Join(world.root, "task_state.jsonl"))
	code, stdout, stderr := runReap(args, world.root)
	output := decodeReapOutput(t, stdout)
	if code != 3 || output.Status != "no_candidates" || output.Report == nil || output.Report.Candidates != 0 || output.Report.Applied != 0 || len(output.Report.Batches) != 0 {
		t.Fatalf("second apply exit=%d stderr=%s stdout=%s", code, stderr, stdout)
	}
	if !bytes.Equal(state, mustReadFile(t, filepath.Join(world.root, "task_state.jsonl"))) {
		t.Fatal("the second apply appended to the log")
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestTasksReapDryRunAndApplyWorkOnTheSameCandidateSet(t *testing.T) {
	world := seedReapCLIStore(t)
	_, dryOut, _ := runReap([]string{"reap-queued", "--created-before", reapCLICutoff}, world.root)
	_, applyOut, _ := runReap([]string{"reap-queued", "--created-before", reapCLICutoff, "--apply"}, world.root)
	dry, apply := decodeReapOutput(t, dryOut).Report, decodeReapOutput(t, applyOut).Report
	if dry == nil || apply == nil || dry.CandidateSetSHA256 == "" || dry.CandidateSetSHA256 != apply.CandidateSetSHA256 {
		t.Fatalf("dry-run set %v, apply set %v", dry, apply)
	}
}

func TestTasksReapApplyStopsWhileCoreHoldsTheWriterLock(t *testing.T) {
	world := seedReapCLIStore(t)
	writer, err := taskpersistence.NewJSONLStoreWithOptions(world.root, taskpersistence.OpenOptions{Index: true, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	logs := []string{"task_state.jsonl", "task_run.jsonl", "task_context.jsonl", "task_notifications.jsonl", "task_operation_receipts.jsonl"}
	before := snapshotReapTree(t, world.root)
	code, stdout, stderr := runReap([]string{"reap-queued", "--created-before", reapCLICutoff, "--apply"}, world.root)
	output := decodeReapOutput(t, stdout)
	if code != 1 || output.Status != "failed" || output.Error != "writer_busy" || !strings.Contains(stderr, "stop the running CORE") {
		t.Fatalf("exit=%d stdout=%s stderr=%q, want the busy failure", code, stdout, stderr)
	}
	after := snapshotReapTree(t, world.root)
	for _, name := range logs {
		if before[name] != after[name] {
			t.Errorf("%s changed although the writer lock was held elsewhere", name)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	// With the lock released the same command works.
	if code, stdout, stderr := runReap([]string{"reap-queued", "--created-before", reapCLICutoff, "--apply"}, world.root); code != 0 {
		t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr, stdout)
	}
}

func TestTasksReapFailsClearlyWhenTheStoreIsMissing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent", "tasks")
	code, stdout, _ := runReap([]string{"reap-queued", "--created-before", reapCLICutoff}, root)
	if code != 1 || decodeReapOutput(t, stdout).Status != "failed" {
		t.Fatalf("exit=%d stdout=%s", code, stdout)
	}
	if _, err := os.Stat(root); err == nil {
		t.Fatal("a dry-run must not create the store")
	}
}

func TestTasksReapCompactOutputIsOneLine(t *testing.T) {
	world := seedReapCLIStore(t)
	code, stdout, _ := runReap([]string{"reap-queued", "--compact", "--created-before", reapCLICutoff}, world.root)
	if code != 0 || strings.Count(strings.TrimSpace(stdout), "\n") != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
}
