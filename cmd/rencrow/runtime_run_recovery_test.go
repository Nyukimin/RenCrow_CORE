package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

// restartedTaskRuntime は Task ストアを閉じて同じルートを再オープンし、再起動後の
// プロセスが新しい writer generation を保持した状態を作るテスト用足場である。
// Step15 の本番実測で、Action を持たない active Run だけが残り OPS スロットを
// 恒久的に占有する症状を再現する。
type restartedTaskRuntime struct {
	taskRoot   string
	first      *taskpersistence.JSONLStore
	firstTasks *taskmanager.Manager
	actions    *actionmanager.Manager
	closed     bool
}

func newRestartedTaskRuntime(t *testing.T) *restartedTaskRuntime {
	t.Helper()
	runtime := &restartedTaskRuntime{taskRoot: filepath.Join(t.TempDir(), "tasks")}

	store, err := taskpersistence.NewJSONLStore(runtime.taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	runtime.first = store
	runtime.firstTasks = taskmanager.New(store, taskmanager.DefaultParallelLimits())

	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	runtime.actions = actionmanager.New(actionStore)

	t.Cleanup(func() {
		if !runtime.closed {
			if err := runtime.first.Close(); err != nil {
				t.Logf("close first Task store: %v", err)
			}
		}
	})
	return runtime
}

// restart 前のストアを閉じ、同じルートを新的 writer generation で開き直す。
func (r *restartedTaskRuntime) restart(t *testing.T) *taskmanager.Manager {
	t.Helper()
	if err := r.first.Close(); err != nil {
		t.Fatal(err)
	}
	r.closed = true

	secondStore, err := taskpersistence.NewJSONLStore(r.taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := secondStore.Close(); err != nil {
			t.Logf("close restarted Task store: %v", err)
		}
	})
	return taskmanager.New(secondStore, taskmanager.DefaultParallelLimits())
}

func newRunningTaskRun(t *testing.T, tasks *taskmanager.Manager, title string, route domaintask.Route, assignee string) (domaintask.Task, domaintask.Run) {
	t.Helper()
	task, err := tasks.Create(context.Background(), domaintask.Task{
		Title:    title,
		Route:    route,
		OwnerID:  assignee,
		Assignee: assignee,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := tasks.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	return task, run
}

// TestRecoverOrphanTaskRunsAfterRestartClosesStaleOpsRunAndFreesLimit は
// Action を持たない OPS Run が再起動後も running のまま残り、Atlas 起動処理が
// 「operations task limit reached」で弾かれる本番症状を回帰テスト化する。
func TestRecoverOrphanTaskRunsAfterRestartClosesStaleOpsRunAndFreesLimit(t *testing.T) {
	ctx := context.Background()
	runtime := newRestartedTaskRuntime(t)
	staleTask, staleRun := newRunningTaskRun(t, runtime.firstTasks, "Heartbeat worker", domaintask.RouteOperations, "shiro")

	restartedTasks := runtime.restart(t)

	recovered, err := recoverOrphanTaskRunsAfterRestart(ctx, runtime.actions, restartedTasks)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("recovered=%d, want 1", recovered)
	}

	task, err := restartedTasks.Get(ctx, staleTask.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domaintask.StatusFailed {
		t.Fatalf("stale Task status=%s, want failed", task.Status)
	}
	run, err := restartedTasks.GetRun(ctx, staleRun.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domaintask.RunStatusFailed {
		t.Fatalf("stale Run status=%s, want failed", run.Status)
	}

	candidate, err := restartedTasks.Create(ctx, domaintask.Task{
		Title:    "Atlas lifecycle recovery",
		Route:    domaintask.RouteOperations,
		OwnerID:  "shiro",
		Assignee: "shiro",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	allowed, reason, err := restartedTasks.CanStart(ctx, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatalf("OPS Task is still limited after restart recovery: %s", reason)
	}

	again, err := recoverOrphanTaskRunsAfterRestart(ctx, runtime.actions, restartedTasks)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("recovered=%d on the second pass, want 0", again)
	}
}

func TestRecoverOrphanTaskRunsAfterRestartLeavesCurrentGenerationRun(t *testing.T) {
	ctx := context.Background()
	runtime := newRestartedTaskRuntime(t)
	_, staleRun := newRunningTaskRun(t, runtime.firstTasks, "stale worker", domaintask.RouteGeneral, "mio")

	restartedTasks := runtime.restart(t)
	currentTask, currentRun := newRunningTaskRun(t, restartedTasks, "current worker", domaintask.RouteGeneral, "mio")

	recovered, err := recoverOrphanTaskRunsAfterRestart(ctx, runtime.actions, restartedTasks)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("recovered=%d, want 1", recovered)
	}

	stale, err := restartedTasks.GetRun(ctx, staleRun.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Status != domaintask.RunStatusFailed {
		t.Fatalf("stale Run status=%s, want failed", stale.Status)
	}
	current, err := restartedTasks.GetRun(ctx, currentRun.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != domaintask.RunStatusRunning {
		t.Fatalf("current-generation Run status=%s, want running", current.Status)
	}
	currentTaskRecord, err := restartedTasks.Get(ctx, currentTask.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if currentTaskRecord.Status != domaintask.StatusRunning {
		t.Fatalf("current-generation Task status=%s, want running", currentTaskRecord.Status)
	}
}

func TestRecoverOrphanTaskRunsAfterRestartSkipsActionOwnedRun(t *testing.T) {
	ctx := context.Background()
	runtime := newRestartedTaskRuntime(t)
	task, run := newRunningTaskRun(t, runtime.firstTasks, "action owned", domaintask.RouteGeneral, "mio")
	if _, _, err := runtime.actions.CreateAction(ctx, actionmanager.CreateInput{
		TaskID: task.TaskID,
		RunID:  run.RunID,
		Kind:   domainaction.KindLLM,
		Name:   "llm.worker.generate",
	}); err != nil {
		t.Fatal(err)
	}

	restartedTasks := runtime.restart(t)

	recovered, err := recoverOrphanTaskRunsAfterRestart(ctx, runtime.actions, restartedTasks)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 0 {
		t.Fatalf("recovered=%d, want 0 because the Run is owned by an Action", recovered)
	}
	owned, err := restartedTasks.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if owned.Status != domaintask.RunStatusRunning {
		t.Fatalf("Action-owned Run status=%s, want running", owned.Status)
	}
}
