package taskmanager

import (
	"context"
	"errors"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func beginAcceptanceGameTask(t *testing.T, manager *Manager, task domaintask.Task) (domaintask.Run, GameTaskState) {
	t.Helper()
	run, err := manager.StartGameTaskRun(context.Background(), task.TaskID, modulecore.NewQueueItemID(), task.Assignee, "", "")
	if err != nil {
		t.Fatalf("StartGameTaskRun: %v", err)
	}
	state, created, err := manager.BeginGameLaunch(context.Background(), task.TaskID)
	if err != nil || !created || state.Progress.Status != domaintask.GameExecutionLaunching {
		t.Fatalf("BeginGameLaunch(first) = status %q, created %t, error %v", state.Progress.Status, created, err)
	}
	return run, state
}

func acceptanceReceipt(task domaintask.Task, spec domaintask.GameSpec, sessionID string, status domaintask.GameExecutionStatus, at time.Time) domaintask.GameLaunchReceipt {
	receipt := domaintask.GameLaunchReceipt{
		LaunchKey: string(task.TaskID), SessionID: sessionID, Status: status, Request: spec,
		CreatedAt: at, UpdatedAt: at.Add(time.Second),
	}
	switch status {
	case domaintask.GameExecutionCompleted:
		receipt.Result = &domaintask.GameLaunchResult{OK: true, GameID: spec.GameID, SessionID: sessionID, Status: string(status)}
		receipt.GameResultJSON = `{"winner":"aka","score":37}`
	case domaintask.GameExecutionFailed:
		receipt.Result = &domaintask.GameLaunchResult{OK: false, GameID: spec.GameID, SessionID: sessionID, Status: string(status)}
		receipt.Error = "title process exited"
	}
	return receipt
}

func TestGameTaskRecoveryAcceptanceBindsExactLaunchAndTerminalReceipts(t *testing.T) {
	ctx := context.Background()
	manager, task, spec := newGameTaskFixture(t, "survival_garden")
	if task.Route != domaintask.RouteGAME || spec.GameID != "survival_garden" {
		t.Fatalf("manager-created task is not the requested GAME task: route=%q spec=%+v", task.Route, spec)
	}
	run, launching := beginAcceptanceGameTask(t, manager, task)
	if launching.Progress.LaunchKey != string(task.TaskID) {
		t.Fatalf("launch key = %q, want stable TaskID %q", launching.Progress.LaunchKey, task.TaskID)
	}
	if _, created, err := manager.BeginGameLaunch(ctx, task.TaskID); err != nil || created {
		t.Fatalf("duplicate BeginGameLaunch created a second intent: created=%t err=%v", created, err)
	}
	if _, err := manager.Cancel(ctx, task.TaskID, "cancel after launch intent"); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("Cancel after launch intent = %v, want ErrRunConflict", err)
	}
	if _, err := manager.MarkGameLaunchUnknown(ctx, task.TaskID, "POST response was lost"); err != nil {
		t.Fatalf("MarkGameLaunchUnknown: %v", err)
	}
	now := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	receipt := acceptanceReceipt(task, spec, "sg_opaque:session.with-punctuation", domaintask.GameExecutionRunning, now)
	wrongKey := receipt
	wrongKey.LaunchKey = "different-task-key"
	if _, err := manager.RecordGameLaunchReceipt(ctx, task.TaskID, wrongKey); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("receipt with a different launch key = %v, want ErrRunConflict", err)
	}
	wrongRequest := receipt
	wrongRequest.Request.Turns++
	if _, err := manager.RecordGameLaunchReceipt(ctx, task.TaskID, wrongRequest); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("receipt with a different request = %v, want ErrRunConflict", err)
	}
	bound, err := manager.RecordGameLaunchReceipt(ctx, task.TaskID, receipt)
	if err != nil || bound.Progress.Status != domaintask.GameExecutionRunning || bound.Progress.SessionID != receipt.SessionID {
		t.Fatalf("RecordGameLaunchReceipt(running) = (%+v, %v)", bound.Progress, err)
	}
	// A callback that arrives before the POST response and the later response
	// itself must resolve to the same exact session receipt.
	if replayed, err := manager.RecordGameLaunchReceipt(ctx, task.TaskID, receipt); err != nil || replayed.Progress.SessionID != receipt.SessionID {
		t.Fatalf("same receipt replay changed the binding: state=%+v err=%v", replayed.Progress, err)
	}
	changedSession := receipt
	changedSession.SessionID = "sg_different_session"
	if _, err := manager.RecordGameLaunchReceipt(ctx, task.TaskID, changedSession); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("receipt changed the bound session = %v, want ErrRunConflict", err)
	}
	if _, err := manager.Succeed(ctx, task.TaskID, "premature completion"); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("Succeed while GAMES session is running = %v, want ErrRunConflict", err)
	}
	if _, err := manager.Fail(ctx, task.TaskID, "premature failure", nil); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("Fail while GAMES session is running = %v, want ErrRunConflict", err)
	}
	if _, err := manager.Cancel(ctx, task.TaskID, "cancel active GAMES session"); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("Cancel while GAMES session is running = %v, want ErrRunConflict", err)
	}
	if err := manager.PauseGameTaskRun(ctx, task.TaskID, run.RunID, task.Assignee); err != nil {
		t.Fatalf("PauseGameTaskRun: %v", err)
	}
	currentRun, err := manager.StartGameTaskRun(ctx, task.TaskID, modulecore.NewQueueItemID(), task.Assignee, run.RunID, spec.Digest())
	if err != nil {
		t.Fatalf("StartGameTaskRun(resume): %v", err)
	}
	completedReceipt := acceptanceReceipt(task, spec, receipt.SessionID, domaintask.GameExecutionCompleted, now.Add(2*time.Second))
	completed, err := manager.FinalizeGameTaskRun(ctx, task.TaskID, currentRun.RunID, task.Assignee, completedReceipt, "verified title completion")
	if err != nil || completed.Status != domaintask.StatusSucceeded {
		t.Fatalf("FinalizeGameTaskRun(completed) = status %q, error %v", completed.Status, err)
	}
	replayed, err := manager.FinalizeGameTaskRun(ctx, task.TaskID, currentRun.RunID, task.Assignee, completedReceipt, "verified title completion")
	if err != nil || replayed.Status != domaintask.StatusSucceeded {
		t.Fatalf("same completed receipt retry = status %q, error %v", replayed.Status, err)
	}
	shared, err := manager.Context(ctx, task.TaskID)
	if err != nil || shared.GameProgress == nil || shared.GameProgress.GameResultJSON != completedReceipt.GameResultJSON {
		t.Fatalf("completed title outcome not persisted: progress=%+v err=%v", shared.GameProgress, err)
	}
}

func TestOneGameCompletionRequiresVerifiedSessionReceipt(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := New(store, DefaultParallelLimits())
	t.Cleanup(func() { _ = manager.Close() })
	draft, shared := gameTaskCreateInput(t, "one game", "play one session")
	draft.Timing = &modulecore.TaskTiming{CompletionCondition: &modulecore.TaskCompletionCondition{Kind: "count", TargetCount: 1, CountUnit: "game"}}
	task, err := manager.EnsureGameTaskCreated(ctx, modulecore.NewRequestID(), "ren", draft, shared)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := beginAcceptanceGameTask(t, manager, task)
	now := time.Now().UTC()
	running := acceptanceReceipt(task, *shared.GameSpec, "session-one", domaintask.GameExecutionRunning, now)
	if _, err := manager.RecordGameLaunchReceipt(ctx, task.TaskID, running); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Succeed(ctx, task.TaskID, "unverified"); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("unverified session completion error=%v", err)
	}
	completed := acceptanceReceipt(task, *shared.GameSpec, running.SessionID, domaintask.GameExecutionCompleted, now.Add(2*time.Second))
	finished, err := manager.FinalizeGameTaskRun(ctx, task.TaskID, run.RunID, task.Assignee, completed, "verified session completed")
	if err != nil || finished.Status != domaintask.StatusSucceeded {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
}

func TestUntilGameCompleteRequiresVerifiedSessionReceipt(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := New(store, DefaultParallelLimits())
	t.Cleanup(func() { _ = manager.Close() })
	draft, shared := gameTaskCreateInput(t, "complete game", "play until session completes")
	draft.Timing = &modulecore.TaskTiming{CompletionCondition: &modulecore.TaskCompletionCondition{Kind: "until_complete", Criteria: "game_session_completed"}}
	task, err := manager.EnsureGameTaskCreated(ctx, modulecore.NewRequestID(), "ren", draft, shared)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := beginAcceptanceGameTask(t, manager, task)
	now := time.Now().UTC()
	running := acceptanceReceipt(task, *shared.GameSpec, "session-one", domaintask.GameExecutionRunning, now)
	if _, err := manager.RecordGameLaunchReceipt(ctx, task.TaskID, running); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Succeed(ctx, task.TaskID, "unverified"); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("unverified session completion error=%v", err)
	}
	completed := acceptanceReceipt(task, *shared.GameSpec, running.SessionID, domaintask.GameExecutionCompleted, now.Add(2*time.Second))
	finished, err := manager.FinalizeGameTaskRun(ctx, task.TaskID, run.RunID, task.Assignee, completed, "verified session completed")
	if err != nil || finished.Status != domaintask.StatusSucceeded {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
}

func TestGameTaskRecoveryAcceptanceKeepsLaunchKeyAcrossStoreRestart(t *testing.T) {
	ctx := context.Background()
	storeRoot := t.TempDir()
	store, err := taskpersistence.NewJSONLStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	manager := New(store, DefaultParallelLimits())
	spec, err := domaintask.NormalizeGameSpec(domaintask.GameSpec{GameID: "herzog_zwei"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := manager.Create(ctx, domaintask.Task{Title: "Play herzog_zwei", OwnerID: "operator", Assignee: "mio", Route: domaintask.RouteGAME}, domaintask.SharedRoleContext{GameSpec: &spec})
	if err != nil {
		t.Fatal(err)
	}
	_, first := beginAcceptanceGameTask(t, manager, task)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = taskpersistence.NewJSONLStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	restarted := New(store, DefaultParallelLimits())
	retried, created, err := restarted.BeginGameLaunch(ctx, task.TaskID)
	if err != nil || created {
		t.Fatalf("BeginGameLaunch after store restart = created %t, error %v", created, err)
	}
	if retried.Progress.LaunchKey != first.Progress.LaunchKey || retried.Progress.LaunchKey != string(task.TaskID) || retried.Progress.SpecDigest != spec.Digest() || retried.Progress.Status != domaintask.GameExecutionLaunching {
		t.Fatalf("restart reset launch checkpoint: before=%+v after=%+v", first.Progress, retried.Progress)
	}
}

func TestGameTaskRecoveryAcceptancePreservesOwnerManagedCheckpointOnGenericContextUpdate(t *testing.T) {
	ctx := context.Background()
	manager, task, spec := newGameTaskFixture(t, "survival_garden")
	_, original := beginAcceptanceGameTask(t, manager, task)
	shared, err := manager.Context(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	forgedSpec, err := domaintask.NormalizeGameSpec(domaintask.GameSpec{GameID: "herzog_zwei", Turns: 77})
	if err != nil {
		t.Fatal(err)
	}
	shared.GameSpec = &forgedSpec
	shared.GameProgress = &domaintask.GameProgress{LaunchKey: "forged", Status: domaintask.GameExecutionCompleted}
	if err := manager.UpdateContext(ctx, shared); err != nil {
		t.Fatalf("generic UpdateContext: %v", err)
	}
	after, err := manager.GetGameTaskState(ctx, task.TaskID)
	if err != nil || after.Spec.Digest() != spec.Digest() || after.Progress.LaunchKey != original.Progress.LaunchKey || after.Progress.Status != original.Progress.Status {
		t.Fatalf("generic context update changed owner-managed GAME checkpoint: state=%+v err=%v", after, err)
	}
}

func TestGameTaskRecoveryAcceptanceRequiresFailedReceiptForFailure(t *testing.T) {
	ctx := context.Background()
	manager, task, spec := newGameTaskFixture(t, "survival_garden")
	run, _ := beginAcceptanceGameTask(t, manager, task)
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	failedReceipt := acceptanceReceipt(task, spec, "sg_failed_exact", domaintask.GameExecutionFailed, now)
	failed, err := manager.FinalizeGameTaskRun(ctx, task.TaskID, run.RunID, task.Assignee, failedReceipt, "GAMES process failed")
	if err != nil || failed.Status != domaintask.StatusFailed {
		t.Fatalf("FinalizeGameTaskRun(failed) = status %q, error %v", failed.Status, err)
	}
	replayed, err := manager.FinalizeGameTaskRun(ctx, task.TaskID, run.RunID, task.Assignee, failedReceipt, "GAMES process failed")
	if err != nil || replayed.Status != domaintask.StatusFailed {
		t.Fatalf("same failed receipt retry = status %q, error %v", replayed.Status, err)
	}
	if _, err := manager.Succeed(ctx, task.TaskID, "failed session succeeded"); err == nil {
		t.Fatal("Succeed after failed GAMES receipt unexpectedly succeeded")
	}
}

func TestGameTaskCanCancelBeforeLaunchIntentAcceptance(t *testing.T) {
	manager, task, _ := newGameTaskFixture(t, "herzog_zwei")
	if _, err := manager.Cancel(context.Background(), task.TaskID, "cancel before external launch intent"); err != nil {
		t.Fatalf("Cancel before launch intent: %v", err)
	}
}
