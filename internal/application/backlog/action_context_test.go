package backlog

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func backlogActionContext(t *testing.T) context.Context {
	t.Helper()
	ctx, err := domainexecution.WithBoundActionAttempt(context.Background(), modulecore.NewActionID(), modulecore.NewAttemptID())
	if err != nil {
		t.Fatalf("bind test Action/Attempt: %v", err)
	}
	return ctx
}

func TestBeginActionCreatesAndCompletesCanonicalTaskRunActionAttempt(t *testing.T) {
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatalf("create Task store: %v", err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatalf("create Action store: %v", err)
	}
	tasks := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	actions := actionmanager.New(actionStore)
	service := NewService(nil, nil).WithExecutionOwners(actions, tasks, "shiro")

	ctx, finish, err := service.beginAction(context.Background(), "atlas_recover")
	if err != nil {
		t.Fatalf("begin Action: %v", err)
	}
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		t.Fatalf("execution identity: %v", err)
	}
	actionID, attemptID, ok := domainexecution.BoundActionAttemptFromContext(ctx)
	if !ok {
		t.Fatal("Action/Attempt pair is not bound")
	}
	if err := finish(nil); err != nil {
		t.Fatalf("finish Action: %v", err)
	}

	task, err := tasks.Get(context.Background(), identity.TaskID)
	if err != nil {
		t.Fatalf("get Task: %v", err)
	}
	run, err := tasks.GetRun(context.Background(), identity.RunID)
	if err != nil {
		t.Fatalf("get Run: %v", err)
	}
	action, err := actions.GetAction(context.Background(), actionID)
	if err != nil {
		t.Fatalf("get Action: %v", err)
	}
	attempts, err := actions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: actionID})
	if err != nil {
		t.Fatalf("list Attempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].AttemptID != attemptID {
		t.Fatalf("Attempts=%#v, want exact bound Attempt", attempts)
	}
	attempt := attempts[0]
	if task.Status != domaintask.StatusSucceeded || run.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("Task/Run not terminal: task=%s run=%s", task.Status, run.Status)
	}
	if action.TaskID != task.TaskID || action.RunID != run.RunID || action.Kind != domainaction.KindVerification || action.Status != domainaction.StatusSucceeded {
		t.Fatalf("Action identity/status mismatch: %#v", action)
	}
	if attempt.ActionID != action.ActionID || attempt.Status != domainaction.AttemptStatusSucceeded {
		t.Fatalf("Attempt identity/status mismatch: %#v", attempt)
	}
}
