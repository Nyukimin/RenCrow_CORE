package stt

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

func TestActionProviderCreatesAndCompletesStandaloneSTTExecution(t *testing.T) {
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	tasks := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	actions := actionmanager.New(actionStore)
	provider, err := NewActionProvider(MockProvider{Text: "こんにちは"}, actions, tasks, "mio")
	if err != nil {
		t.Fatal(err)
	}
	wav := make([]byte, 44)
	copy(wav[0:4], "RIFF")
	copy(wav[8:12], "WAVE")

	result, err := provider.Transcribe(context.Background(), wav)
	if err != nil || result.Text != "こんにちは" {
		t.Fatalf("Transcribe result=%#v err=%v", result, err)
	}
	storedActions, err := actions.ListActions(context.Background(), domainaction.Filter{})
	if err != nil || len(storedActions) != 1 {
		t.Fatalf("actions=%#v err=%v", storedActions, err)
	}
	action := storedActions[0]
	attempts, err := actions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: action.ActionID})
	if err != nil {
		t.Fatal(err)
	}
	task, err := tasks.Get(context.Background(), action.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := tasks.GetRun(context.Background(), action.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if action.Kind != domainaction.KindSTT || action.Status != domainaction.StatusSucceeded || len(attempts) != 1 || attempts[0].Status != domainaction.AttemptStatusSucceeded {
		t.Fatalf("STT Action lifecycle action=%#v attempts=%#v", action, attempts)
	}
	if task.Status != domaintask.StatusSucceeded || run.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("STT Task/Run lifecycle task=%#v run=%#v", task, run)
	}
}
