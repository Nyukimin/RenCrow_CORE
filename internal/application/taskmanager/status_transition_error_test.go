package taskmanager

import (
	"context"
	"errors"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

// TestManagerTerminalOverwriteReportsAlreadyTerminalThroughRealStore は、実JSONL
// storeのトランザクション越しでも、終端済みTaskへの別終端の書き込み拒否が
// 型付きエラーとして呼び出し側へ届くことを検証する。Heartbeatの終端再試行は
// この信号だけで「別経路が既に決着させた」と判断する。
func TestManagerTerminalOverwriteReportsAlreadyTerminalThroughRealStore(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	ctx := context.Background()
	value, err := manager.Create(ctx, domaintask.Task{Title: "terminal overwrite", Route: domaintask.RouteOperations, Assignee: "shiro"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StartRunWithReason(ctx, value.TaskID, domaintask.RunStartReasonFirst); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Fail(ctx, value.TaskID, "recovered by restart recovery", nil); err != nil {
		t.Fatal(err)
	}

	_, err = manager.Succeed(ctx, value.TaskID, "late success")
	if err == nil {
		t.Fatal("Succeed overwrote an already failed Task")
	}
	var transition *domaintask.InvalidStatusTransitionError
	if !errors.As(err, &transition) || transition.From != domaintask.StatusFailed || transition.To != domaintask.StatusSucceeded {
		t.Fatalf("error is not a typed failed->succeeded transition rejection: %T %v", err, err)
	}
	if !domaintask.AlreadyTerminal(err) {
		t.Fatalf("AlreadyTerminal did not recognize %v", err)
	}
	if err.Error() != "invalid status transition: failed -> succeeded" {
		t.Fatalf("message changed: %q", err.Error())
	}
	got, err := manager.Get(ctx, value.TaskID)
	if err != nil || got.Status != domaintask.StatusFailed || got.Summary != "recovered by restart recovery" {
		t.Fatalf("rejected overwrite changed the Task: %+v err=%v", got, err)
	}
}

// TestManagerNonTerminalInvalidTransitionIsNotAlreadyTerminal は、待機中Taskの
// 無効遷移を「既に終端」と誤認しないことを検証する。
func TestManagerNonTerminalInvalidTransitionIsNotAlreadyTerminal(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	ctx := context.Background()
	value, err := manager.Create(ctx, domaintask.Task{Title: "waiting", Route: domaintask.RouteCode}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(ctx, value.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Wait(ctx, value.TaskID, "external review"); err != nil {
		t.Fatal(err)
	}
	_, err = manager.Succeed(ctx, value.TaskID, "premature")
	if err == nil {
		t.Fatal("waiting Task was succeeded directly")
	}
	var transition *domaintask.InvalidStatusTransitionError
	if !errors.As(err, &transition) || transition.From != domaintask.StatusWaiting {
		t.Fatalf("error is not a typed waiting->succeeded rejection: %T %v", err, err)
	}
	if domaintask.AlreadyTerminal(err) {
		t.Fatalf("waiting Task was reported as already terminal: %v", err)
	}
}
