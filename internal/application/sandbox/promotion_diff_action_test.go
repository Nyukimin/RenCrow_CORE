package sandbox

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

func TestPromotionDiffApplierCompletesCanonicalPatchAction(t *testing.T) {
	sandboxRoot := t.TempDir()
	applyRoot := t.TempDir()
	writeFile(t, filepath.Join(applyRoot, "docs", "example.md"), "before\n")
	writeFile(t, filepath.Join(sandboxRoot, "diff.patch"), `diff --git a/docs/example.md b/docs/example.md
--- a/docs/example.md
+++ b/docs/example.md
@@ -1 +1 @@
-before
+after
`)
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
	applier := NewPromotionDiffApplier(sandboxRoot, applyRoot).WithExecutionOwners(actions, tasks, "shiro")

	if _, err := applier.ApplyPromotionDiff(context.Background(), domainsandbox.PromotionApplyRequest{
		Promotion: domainsandbox.PromotionRequest{DiffPath: "diff.patch"},
	}); err != nil {
		t.Fatal(err)
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
	if action.Kind != domainaction.KindPatchApply || action.Status != domainaction.StatusSucceeded || len(attempts) != 1 || attempts[0].Status != domainaction.AttemptStatusSucceeded {
		t.Fatalf("Patch Action lifecycle action=%#v attempts=%#v", action, attempts)
	}
	if task.Status != domaintask.StatusSucceeded || run.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("Patch Task/Run lifecycle task=%#v run=%#v", task, run)
	}
}
