package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	memorypromotionapp "github.com/Nyukimin/RenCrow_CORE/internal/application/memorypromotion"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

type memoryPromotionActionRunnerStub struct {
	identityBound bool
	actionBound   bool
}

func (s *memoryPromotionActionRunnerStub) RunOne(ctx context.Context) (memorypromotionapp.RunResult, error) {
	_, identityErr := domainexecution.IdentityFromContext(ctx)
	_, _, s.actionBound = domainexecution.BoundActionAttemptFromContext(ctx)
	s.identityBound = identityErr == nil
	return memorypromotionapp.RunResult{Processed: true, MessageCount: 1, CandidateCount: 1}, nil
}

func TestActionMemoryPromotionRunnerCompletesCanonicalLifecycle(t *testing.T) {
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
	inner := &memoryPromotionActionRunnerStub{}
	runner, err := newActionMemoryPromotionRunner(inner, actions, tasks, "midori")
	if err != nil {
		t.Fatal(err)
	}

	result, err := runner.RunOne(context.Background())
	if err != nil || !result.Processed || !inner.identityBound || !inner.actionBound {
		t.Fatalf("RunOne result=%#v identity_bound=%v action_bound=%v err=%v", result, inner.identityBound, inner.actionBound, err)
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
	if action.Kind != domainaction.KindMemoryPromotion || action.Status != domainaction.StatusSucceeded || len(attempts) != 1 || attempts[0].Status != domainaction.AttemptStatusSucceeded {
		t.Fatalf("Memory Promotion Action lifecycle action=%#v attempts=%#v", action, attempts)
	}
	if task.Status != domaintask.StatusSucceeded || run.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("Memory Promotion Task/Run lifecycle task=%#v run=%#v", task, run)
	}
}
