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

func TestRecoverActionRunsAfterRestartClosesExactStaleRun(t *testing.T) {
	taskRoot := filepath.Join(t.TempDir(), "tasks")
	firstStore, err := taskpersistence.NewJSONLStore(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	firstTasks := taskmanager.New(firstStore, taskmanager.DefaultParallelLimits())
	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	actions := actionmanager.New(actionStore)
	task, err := firstTasks.Create(context.Background(), domaintask.Task{
		Title: "restart recovery", Route: domaintask.RouteGeneral, OwnerID: "mio", Assignee: "mio", ReadOnly: true,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := firstTasks.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	action, attempt, err := actions.CreateAction(context.Background(), actionmanager.CreateInput{
		TaskID: task.TaskID, RunID: run.RunID, Kind: domainaction.KindLLM, Name: "llm.worker.generate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := actions.CompleteAttempt(context.Background(), action.ActionID, attempt.AttemptID, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "completed"); err != nil {
		t.Fatal(err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}
	secondStore, err := taskpersistence.NewJSONLStore(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := secondStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	secondTasks := taskmanager.New(secondStore, taskmanager.DefaultParallelLimits())

	recovered, err := recoverActionRunsAfterRestart(context.Background(), actions, secondTasks)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("recovered=%d, want 1", recovered)
	}
	recoveredTask, err := secondTasks.Get(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	recoveredRun, err := secondTasks.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredTask.Status != domaintask.StatusSucceeded || recoveredRun.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("recovered task=%#v run=%#v", recoveredTask, recoveredRun)
	}
}

func TestRecoverActionRunsAfterRestartCancelsOpenStaleAction(t *testing.T) {
	taskRoot := filepath.Join(t.TempDir(), "tasks")
	firstStore, err := taskpersistence.NewJSONLStore(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	firstTasks := taskmanager.New(firstStore, taskmanager.DefaultParallelLimits())
	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	actions := actionmanager.New(actionStore)
	task, err := firstTasks.Create(context.Background(), domaintask.Task{
		Title: "interrupted action", Route: domaintask.RouteGeneral, OwnerID: "mio", Assignee: "mio", ReadOnly: true,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := firstTasks.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	action, _, err := actions.CreateAction(context.Background(), actionmanager.CreateInput{
		TaskID: task.TaskID, RunID: run.RunID, Kind: domainaction.KindLLM, Name: "llm.worker.generate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}
	secondStore, err := taskpersistence.NewJSONLStore(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := secondStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	secondTasks := taskmanager.New(secondStore, taskmanager.DefaultParallelLimits())

	recovered, err := recoverActionRunsAfterRestart(context.Background(), actions, secondTasks)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("recovered=%d, want 1", recovered)
	}
	recoveredAction, err := actions.GetAction(context.Background(), action.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	recoveredTask, err := secondTasks.Get(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredAction.Status != domainaction.StatusCancelled || recoveredTask.Status != domaintask.StatusCancelled {
		t.Fatalf("recovered Action=%#v Task=%#v", recoveredAction, recoveredTask)
	}
}
