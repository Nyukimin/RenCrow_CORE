package taskmanager

import (
	"context"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

func TestManagerRecoverRunAfterProcessRestartClosesExactStaleRun(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	firstStore, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	firstManager := New(firstStore, DefaultParallelLimits())
	task, staleRun := newRunningExecutionTaskRun(t, firstManager, "Mio")
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	secondManager := New(secondStore, DefaultParallelLimits())
	recoveredTask, recovered, err := secondManager.RecoverRunAfterProcessRestart(
		ctx,
		task.TaskID,
		staleRun.RunID,
		"Mio",
		domaintask.StatusWaiting,
		"process restarted",
		"retry from durable checkpoint",
	)
	if err != nil {
		t.Fatalf("recover stale Run: %v", err)
	}
	if !recovered {
		t.Fatal("stale Run was not recovered")
	}
	if recoveredTask.TaskID != task.TaskID || recoveredTask.Status != domaintask.StatusWaiting {
		t.Fatalf("recovered Task = %+v, want exact waiting Task", recoveredTask)
	}

	closed, err := secondManager.GetRun(ctx, staleRun.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != domaintask.RunStatusWaiting || closed.WriterGeneration != staleRun.WriterGeneration {
		t.Fatalf("stale Run after recovery = %+v, want waiting with immutable generation", closed)
	}
	currentTask, err := secondManager.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if currentTask.Status != domaintask.StatusWaiting {
		t.Fatalf("Task status = %s, want waiting", currentTask.Status)
	}

	again, recovered, err := secondManager.RecoverRunAfterProcessRestart(
		ctx,
		task.TaskID,
		staleRun.RunID,
		"Mio",
		domaintask.StatusWaiting,
		"process restarted",
		"retry from durable checkpoint",
	)
	if err != nil {
		t.Fatalf("current Run recovery check: %v", err)
	}
	if recovered || again.TaskID != "" {
		t.Fatalf("terminal Run was rewritten: recovered=%t task=%+v", recovered, again)
	}
}
