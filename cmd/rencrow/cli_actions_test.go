package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// seedActionStore writes n Actions, each on a Run of the Task store at taskRoot
// when taskRoot has one, and returns the Action store root.
func seedActionStoreForCLI(t *testing.T, runs []modulecore.RunID, extraUnknownRuns int) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "actions")
	store, err := actionpersistence.NewJSONLStoreWithOptions(root, actionpersistence.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	manager := actionmanager.New(store)
	ctx := context.Background()
	all := append([]modulecore.RunID(nil), runs...)
	for i := 0; i < extraUnknownRuns; i++ {
		all = append(all, modulecore.NewRunID())
	}
	for _, run := range all {
		action, attempt, err := manager.CreateAction(ctx, actionmanager.CreateInput{TaskID: modulecore.NewTaskID(), RunID: run, Kind: domainaction.KindTool, Name: "t"})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := manager.CompleteAttempt(ctx, action.ActionID, attempt.AttemptID, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "ok"); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func seedTaskStoreWithRuns(t *testing.T, n int) (string, []modulecore.RunID) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "tasks")
	store, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	tasks := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	var runs []modulecore.RunID
	for i := 0; i < n; i++ {
		_, run := newRunningTaskRun(t, tasks, "cli action verify", domaintask.RouteGeneral, "mio")
		runs = append(runs, run.RunID)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return root, runs
}

func runActions(t *testing.T, args []string, actionRoot, taskRoot string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runActionsCommand(args, actionRoot, taskRoot, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestActionsVerifyPassesAGoodStoreAndPrintsOnlyCounts(t *testing.T) {
	actionRoot := seedActionStoreForCLI(t, nil, 4)
	code, out, errOut := runActions(t, []string{"verify"}, actionRoot, "")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if report["ok"] != true || report["actions"] != float64(4) || report["attempts"] != float64(4) {
		t.Fatalf("report = %v", report)
	}
	for _, forbidden := range []string{"act_", "att_", "run_", "tsk_"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("the report carries an identifier (%s):\n%s", forbidden, out)
		}
	}
	code, compact, _ := runActions(t, []string{"verify", "--compact"}, actionRoot, "")
	if code != 0 || strings.Count(strings.TrimSpace(compact), "\n") != 0 {
		t.Fatalf("--compact: exit %d, output %q", code, compact)
	}
}

func TestActionsVerifyFailsOnAnInvalidLog(t *testing.T) {
	actionRoot := seedActionStoreForCLI(t, nil, 2)
	f, err := os.OpenFile(filepath.Join(actionRoot, "action_attempt.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{not json}\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	code, out, errOut := runActions(t, []string{"verify"}, actionRoot, "")
	if code == 0 {
		t.Fatalf("an invalid log passed verify:\n%s", out)
	}
	if !strings.Contains(errOut, "verification failed") {
		t.Fatalf("stderr = %q", errOut)
	}
}

func TestActionsVerifyChecksRunsOnlyWhenAsked(t *testing.T) {
	taskRoot, runs := seedTaskStoreWithRuns(t, 2)
	actionRoot := seedActionStoreForCLI(t, runs, 1) // two Actions on known Runs, one on a Run no Task owns

	if code, out, errOut := runActions(t, []string{"verify"}, actionRoot, taskRoot); code != 0 {
		t.Fatalf("without --check-runs: exit %d\n%s\n%s", code, out, errOut)
	}
	code, out, errOut := runActions(t, []string{"verify", "--check-runs"}, actionRoot, taskRoot)
	if code == 0 {
		t.Fatalf("a Run no Task owns passed --check-runs:\n%s", out)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("output is not JSON: %v\n%s\n%s", err, out, errOut)
	}
	if report["runs_checked"] != float64(3) || report["runs_missing"] != float64(1) {
		t.Fatalf("report = %v", report)
	}

	cleanActions := seedActionStoreForCLI(t, runs, 0)
	code, out, errOut = runActions(t, []string{"verify", "--check-runs"}, cleanActions, taskRoot)
	if code != 0 {
		t.Fatalf("every Run is owned, yet exit %d\n%s\n%s", code, out, errOut)
	}
	if code, _, errOut := runActions(t, []string{"verify", "--check-runs"}, cleanActions, ""); code == 0 || !strings.Contains(errOut, "task store") {
		t.Fatalf("--check-runs without a Task store location: exit %d, stderr %q", code, errOut)
	}
}

func TestActionsCommandRejectsWhatItDoesNotKnow(t *testing.T) {
	actionRoot := seedActionStoreForCLI(t, nil, 1)
	for _, args := range [][]string{nil, {"nothing"}, {"verify", "--no-such-flag"}, {"verify", "extra"}} {
		if code, _, errOut := runActions(t, args, actionRoot, ""); code == 0 || errOut == "" {
			t.Errorf("args %v: exit %d, stderr %q; want a usage error", args, code, errOut)
		}
	}
	if code, _, errOut := runActions(t, []string{"verify"}, "", ""); code == 0 || errOut == "" {
		t.Errorf("no store location: exit %d, stderr %q", code, errOut)
	}
}
