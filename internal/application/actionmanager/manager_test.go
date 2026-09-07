package actionmanager_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	actionstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestCreateActionRetryPreservesActionID(t *testing.T) {
	t.Parallel()
	store, err := actionstore.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	manager := actionmanager.New(store)
	ctx := context.Background()
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()

	action, first, err := manager.CreateAction(ctx, actionmanager.CreateInput{
		TaskID: taskID,
		RunID:  runID,
		Kind:   domainaction.KindTool,
		Name:   "browser.click",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := action.ActionID.Validate(); err != nil {
		t.Fatalf("action id: %v", err)
	}
	if first.StartReason != domainaction.AttemptStartReasonFirst {
		t.Fatalf("first reason: %s", first.StartReason)
	}

	retried, second, err := manager.StartAttempt(ctx, action.ActionID, domainaction.AttemptStartReasonRetry)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if retried.ActionID != action.ActionID {
		t.Fatalf("ActionID changed on retry: %s -> %s", action.ActionID, retried.ActionID)
	}
	if second.AttemptID == first.AttemptID {
		t.Fatal("AttemptID must change on retry")
	}
	if second.StartReason != domainaction.AttemptStartReasonRetry {
		t.Fatalf("retry reason: %s", second.StartReason)
	}

	attempts, err := manager.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: action.ActionID})
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("want 2 attempts, got %d", len(attempts))
	}
	closed, ok := attempts[0], attempts[0].Status == domainaction.AttemptStatusFailed
	if !ok && attempts[0].AttemptID == first.AttemptID {
		t.Fatalf("first attempt not closed: %+v", closed)
	}
	for _, item := range attempts {
		if item.AttemptID == first.AttemptID && item.Status != domainaction.AttemptStatusFailed {
			t.Fatalf("first attempt status: %s", item.Status)
		}
		if item.AttemptID == second.AttemptID && item.Status != domainaction.AttemptStatusRunning {
			t.Fatalf("second attempt status: %s", item.Status)
		}
	}

	completed, finalAttempt, err := manager.CompleteAttempt(ctx, action.ActionID, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded, "done")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if completed.Status != domainaction.StatusSucceeded || finalAttempt.Status != domainaction.AttemptStatusSucceeded {
		t.Fatalf("complete result action=%s attempt=%s", completed.Status, finalAttempt.Status)
	}
}
