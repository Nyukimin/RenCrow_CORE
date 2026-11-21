package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

func TestTypedGameTaskOwnsCurrentActivityAcrossControllerPauseAndReceipt(t *testing.T) {
	ctx := context.Background()
	manager, task, spec := newGameTaskFixture(t, "survival_garden")
	for _, actorID := range spec.Personas {
		if _, err := manager.CreateCurrentActivity(ctx, domaintask.Task{Title: "Evaluating next activity", Route: domaintask.RouteGeneral, Assignee: actorID}, domaintask.ActivityMetadata{
			Kind: domaintask.ActivityKindEvaluating, Label: "Evaluating next activity", Phase: "evaluating", Passive: true,
			Participants: []domaintask.ActivityParticipant{{ActorID: actorID}},
		}); err != nil {
			t.Fatalf("create initial evaluation for %s: %v", actorID, err)
		}
	}
	run, err := manager.StartGameTaskRun(ctx, task.TaskID, modulecore.NewQueueItemID(), task.Assignee, "", "")
	if err != nil {
		t.Fatal(err)
	}
	state, created, err := manager.BeginGameLaunch(ctx, task.TaskID)
	if err != nil || !created || state.Task.Activity == nil || state.Task.Activity.Game == nil ||
		state.Task.Activity.Game.GameID != spec.GameID || state.Task.Activity.Phase != "launching" {
		t.Fatalf("BeginGameLaunch activity=%#v created=%t err=%v", state.Task.Activity, created, err)
	}
	current, err := manager.SnapshotCurrentActivities(ctx, spec.Personas)
	if err != nil {
		t.Fatal(err)
	}
	for _, actorID := range spec.Personas {
		if current[actorID].Task.TaskID != task.TaskID {
			t.Fatalf("actor %s current Task=%s, want typed GAME Task %s", actorID, current[actorID].Task.TaskID, task.TaskID)
		}
	}
	now := time.Now().UTC()
	receipt := domaintask.GameLaunchReceipt{LaunchKey: string(task.TaskID), SessionID: "survival_garden_live_session", Status: domaintask.GameExecutionRunning,
		Request: spec, CreatedAt: now, UpdatedAt: now.Add(time.Second)}
	state, err = manager.RecordGameLaunchReceipt(ctx, task.TaskID, receipt)
	if err != nil || state.Task.TaskID != task.TaskID || state.Task.Activity == nil || state.Task.Activity.Game == nil ||
		state.Task.Activity.Game.SessionID != receipt.SessionID || state.Task.Activity.Phase != "playing" {
		t.Fatalf("receipt Game activity=%#v err=%v", state.Task.Activity, err)
	}
	if err := manager.PauseGameTaskRun(ctx, task.TaskID, run.RunID, task.Assignee); err != nil {
		t.Fatal(err)
	}
	paused, err := manager.Get(ctx, task.TaskID)
	if err != nil || paused.Status != domaintask.StatusWaiting {
		t.Fatalf("paused typed GAME Task=%#v err=%v", paused, err)
	}
	current, err = manager.SnapshotCurrentActivities(ctx, spec.Personas)
	if err != nil {
		t.Fatal(err)
	}
	for _, actorID := range spec.Personas {
		if current[actorID].Task.TaskID != task.TaskID {
			t.Fatalf("actor %s lost GAME Task during controller pause", actorID)
		}
	}
	resumed, err := manager.StartGameTaskRun(ctx, task.TaskID, modulecore.NewQueueItemID(), task.Assignee, run.RunID, spec.Digest())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.BeginGameLaunch(ctx, task.TaskID); err != nil {
		t.Fatalf("resume exact Game Task activity: %v", err)
	}
	terminal := acceptanceReceipt(task, spec, receipt.SessionID, domaintask.GameExecutionCompleted, now.Add(2*time.Second))
	if _, err := manager.FinalizeGameTaskRun(ctx, task.TaskID, resumed.RunID, task.Assignee, terminal, "game complete"); err != nil {
		t.Fatalf("finalize typed GAME Task: %v", err)
	}
	current, err = manager.SnapshotCurrentActivities(ctx, spec.Personas)
	if err != nil {
		t.Fatal(err)
	}
	for _, actorID := range spec.Personas {
		if current[actorID].Task.TaskID == task.TaskID || current[actorID].Task.Activity.Kind != domaintask.ActivityKindEvaluating {
			t.Fatalf("actor %s terminal current Task=%#v, want evaluating successor", actorID, current[actorID].Task)
		}
	}
}

func TestBeginGameLaunchRollsBackIntentWhenActivityWriteFails(t *testing.T) {
	ctx := context.Background()
	manager, task, spec := newGameTaskFixture(t, "survival_garden")
	if _, err := manager.StartGameTaskRun(ctx, task.TaskID, modulecore.NewQueueItemID(), task.Assignee, "", ""); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("typed game activity append failed")
	faultManager := New(&faultTaskStore{Store: manager.store, failOperation: "activity", failErr: injected}, DefaultParallelLimits())
	if _, created, err := faultManager.BeginGameLaunch(ctx, task.TaskID); !errors.Is(err, injected) || created {
		t.Fatalf("BeginGameLaunch with activity failure=(created %t, err %v), want rollback and injected error", created, err)
	}
	afterFailure, err := manager.GetGameTaskState(ctx, task.TaskID)
	if err != nil || afterFailure.Progress.Status != domaintask.GameExecutionPrepared || afterFailure.Progress.LaunchKey != string(task.TaskID) {
		t.Fatalf("failed BeginGameLaunch left progress=%+v err=%v; want prepared exact launch key", afterFailure.Progress, err)
	}
	retried, created, err := manager.BeginGameLaunch(ctx, task.TaskID)
	if err != nil || !created || retried.Progress.Status != domaintask.GameExecutionLaunching || retried.Progress.LaunchKey != string(task.TaskID) {
		t.Fatalf("retry BeginGameLaunch=(status %s, created %t, key %q, err %v), want one launch of the same TaskID key", retried.Progress.Status, created, retried.Progress.LaunchKey, err)
	}
	if retried.Spec.GameID != spec.GameID {
		t.Fatalf("retry Game spec=%+v, want %s", retried.Spec, spec.GameID)
	}
	if _, err := faultManager.MarkGameLaunchUnknown(ctx, task.TaskID, "receipt read failed"); !errors.Is(err, injected) {
		t.Fatalf("MarkGameLaunchUnknown activity failure=%v, want injected error", err)
	}
	afterUnknownFailure, err := manager.GetGameTaskState(ctx, task.TaskID)
	if err != nil || afterUnknownFailure.Progress.Status != domaintask.GameExecutionLaunching || afterUnknownFailure.Progress.SessionID != "" {
		t.Fatalf("failed unknown-state write left progress=%+v err=%v; want launching checkpoint", afterUnknownFailure.Progress, err)
	}
	if _, err := manager.MarkGameLaunchUnknown(ctx, task.TaskID, "receipt read failed"); err != nil {
		t.Fatalf("retry MarkGameLaunchUnknown: %v", err)
	}
	now := time.Now().UTC()
	receipt := domaintask.GameLaunchReceipt{LaunchKey: string(task.TaskID), SessionID: "session:after-commit-guard", Status: domaintask.GameExecutionRunning,
		Request: spec, CreatedAt: now, UpdatedAt: now.Add(time.Second)}
	if _, err := faultManager.RecordGameLaunchReceipt(ctx, task.TaskID, receipt); !errors.Is(err, injected) {
		t.Fatalf("RecordGameLaunchReceipt activity failure=%v, want injected error", err)
	}
	afterReceiptFailure, err := manager.GetGameTaskState(ctx, task.TaskID)
	if err != nil || afterReceiptFailure.Progress.Status != domaintask.GameExecutionUnknown || afterReceiptFailure.Progress.SessionID != "" {
		t.Fatalf("failed receipt write left progress=%+v err=%v; want exact unknown checkpoint", afterReceiptFailure.Progress, err)
	}
	bound, err := manager.RecordGameLaunchReceipt(ctx, task.TaskID, receipt)
	if err != nil || bound.Progress.Status != domaintask.GameExecutionRunning || bound.Progress.SessionID != receipt.SessionID ||
		bound.Task.Activity == nil || bound.Task.Activity.Game == nil || bound.Task.Activity.Game.SessionID != receipt.SessionID {
		t.Fatalf("receipt retry state=%+v err=%v, want atomically bound exact session", bound.Progress, err)
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

func TestHasTerminalGameTaskSessionUsesStableCursorAcrossPages(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.November, 18, 10, 0, 0, 0, time.UTC)
	spec, err := domaintask.NormalizeGameSpec(domaintask.GameSpec{
		GameID: "herzog_zwei", Personas: []string{"aka", "ao", "kin", "gin"}, Turns: 40, Mode: "realtime",
	})
	if err != nil {
		t.Fatalf("normalize GAME spec: %v", err)
	}
	store := &terminalGameTaskPageStore{contexts: make(map[modulecore.TaskID]domaintask.SharedRoleContext)}
	const taskCount = domaintask.MaxTaskListPageSize + 1
	typedTaskIDs := make([]modulecore.TaskID, 0, taskCount)
	for index := 0; index < taskCount; index++ {
		taskID := modulecore.NewTaskID()
		typedTaskIDs = append(typedTaskIDs, taskID)
		progress, err := domaintask.NewGameProgress(taskID, spec, now)
		if err != nil {
			t.Fatalf("create GAME progress %d: %v", index, err)
		}
		sessionID := fmt.Sprintf("herzog_zwei_terminal_%03d", index)
		resultOK := true
		progress.Status = domaintask.GameExecutionCompleted
		progress.SessionID = sessionID
		progress.ResultOK = &resultOK
		progress.ResultGameID = spec.GameID
		progress.ResultSessionID = sessionID
		progress.ResultStatus = string(domaintask.GameExecutionCompleted)
		progress.GameResultJSON = `{"winner":"aka"}`
		task := domaintask.Task{
			TaskID: taskID, Title: fmt.Sprintf("terminal game %d", index), OwnerID: "operator", Assignee: "mio",
			Route: domaintask.RouteGAME, Status: domaintask.StatusSucceeded, CreatedAt: now, UpdatedAt: now, Summary: "completed",
		}
		store.tasks = append(store.tasks, task)
		store.contexts[taskID] = domaintask.SharedRoleContext{TaskID: taskID, GameSpec: &spec, GameProgress: &progress, UpdatedAt: now}
	}
	legacyTaskID := modulecore.NewTaskID()
	store.tasks = append(store.tasks, domaintask.Task{
		TaskID: legacyTaskID, Title: "legacy generic GAME task", OwnerID: "operator", Assignee: "mio",
		Route: domaintask.RouteGAME, Status: domaintask.StatusSucceeded, CreatedAt: now, UpdatedAt: now, Summary: "legacy completion",
	})
	sort.Slice(typedTaskIDs, func(i, j int) bool { return typedTaskIDs[i] < typedTaskIDs[j] })
	oldestState := store.contexts[typedTaskIDs[0]]
	manager := New(store, DefaultParallelLimits())
	found, err := manager.HasTerminalGameTaskSession(ctx, spec.GameID, oldestState.GameProgress.SessionID, "aka")
	if err != nil || !found {
		t.Fatalf("terminal session in the second normal-order page = (%t, %v), want found", found, err)
	}
	found, err = manager.HasTerminalGameTaskSession(ctx, spec.GameID, oldestState.GameProgress.SessionID, "shiro")
	if err != nil || found {
		t.Fatalf("nonparticipant terminal session match = (%t, %v), want absent", found, err)
	}
	found, err = manager.HasTerminalGameTaskSession(ctx, spec.GameID, "herzog_zwei_legacy_session", "aka")
	if err != nil || found {
		t.Fatalf("different same-actor legacy session match = (%t, %v), want absent", found, err)
	}
}

type terminalGameTaskPageStore struct {
	domaintask.Store
	tasks    []domaintask.Task
	contexts map[modulecore.TaskID]domaintask.SharedRoleContext
}

func (store *terminalGameTaskPageStore) ListTasks(_ context.Context, filter domaintask.Filter) ([]domaintask.Task, error) {
	items := make([]domaintask.Task, 0, len(store.tasks))
	for _, item := range store.tasks {
		if filter.Route != "" && item.Route != filter.Route {
			continue
		}
		if filter.Status != "" && item.Status != filter.Status {
			continue
		}
		if filter.TaskIDAfter != nil && item.TaskID <= *filter.TaskIDAfter {
			continue
		}
		items = append(items, item)
	}
	if filter.TaskIDAfter == nil {
		sort.Slice(items, func(i, j int) bool { return items[i].TaskID > items[j].TaskID })
	} else {
		sort.Slice(items, func(i, j int) bool { return items[i].TaskID < items[j].TaskID })
	}
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items, nil
}

func (store *terminalGameTaskPageStore) GetTask(_ context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	for _, item := range store.tasks {
		if item.TaskID == taskID {
			return item, nil
		}
	}
	return domaintask.Task{}, domaintask.ErrNotFound
}

func (store *terminalGameTaskPageStore) GetContext(_ context.Context, taskID modulecore.TaskID) (domaintask.SharedRoleContext, error) {
	shared, ok := store.contexts[taskID]
	if !ok {
		return domaintask.SharedRoleContext{}, domaintask.ErrNotFound
	}
	return shared, nil
}
