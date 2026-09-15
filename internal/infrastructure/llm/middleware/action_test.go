package middleware

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	transportpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type actionTestProvider struct {
	err          error
	actionID     modulecore.ActionID
	attemptID    modulecore.AttemptID
	receiptBound bool
}

func (p *actionTestProvider) Name() string { return "test-provider" }

func (p *actionTestProvider) Generate(ctx context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.actionID, p.attemptID, _ = domainexecution.BoundActionAttemptFromContext(ctx)
	_, p.receiptBound = domaintransport.ReceiptOwnerFromContext(ctx)
	return llm.GenerateResponse{Content: "ok"}, p.err
}

func (p *actionTestProvider) Chat(ctx context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	p.actionID, p.attemptID, _ = domainexecution.BoundActionAttemptFromContext(ctx)
	return llm.ChatResponse{Done: true}, p.err
}

func TestActionProviderCompletesGenerateAndPreservesToolCapability(t *testing.T) {
	actions := newLLMActionTestManager(t)
	inner := &actionTestProvider{}
	wrapped, err := WithActionOwner(inner, actions)
	if err != nil {
		t.Fatal(err)
	}
	toolWrapped, ok := wrapped.(llm.ToolCallingProvider)
	if !ok {
		t.Fatal("tool-calling capability was lost")
	}
	ctx, taskID, runID := llmActionTestContext(t)

	if _, err := wrapped.Generate(ctx, llm.GenerateRequest{}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := toolWrapped.Chat(ctx, llm.ChatRequest{}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	stored, err := actions.ListActions(context.Background(), domainaction.Filter{TaskID: taskID, RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 {
		t.Fatalf("actions=%#v, want Generate and Chat", stored)
	}
	for _, action := range stored {
		if action.Kind != domainaction.KindLLM || action.Status != domainaction.StatusSucceeded {
			t.Fatalf("unexpected LLM action: %#v", action)
		}
		attempts, err := actions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: action.ActionID})
		if err != nil {
			t.Fatal(err)
		}
		if len(attempts) != 1 || attempts[0].Status != domainaction.AttemptStatusSucceeded {
			t.Fatalf("attempts=%#v for action=%#v", attempts, action)
		}
	}
}

func TestActionProviderClosesTimedOutAttemptAfterCancelledContext(t *testing.T) {
	actions := newLLMActionTestManager(t)
	inner := &actionTestProvider{err: context.DeadlineExceeded}
	wrapped, err := WithActionOwner(inner, actions)
	if err != nil {
		t.Fatal(err)
	}
	ctx, taskID, runID := llmActionTestContext(t)

	if _, err := wrapped.Generate(ctx, llm.GenerateRequest{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Generate error=%v", err)
	}
	stored, err := actions.ListActions(context.Background(), domainaction.Filter{TaskID: taskID, RunID: runID})
	if err != nil || len(stored) != 1 {
		t.Fatalf("actions=%#v err=%v", stored, err)
	}
	attempts, err := actions.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: stored[0].ActionID})
	if err != nil {
		t.Fatal(err)
	}
	if stored[0].Status != domainaction.StatusFailed || len(attempts) != 1 || attempts[0].Status != domainaction.AttemptStatusTimedOut {
		t.Fatalf("timeout lifecycle action=%#v attempts=%#v", stored[0], attempts)
	}
}

func TestActionProviderCreatesTaskRunForBackgroundLLMCall(t *testing.T) {
	actions := newLLMActionTestManager(t)
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	tasks := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	transportStore, err := transportpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "transport"))
	if err != nil {
		t.Fatal(err)
	}
	transport := transportmanager.New(transportStore, actions)
	inner := &actionTestProvider{}
	wrapped, err := WithActionExecutionOwners(inner, actions, tasks, transport, "mio")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := wrapped.Generate(context.Background(), llm.GenerateRequest{}); err != nil {
		t.Fatal(err)
	}
	if !inner.receiptBound {
		t.Fatal("LLM transport receipt owner was not bound to provider context")
	}
	stored, err := actions.ListActions(context.Background(), domainaction.Filter{})
	if err != nil || len(stored) != 1 {
		t.Fatalf("actions=%#v err=%v", stored, err)
	}
	task, err := tasks.Get(context.Background(), stored[0].TaskID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := tasks.GetRun(context.Background(), stored[0].RunID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domaintask.StatusSucceeded || run.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("background LLM Task/Run task=%#v run=%#v", task, run)
	}
}

func newLLMActionTestManager(t *testing.T) *actionmanager.Manager {
	t.Helper()
	store, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	return actionmanager.New(store)
}

func llmActionTestContext(t *testing.T) (context.Context, modulecore.TaskID, modulecore.RunID) {
	t.Helper()
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	ctx, err := domainexecution.WithIdentity(context.Background(), taskID, runID, modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	return ctx, taskID, runID
}
