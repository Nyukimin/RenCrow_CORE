package task

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestJSONLStoreKeepsLatestCanonicalTaskState(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	value := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "test", Route: domaintask.RouteCode, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
	if err := store.SaveTask(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	value.Status = domaintask.StatusRunning
	value.UpdatedAt = now.Add(time.Minute)
	if err := store.SaveTask(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(context.Background(), value.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domaintask.StatusRunning {
		t.Fatalf("status = %s, want running", got.Status)
	}
	if _, err := os.Stat(filepath.Join(root, "task_state.jsonl")); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{"task_context.jsonl", "task_notifications.jsonl"} {
		if _, err := os.Stat(filepath.Join(root, filename)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestJSONLStoreRejectsLegacyFilesAndUnknownAliases(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "job_state.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewJSONLStore(root); err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Fatalf("legacy store result = %v", err)
	}

	root = t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.statePath, []byte(`{"job_id":"legacy"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListTasks(context.Background(), domaintask.Filter{}); err == nil {
		t.Fatal("legacy JSON alias was accepted")
	}
}

func TestJSONLStoreContextAndNotificationUseTaskID(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	value := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "test", Route: domaintask.RouteGeneral, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
	if err := store.SaveTask(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContext(context.Background(), domaintask.SharedRoleContext{TaskID: value.TaskID, CurrentPlan: "plan", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	contextValue, err := store.GetContext(context.Background(), value.TaskID)
	if err != nil || contextValue.CurrentPlan != "plan" {
		t.Fatalf("context = %#v err=%v", contextValue, err)
	}
	notification := domaintask.NewNotification(value, now)
	if err := store.SaveNotification(context.Background(), notification); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListNotifications(context.Background(), 10, false)
	if err != nil || len(items) != 1 || items[0].TaskID != value.TaskID {
		t.Fatalf("notifications = %#v err=%v", items, err)
	}
}

func TestJSONLStoreRunHistoryReloadsByRunIDAndTaskID(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 5, 6, 0, 0, 0, time.UTC)
	task := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "run persistence", Route: domaintask.RouteGeneral, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
	if err := store.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	first := domaintask.Run{RunID: modulecore.NewRunID(), TaskID: task.TaskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now}
	if err := store.SaveRun(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	completedAt := now.Add(time.Minute)
	first.Status = domaintask.RunStatusSucceeded
	first.CompletedAt = &completedAt
	first.Summary = "first complete"
	if err := store.SaveRun(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := domaintask.Run{RunID: modulecore.NewRunID(), TaskID: task.TaskID, StartReason: domaintask.RunStartReasonExplicitRerun, Status: domaintask.RunStatusRunning, StartedAt: now.Add(2 * time.Minute), Assignee: "Mio"}
	if err := store.SaveRun(context.Background(), second); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := reloaded.ListRuns(context.Background(), domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil || len(runs) != 2 {
		t.Fatalf("reloaded runs = %#v err=%v", runs, err)
	}
	if runs[0].RunID != first.RunID || runs[0].Status != domaintask.RunStatusSucceeded || runs[1].RunID != second.RunID {
		t.Fatalf("reloaded chronology = %#v", runs)
	}
	if _, err := os.Stat(filepath.Join(root, "task_run.jsonl")); err != nil {
		t.Fatalf("task_run.jsonl missing: %v", err)
	}
}

func TestJSONLStoreRejectsInvalidRunOwnershipAndUnknownFields(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 5, 7, 0, 0, 0, time.UTC)
	missingTask := domaintask.Run{RunID: modulecore.NewRunID(), TaskID: modulecore.NewTaskID(), StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now}
	if err := store.SaveRun(context.Background(), missingTask); err == nil {
		t.Fatal("run for missing task was accepted")
	}
	missingID := missingTask
	missingID.TaskID = ""
	if err := store.SaveRun(context.Background(), missingID); err == nil {
		t.Fatal("run without task ID was accepted")
	}
	if err := os.WriteFile(store.runPath, []byte(`{"run_id":"run_00000000-0000-7000-8000-000000000000","task_id":"tsk_00000000-0000-7000-8000-000000000000","start_reason":"first","status":"running","started_at":"2026-09-05T07:00:00Z","parent_run_id":"run_00000000-0000-7000-8000-000000000001"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListRuns(context.Background(), domaintask.RunFilter{}); err == nil {
		t.Fatal("unknown run field was accepted")
	}
}

func TestJSONLStoreRejectsRunHistoryCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(domaintask.Run, modulecore.TaskID, time.Time) domaintask.Run
	}{
		{name: "task_id", mutate: func(run domaintask.Run, otherTaskID modulecore.TaskID, _ time.Time) domaintask.Run {
			run.TaskID = otherTaskID
			return run
		}},
		{name: "start_reason", mutate: func(run domaintask.Run, _ modulecore.TaskID, _ time.Time) domaintask.Run {
			run.StartReason = domaintask.RunStartReasonExplicitRerun
			return run
		}},
		{name: "assignee", mutate: func(run domaintask.Run, _ modulecore.TaskID, _ time.Time) domaintask.Run {
			run.Assignee = "Shiro"
			return run
		}},
		{name: "started_at", mutate: func(run domaintask.Run, _ modulecore.TaskID, now time.Time) domaintask.Run {
			run.StartedAt = now.Add(time.Minute)
			return run
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewJSONLStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 9, 5, 11, 0, 0, 0, time.UTC)
			task := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "history", Route: domaintask.RouteGeneral, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
			if err := store.SaveTask(context.Background(), task); err != nil {
				t.Fatal(err)
			}
			otherTaskID := modulecore.NewTaskID()
			base := domaintask.Run{RunID: modulecore.NewRunID(), TaskID: task.TaskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now, Assignee: "Mio"}
			mutated := test.mutate(base, otherTaskID, now)
			if err := os.WriteFile(store.runPath, []byte(mustJSONLRun(t, base)+mustJSONLRun(t, mutated)), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ListRuns(context.Background(), domaintask.RunFilter{}); err == nil {
				t.Fatal("immutable run history rewrite was accepted")
			}
		})
	}

	t.Run("closed_terminal_rewrite", func(t *testing.T) {
		store, err := NewJSONLStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 9, 5, 11, 30, 0, 0, time.UTC)
		task := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "terminal history", Route: domaintask.RouteGeneral, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
		if err := store.SaveTask(context.Background(), task); err != nil {
			t.Fatal(err)
		}
		base := domaintask.Run{RunID: modulecore.NewRunID(), TaskID: task.TaskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now}
		completedAt := now.Add(time.Minute)
		closed := base
		closed.Status = domaintask.RunStatusSucceeded
		closed.CompletedAt = &completedAt
		closed.Summary = "first"
		rewrite := closed
		rewrite.Summary = "rewritten"
		if err := os.WriteFile(store.runPath, []byte(mustJSONLRun(t, base)+mustJSONLRun(t, closed)+mustJSONLRun(t, rewrite)), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ListRuns(context.Background(), domaintask.RunFilter{}); err == nil {
			t.Fatal("closed terminal rewrite was accepted")
		}
	})

	t.Run("two_active_runs", func(t *testing.T) {
		store, err := NewJSONLStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
		task := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "active history", Route: domaintask.RouteGeneral, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
		if err := store.SaveTask(context.Background(), task); err != nil {
			t.Fatal(err)
		}
		first := domaintask.Run{RunID: modulecore.NewRunID(), TaskID: task.TaskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now}
		second := first
		second.RunID = modulecore.NewRunID()
		if err := os.WriteFile(store.runPath, []byte(mustJSONLRun(t, first)+mustJSONLRun(t, second)), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ListRuns(context.Background(), domaintask.RunFilter{}); err == nil {
			t.Fatal("multiple active runs were accepted")
		}
	})
}

func TestJSONLStoreConcurrentFirstActiveRunHasSingleWinner(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 5, 13, 0, 0, 0, time.UTC)
	task := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "concurrent", Route: domaintask.RouteGeneral, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
	if err := store.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	runs := []domaintask.Run{
		{RunID: modulecore.NewRunID(), TaskID: task.TaskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now},
		{RunID: modulecore.NewRunID(), TaskID: task.TaskID, StartReason: domaintask.RunStartReasonExplicitRerun, Status: domaintask.RunStatusRunning, StartedAt: now.Add(time.Second)},
	}
	start := make(chan struct{})
	results := make(chan error, len(runs))
	var group sync.WaitGroup
	for _, run := range runs {
		group.Add(1)
		go func(value domaintask.Run) {
			defer group.Done()
			<-start
			results <- store.SaveRun(context.Background(), value)
		}(run)
	}
	close(start)
	group.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted concurrent active runs = %d, want 1", accepted)
	}
	reloaded, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	items, err := reloaded.ListRuns(context.Background(), domaintask.RunFilter{TaskID: task.TaskID, Status: domaintask.RunStatusRunning})
	if err != nil || len(items) != 1 {
		t.Fatalf("reloaded active runs = %#v err=%v", items, err)
	}
}

func mustJSONLRun(t *testing.T, value domaintask.Run) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded) + "\n"
}
