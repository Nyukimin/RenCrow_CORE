package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type actionTTSBridgeStub struct {
	pushErr error
}

func (s *actionTTSBridgeStub) StartSession(context.Context, orchestrator.TTSSessionStart) error {
	return nil
}

func (s *actionTTSBridgeStub) PushText(context.Context, string, string, *moduletts.EmotionState) error {
	return s.pushErr
}

func (s *actionTTSBridgeStub) EndSession(context.Context, string) error {
	return nil
}

func TestActionTTSBridgeCompletesSessionTaskRunActionAttempt(t *testing.T) {
	tasks, actions := newTTSActionTestOwners(t)
	bridge, err := newActionTTSBridge(&actionTTSBridgeStub{}, actions, tasks, "mio")
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-session-1"}); err != nil {
		t.Fatal(err)
	}
	if err := bridge.PushText(context.Background(), "tts-session-1", "こんにちは", nil); err != nil {
		t.Fatal(err)
	}
	if err := bridge.EndSession(context.Background(), "tts-session-1"); err != nil {
		t.Fatal(err)
	}
	assertTTSActionLifecycle(t, tasks, actions, domainaction.StatusSucceeded, domainaction.AttemptStatusSucceeded, domaintask.StatusSucceeded)
}

func TestActionTTSBridgeClosesFailureDuringPush(t *testing.T) {
	tasks, actions := newTTSActionTestOwners(t)
	sentinel := errors.New("TTS gateway unavailable")
	bridge, err := newActionTTSBridge(&actionTTSBridgeStub{pushErr: sentinel}, actions, tasks, "mio")
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.StartSession(context.Background(), orchestrator.TTSSessionStart{SessionID: "tts-session-2"}); err != nil {
		t.Fatal(err)
	}
	if err := bridge.PushText(context.Background(), "tts-session-2", "失敗", nil); !errors.Is(err, sentinel) {
		t.Fatalf("PushText error=%v", err)
	}
	assertTTSActionLifecycle(t, tasks, actions, domainaction.StatusFailed, domainaction.AttemptStatusFailed, domaintask.StatusFailed)
}

func newTTSActionTestOwners(t *testing.T) (*taskmanager.Manager, *actionmanager.Manager) {
	t.Helper()
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
	return taskmanager.New(taskStore, taskmanager.DefaultParallelLimits()), actionmanager.New(actionStore)
}

func assertTTSActionLifecycle(t *testing.T, tasks *taskmanager.Manager, actions *actionmanager.Manager, actionStatus domainaction.Status, attemptStatus domainaction.AttemptStatus, taskStatus domaintask.Status) {
	t.Helper()
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
	if action.Kind != domainaction.KindTTS || action.Status != actionStatus || len(attempts) != 1 || attempts[0].Status != attemptStatus {
		t.Fatalf("TTS action lifecycle action=%#v attempts=%#v", action, attempts)
	}
	if task.Status != taskStatus || run.Status != domaintask.RunStatus(taskStatus) {
		t.Fatalf("TTS Task/Run lifecycle task=%#v run=%#v", task, run)
	}
}
