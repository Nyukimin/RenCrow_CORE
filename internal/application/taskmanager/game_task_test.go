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

func newGameTaskFixture(t *testing.T, gameID string) (*Manager, domaintask.Task, domaintask.GameSpec) {
	t.Helper()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	spec, err := domaintask.NormalizeGameSpec(domaintask.GameSpec{GameID: gameID})
	if err != nil {
		t.Fatal(err)
	}
	task, err := manager.Create(context.Background(), domaintask.Task{
		Title: "Play " + gameID, OwnerID: "operator", Assignee: "mio", Route: domaintask.RouteGAME,
	}, domaintask.SharedRoleContext{GameSpec: &spec})
	if err != nil {
		t.Fatalf("create GAME Task: %v", err)
	}
	return manager, task, spec
}

func TestGameTaskLaunchIntentAndReceiptOwnTerminalTransitions(t *testing.T) {
	ctx := context.Background()
	manager, task, spec := newGameTaskFixture(t, "survival_garden")
	if _, err := manager.Succeed(ctx, task.TaskID, "unverified"); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("Succeed before launch receipt error = %v, want ErrRunConflict", err)
	}
	run, err := manager.StartGameTaskRun(ctx, task.TaskID, modulecore.NewQueueItemID(), "mio", "", "")
	if err != nil {
		t.Fatalf("StartGameTaskRun(first): %v", err)
	}
	intent, newlyCreated, err := manager.BeginGameLaunch(ctx, task.TaskID)
	if err != nil || !newlyCreated || intent.Progress.Status != domaintask.GameExecutionLaunching || intent.Progress.LaunchKey != string(task.TaskID) {
		t.Fatalf("BeginGameLaunch(first) = (%+v, %t, %v)", intent, newlyCreated, err)
	}
	if _, newlyCreated, err := manager.BeginGameLaunch(ctx, task.TaskID); err != nil || newlyCreated {
		t.Fatalf("BeginGameLaunch(retry) = newlyCreated %t, err %v; want reconciliation only", newlyCreated, err)
	}
	if _, err := manager.Cancel(ctx, task.TaskID, "operator cancellation after launch"); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("Cancel after launch intent error = %v, want ErrRunConflict", err)
	}
	if _, err := manager.MarkGameLaunchUnknown(ctx, task.TaskID, "receipt temporarily unavailable"); err != nil {
		t.Fatalf("MarkGameLaunchUnknown: %v", err)
	}
	now := time.Now().UTC()
	receipt := domaintask.GameLaunchReceipt{
		LaunchKey: string(task.TaskID), SessionID: "survival_garden_20261118T000000Z_1", Status: domaintask.GameExecutionRunning,
		Request: spec, CreatedAt: now, UpdatedAt: now,
	}
	bound, err := manager.RecordGameLaunchReceipt(ctx, task.TaskID, receipt)
	if err != nil || bound.Progress.SessionID != receipt.SessionID || bound.Progress.Status != domaintask.GameExecutionRunning {
		t.Fatalf("RecordGameLaunchReceipt(running) = (%+v, %v)", bound.Progress, err)
	}
	if err := manager.PauseGameTaskRun(ctx, task.TaskID, run.RunID, "mio"); err != nil {
		t.Fatalf("PauseGameTaskRun: %v", err)
	}
	if _, err := manager.Succeed(ctx, task.TaskID, "false completion"); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("Succeed while session is running error = %v, want ErrRunConflict", err)
	}
	resumedRun, err := manager.StartGameTaskRun(ctx, task.TaskID, modulecore.NewQueueItemID(), "mio", run.RunID, spec.Digest())
	if err != nil {
		t.Fatalf("StartGameTaskRun(resume): %v", err)
	}
	terminal := domaintask.GameLaunchReceipt{
		LaunchKey: string(task.TaskID), SessionID: receipt.SessionID, Status: domaintask.GameExecutionCompleted,
		Request: spec, Result: &domaintask.GameLaunchResult{OK: true, GameID: spec.GameID, SessionID: receipt.SessionID, Status: "completed"},
		GameResultJSON: `{"winner":"aka","run_summary":"completed game"}`, CreatedAt: now, UpdatedAt: now.Add(time.Second),
	}
	summary := "survival_garden session completed for [aka,ao,kin,gin]; title outcome " + terminal.GameResultJSON
	completed, err := manager.FinalizeGameTaskRun(ctx, task.TaskID, resumedRun.RunID, "mio", terminal, summary)
	if err != nil || completed.Status != domaintask.StatusSucceeded {
		t.Fatalf("FinalizeGameTaskRun after terminal receipt = (%s, %v)", completed.Status, err)
	}
	shared, err := manager.Context(ctx, task.TaskID)
	if err != nil || shared.GameProgress == nil || shared.GameProgress.GameResultJSON != terminal.GameResultJSON {
		t.Fatalf("completed result was not retained in the Task context: (%+v, %v)", shared.GameProgress, err)
	}
	if _, err := manager.FinalizeGameTaskRun(ctx, task.TaskID, resumedRun.RunID, "mio", terminal, summary); err != nil {
		t.Fatalf("FinalizeGameTaskRun retry after commit: %v", err)
	}
}

func TestGameTaskCanCancelBeforeLaunchIntent(t *testing.T) {
	manager, task, _ := newGameTaskFixture(t, "herzog_zwei")
	if _, err := manager.RecordAssignment(context.Background(), task.TaskID, "aka", modulecore.NewEventID()); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("RecordAssignment() error = %v, want ErrRunConflict", err)
	}
	if _, err := manager.RecordRouting(context.Background(), task.TaskID, domaintask.RouteCHAT, modulecore.NewEventID()); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("RecordRouting() error = %v, want ErrRunConflict", err)
	}
	if _, err := manager.Cancel(context.Background(), task.TaskID, "cancel before launch"); err != nil {
		t.Fatalf("Cancel before launch intent: %v", err)
	}
}

func TestGenericTaskTransitionsCannotPauseOrCloseLaunchedGame(t *testing.T) {
	ctx := context.Background()
	manager, task, spec := newGameTaskFixture(t, "nethack")
	run, err := manager.StartGameTaskRun(ctx, task.TaskID, modulecore.NewQueueItemID(), "mio", "", "")
	if err != nil {
		t.Fatalf("StartGameTaskRun(first): %v", err)
	}
	launch, created, err := manager.BeginGameLaunch(ctx, task.TaskID)
	if err != nil || !created {
		t.Fatalf("BeginGameLaunch() = (%t, %v)", created, err)
	}
	now := time.Now().UTC()
	receipt := domaintask.GameLaunchReceipt{
		LaunchKey: launch.Progress.LaunchKey, SessionID: "nethack_20261118T000000000Z_1",
		Status: domaintask.GameExecutionRunning, Request: spec, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := manager.RecordGameLaunchReceipt(ctx, task.TaskID, receipt); err != nil {
		t.Fatalf("RecordGameLaunchReceipt(running): %v", err)
	}

	if _, err := manager.Wait(ctx, task.TaskID, "operator wait"); !errors.Is(err, ErrRunConflict) {
		t.Errorf("Wait after launch intent error = %v, want ErrRunConflict", err)
	}
	if _, err := manager.Block(ctx, task.TaskID, "operator block"); !errors.Is(err, ErrRunConflict) {
		t.Errorf("Block after launch intent error = %v, want ErrRunConflict", err)
	}
	if _, err := manager.Resume(ctx, task.TaskID); !errors.Is(err, ErrRunConflict) {
		t.Errorf("Resume after launch intent error = %v, want ErrRunConflict", err)
	}
	if _, err := manager.Cancel(ctx, task.TaskID, "operator cancellation"); !errors.Is(err, ErrRunConflict) {
		t.Errorf("Cancel after launch intent error = %v, want ErrRunConflict", err)
	}
	if _, err := manager.CompleteRun(ctx, task.TaskID, run.RunID, "mio", domaintask.StatusWaiting, "manual close", "waiting"); !errors.Is(err, ErrRunConflict) {
		t.Errorf("CompleteRun after launch intent error = %v, want ErrRunConflict", err)
	}
	if _, err := manager.StartWithReason(ctx, task.TaskID, domaintask.RunStartReasonExplicitRerun); !errors.Is(err, ErrRunConflict) {
		t.Errorf("StartWithReason after launch intent error = %v, want ErrRunConflict", err)
	}

	current, err := manager.GetGameTaskState(ctx, task.TaskID)
	if err != nil || current.Task.Status != domaintask.StatusRunning || current.Progress.Status != domaintask.GameExecutionRunning {
		t.Fatalf("generic transition changed launched GAME Task: task=%s progress=%s err=%v", current.Task.Status, current.Progress.Status, err)
	}
	if err := manager.ValidateRunExecution(ctx, task.TaskID, run.RunID, "mio"); err != nil {
		t.Fatalf("generic transition closed active controller Run: %v", err)
	}
}

func TestGameTaskReceiptCannotRegressRunningStatus(t *testing.T) {
	ctx := context.Background()
	manager, task, spec := newGameTaskFixture(t, "territory_commander")
	if _, err := manager.StartGameTaskRun(ctx, task.TaskID, modulecore.NewQueueItemID(), "mio", "", ""); err != nil {
		t.Fatalf("StartGameTaskRun(first): %v", err)
	}
	launch, _, err := manager.BeginGameLaunch(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("BeginGameLaunch(): %v", err)
	}
	now := time.Now().UTC()
	running := domaintask.GameLaunchReceipt{
		LaunchKey: launch.Progress.LaunchKey, SessionID: "territory_commander_20261118T000000000Z_1",
		Status: domaintask.GameExecutionRunning, Request: spec, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := manager.RecordGameLaunchReceipt(ctx, task.TaskID, running); err != nil {
		t.Fatalf("RecordGameLaunchReceipt(running): %v", err)
	}
	stale := running
	stale.Status = domaintask.GameExecutionLaunching
	stale.UpdatedAt = now.Add(time.Second)
	if _, err := manager.RecordGameLaunchReceipt(ctx, task.TaskID, stale); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("RecordGameLaunchReceipt(stale launching) error = %v, want ErrRunConflict", err)
	}
	current, err := manager.GetGameTaskState(ctx, task.TaskID)
	if err != nil || current.Progress.Status != domaintask.GameExecutionRunning {
		t.Fatalf("stale receipt regressed durable status to %s: err=%v", current.Progress.Status, err)
	}
}
