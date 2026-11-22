package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type currentActivityGameReader struct {
	active  []gameObserverActiveAgentSession
	summary gameObserverSessionSummary
}

func TestStartupRecoversPendingRoutineAfterTerminalTask(t *testing.T) {
	ctx := context.Background()
	owner, closeStore := newCurrentActivityTestOwner(t)
	defer closeStore()
	routineStore, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer routineStore.Close()
	now := time.Date(2026, time.November, 21, 11, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	runtime := &agentRoutineRuntime{store: routineStore, tasks: owner, ownerID: "mio", now: func() time.Time { return now }}

	taskID := modulecore.NewTaskID()
	draft := domaintask.Task{TaskID: taskID, Title: "CORE routine hobby", ModuleID: agentRoutineModuleID, OwnerID: "mio", Assignee: "mio", Route: domaintask.RouteCHAT}
	routineTask, run, err := owner.CreateAndStartRun(ctx, draft, domaintask.SharedRoleContext{TaskID: taskID, UserIntent: "routine checkpoint"})
	if err != nil {
		t.Fatalf("create checkpoint Task and Run: %v", err)
	}
	if _, err := owner.CompleteRun(ctx, routineTask.TaskID, run.RunID, "mio", domaintask.StatusSucceeded, "routine result saved", ""); err != nil {
		t.Fatalf("complete checkpoint Task and Run: %v", err)
	}
	progress := routineProgress{
		Actors:          []routineActorProgress{{ActorID: "mio", TaskID: routineTask.TaskID, RunID: run.RunID}},
		EffectCompleted: true, CompletedAt: now.Add(-time.Minute),
	}
	encoded, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	state, err := routineStore.DraftIntent(ctx, agentroutine.Intent{
		ID: "routine-terminal-crash-window", ActorID: "mio", Date: now.Format("2006-01-02"),
		Kind: agentroutine.ActivityHobby, Label: "fashion", Step: "effect_complete", Output: string(encoded), CreatedAt: now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("persist pending routine checkpoint: %v", err)
	}

	if err := initializeAgentCurrentActivities(ctx, owner, "mio", runtime, nil); err != nil {
		t.Fatalf("startup should recover around a terminal checkpoint Task: %v", err)
	}
	current, err := owner.SnapshotCurrentActivities(ctx, []string{"mio"})
	if err != nil || current["mio"].Task.Activity == nil || current["mio"].Task.Activity.Kind != domaintask.ActivityKindEvaluating {
		t.Fatalf("current after startup=%+v err=%v; want evaluator while scheduler cleans the checkpoint", current["mio"], err)
	}
	stored, err := routineStore.Load(ctx, "mio")
	if err != nil || stored.Pending == nil || stored.Pending.ID != state.Pending.ID {
		t.Fatalf("startup changed pending checkpoint=%+v err=%v", stored.Pending, err)
	}

	if err := runtime.run(ctx, nil, "mio", agentroutine.Plan{}); err != nil {
		t.Fatalf("scheduler should finish the successful terminal checkpoint: %v", err)
	}
	stored, err = routineStore.Load(ctx, "mio")
	if err != nil || stored.Pending != nil || stored.LastFinishedIntentID != state.Pending.ID {
		t.Fatalf("scheduler did not clear the recovered pending checkpoint: %+v err=%v", stored, err)
	}
}

func (r currentActivityGameReader) ActiveAgentSessions(context.Context) ([]gameObserverActiveAgentSession, error) {
	return append([]gameObserverActiveAgentSession(nil), r.active...), nil
}

func (r currentActivityGameReader) SessionSummary(context.Context, string) (gameObserverSessionSummary, error) {
	return r.summary, nil
}

func TestCurrentActivityBootstrapCreatesOneCanonicalTaskPerActor(t *testing.T) {
	ctx := context.Background()
	owner, closeStore := newCurrentActivityTestOwner(t)
	defer closeStore()
	if err := initializeAgentCurrentActivities(ctx, owner, "mio", nil, nil); err != nil {
		t.Fatalf("initialize current activities: %v", err)
	}
	snapshots, err := owner.SnapshotCurrentActivities(ctx, agentHeartbeatActorIDs)
	if err != nil {
		t.Fatalf("snapshot initialized activities: %v", err)
	}
	if len(snapshots) != len(agentHeartbeatActorIDs) {
		t.Fatalf("initialized snapshot count=%d, want %d", len(snapshots), len(agentHeartbeatActorIDs))
	}
	for _, actorID := range agentHeartbeatActorIDs {
		snapshot := snapshots[actorID]
		if snapshot.Task.TaskID == "" || snapshot.Task.Activity == nil || snapshot.Task.Activity.Kind != domaintask.ActivityKindEvaluating ||
			!snapshot.Participant.IsCurrent || snapshot.Participant.ActorID != actorID || snapshot.Participant.Revision == 0 {
			t.Errorf("current activity for %s = %+v, want one current evaluating Task", actorID, snapshot)
		}
	}
}

func TestMorningRoutineMovementUsesEachActorsCanonicalTask(t *testing.T) {
	ctx := context.Background()
	owner, closeStore := newCurrentActivityTestOwner(t)
	defer closeStore()
	now := time.Date(2026, time.November, 22, 8, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	runtime := &agentRoutineRuntime{tasks: owner, ownerID: "mio", now: func() time.Time { return now }}
	primaryTaskID, peerTaskID := modulecore.NewTaskID(), modulecore.NewTaskID()
	progress := routineProgress{Actors: []routineActorProgress{
		{ActorID: "mio", TaskID: primaryTaskID},
		{ActorID: "shiro", TaskID: peerTaskID},
	}}
	encoded, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	intent := &agentroutine.Intent{
		ID: "morning-movement-two-actors", ActorID: "mio", TaskID: string(primaryTaskID),
		Kind: agentroutine.ActivityMorningChat, Participants: []string{"shiro"}, Output: string(encoded),
	}
	state := agentroutine.State{Pending: intent}
	if got, err := routineActorTaskID(intent, "mio"); err != nil || got != primaryTaskID {
		t.Fatalf("primary actor TaskID=(%s,%v), want %s", got, err, primaryTaskID)
	}
	if got, err := routineActorTaskID(intent, "shiro"); err != nil || got != peerTaskID {
		t.Fatalf("peer actor TaskID=(%s,%v), want %s", got, err, peerTaskID)
	}
	_, _, err = owner.CreateAndStartActivityRun(ctx, domaintask.Task{
		TaskID: peerTaskID, Title: "CORE morning routine", ModuleID: agentRoutineModuleID,
		OwnerID: "shiro", Assignee: "shiro", Route: domaintask.RouteCHAT,
	}, agentRoutineTaskActivity(intent, "shiro"), domaintask.SharedRoleContext{
		TaskID: peerTaskID, UserIntent: "CORE routine step", CurrentPlan: "morning conversation",
	})
	if err != nil {
		t.Fatalf("create peer routine Task: %v", err)
	}
	dueAt := now.Add(5 * time.Minute)
	if err := runtime.setRoutineMovingActivity(ctx, state, "shiro", dueAt); err != nil {
		t.Fatalf("bind confirmed movement for peer: %v", err)
	}
	current, err := owner.SnapshotCurrentActivities(ctx, []string{"shiro"})
	if err != nil {
		t.Fatalf("read moving current Task for peer: %v", err)
	}
	moving := current["shiro"].Task
	if moving.Activity == nil || moving.Activity.Kind != domaintask.ActivityKindMoving || moving.ParentTaskID != peerTaskID {
		t.Fatalf("moving Task for peer = %+v, want parent %s", moving, peerTaskID)
	}
	if err := runtime.restoreRoutineTaskCurrent(ctx, state, "shiro"); err != nil {
		t.Fatalf("restore peer routine Task after arrival: %v", err)
	}
	current, err = owner.SnapshotCurrentActivities(ctx, []string{"shiro"})
	if err != nil || current["shiro"].Task.TaskID != peerTaskID {
		t.Fatalf("restored current Task for peer = %+v err=%v, want %s", current["shiro"], err, peerTaskID)
	}
}

func TestRoutineActorTaskIDRejectsInconsistentMultiActorCheckpoint(t *testing.T) {
	primaryTaskID, peerTaskID := modulecore.NewTaskID(), modulecore.NewTaskID()
	base := &agentroutine.Intent{
		ActorID: "mio", TaskID: string(primaryTaskID),
	}
	encode := func(actors ...routineActorProgress) *agentroutine.Intent {
		copy := *base
		progress, err := json.Marshal(routineProgress{Actors: actors})
		if err != nil {
			t.Fatal(err)
		}
		copy.Output = string(progress)
		return &copy
	}
	tests := []struct {
		name   string
		intent *agentroutine.Intent
		actor  string
	}{
		{name: "missing peer", intent: encode(routineActorProgress{ActorID: "mio", TaskID: primaryTaskID}), actor: "shiro"},
		{name: "foreign intent task", intent: func() *agentroutine.Intent {
			intent := encode(routineActorProgress{ActorID: "mio", TaskID: peerTaskID})
			return intent
		}(), actor: "mio"},
		{name: "duplicate peer", intent: encode(
			routineActorProgress{ActorID: "mio", TaskID: primaryTaskID},
			routineActorProgress{ActorID: "shiro", TaskID: peerTaskID},
			routineActorProgress{ActorID: "shiro", TaskID: modulecore.NewTaskID()},
		), actor: "shiro"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if taskID, err := routineActorTaskID(test.intent, test.actor); err == nil {
				t.Fatalf("routineActorTaskID()=(%s,nil), want checkpoint rejection", taskID)
			}
		})
	}
}

func TestRunningWorkRecoveryChoosesActiveRootOverProviderChild(t *testing.T) {
	ctx := context.Background()
	owner, closeStore := newCurrentActivityTestOwner(t)
	defer closeStore()
	root, _, err := owner.CreateAndStartRun(ctx, domaintask.Task{
		Title: "chat root", OwnerID: "mio", Assignee: "mio", Route: domaintask.RouteCHAT,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("start root Task: %v", err)
	}
	child, _, err := owner.CreateAndStartRun(ctx, domaintask.Task{
		Title: "provider child", OwnerID: "mio", Assignee: "mio", Route: domaintask.RouteGeneral, ParentTaskID: root.TaskID,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("start provider child Task: %v", err)
	}
	if child.Status != domaintask.StatusRunning || child.ParentTaskID != root.TaskID {
		t.Fatalf("child=%+v; want running child of root %s", child, root.TaskID)
	}
	restored, err := restoreRunningWorkTask(ctx, owner, "mio")
	if err != nil || !restored {
		t.Fatalf("restore root Task=(%t,%v), want successful recovery", restored, err)
	}
	current, err := owner.SnapshotCurrentActivities(ctx, []string{"mio"})
	if err != nil || current["mio"].Task.TaskID != root.TaskID || current["mio"].Task.TaskID == child.TaskID {
		t.Fatalf("recovered current=%+v err=%v; want active root %s", current["mio"], err, root.TaskID)
	}
}

func TestLivingHeartbeatHidesPrivateTypedTaskWithRealJSONLStore(t *testing.T) {
	ctx := context.Background()
	root, err := os.MkdirTemp(".", ".heartbeat-private-activity-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	store, err := taskpersistence.NewJSONLStoreWithOptions(root, taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("close heartbeat test store: %v", err)
		}
	}()
	owner := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	if err := initializeAgentCurrentActivities(ctx, owner, "mio", nil, nil); err != nil {
		t.Fatalf("initialize current activities: %v", err)
	}
	timing := modulecore.TaskTiming{AllowedWindows: []modulecore.TaskTimeWindow{}}
	timingDigest, err := domaintask.TaskTimingSHA256(timing)
	if err != nil {
		t.Fatal(err)
	}
	kindInput := json.RawMessage(`{"information":"private data","kind":"movie","limit":1,"name":"Secret private movie title"}`)
	inputDigest, err := domaintask.TaskCenterKindInputSHA256(kindInput)
	if err != nil {
		t.Fatal(err)
	}
	goalID, workstreamID := modulecore.NewGoalID(), modulecore.NewWorkstreamID()
	draft := domaintask.Task{
		Title: "Secret private movie title", Route: domaintask.RouteGeneral, OwnerID: "mio", Assignee: "mio",
		Priority: domaintask.PriorityNormal, ReadOnly: true, GoalID: goalID, WorkstreamID: workstreamID, Timing: &timing,
		PrivateAccess: &domaintask.TaskPrivateAccess{Scope: domaintask.TaskAccessScopeUser, AuthenticatedUserID: "user-1"},
		TaskCenterExecution: &domaintask.TaskCenterExecution{
			KindID: taskmanager.TaskCenterMovieCatalogLookupKind, KindInputVersion: 1, KindInputSHA256: inputDigest,
			SourceGoalID: goalID, PreparedRevision: 1, TimingSHA256: timingDigest,
		},
	}
	task, _, err := owner.EnsureTaskCenterExecution(ctx, modulecore.NewRequestID(), 1, draft,
		domaintask.SharedRoleContext{UserIntent: "private task context"}, kindInput,
		func(context.Context, domaintask.Task, domaintask.SharedRoleContext, json.RawMessage) error {
			return nil
		},
		func(context.Context, domaintask.TaskCenterExecutionReceiptReadInput) error { return nil })
	if err != nil {
		t.Fatalf("create private typed current Task: %v", err)
	}
	if _, err := store.GetTask(ctx, task.TaskID); err == nil {
		t.Fatal("generic JSONL Task reader exposed private typed Task")
	}
	activities := runtimeLivingTaskProjectionReader{tasks: owner}
	handler := viewer.HandleLivingHeartbeatWithActivities(store, func(context.Context, domaintask.Task) (bool, error) {
		return true, nil
	}, activities)
	request := httptest.NewRequest("GET", "http://127.0.0.1:18790/viewer/living/heartbeat", nil)
	request.RemoteAddr = "127.0.0.1:49152"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("heartbeat response=%d body=%s; want 200 with curated descriptor", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "Secret private movie title") || !strings.Contains(response.Body.String(), "作業中") {
		t.Fatalf("heartbeat body=%s; want curated work label without private title", response.Body.String())
	}
}

func TestConfirmedOffLeaseGameResumesAfterForegroundTaskCompletes(t *testing.T) {
	ctx := context.Background()
	owner, closeStore := newCurrentActivityTestOwner(t)
	defer closeStore()
	if err := initializeAgentCurrentActivities(ctx, owner, "mio", nil, nil); err != nil {
		t.Fatalf("initialize current activities: %v", err)
	}
	sessionReader := gameObserverSessionsReaderFunc(func(context.Context) ([]gameObserverActiveAgentSession, error) {
		return []gameObserverActiveAgentSession{{GameID: "nethack", SessionID: "session-resume", Personas: []string{"mio"}, Status: "running"}}, nil
	})
	participants := []domaintask.ActivityParticipant{{ActorID: "mio", Role: "player"}}
	game, err := owner.EnsureExternalGameActivity(ctx, "mio", "nethack", "session-resume", "playing", participants)
	if err != nil {
		t.Fatalf("adopt confirmed GAME session: %v", err)
	}
	foreground, run, err := owner.CreateAndStartActivityRun(ctx, domaintask.Task{
		Title: "private foreground work", ModuleID: "runtime-test", OwnerID: "mio", Assignee: "mio",
		Route: domaintask.RouteGeneral,
	}, domaintask.ActivityMetadata{
		Kind: domaintask.ActivityKindWork, Label: "Work", Phase: "working",
		Participants: []domaintask.ActivityParticipant{{ActorID: "mio"}},
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("start foreground Task: %v", err)
	}
	if _, err := owner.CompleteRun(ctx, foreground.TaskID, run.RunID, "mio", domaintask.StatusSucceeded, "done", ""); err != nil {
		t.Fatalf("complete foreground Task: %v", err)
	}
	deps := &Dependencies{
		taskManager: owner, localAgentOpsOwnerID: "mio",
		gameTaskAdmission: newGameTaskRuntimeAdmission(sessionReader, owner),
	}
	work, found, err := externalGameResumeIdleSource(nil, deps)(ctx, "mio")
	if err != nil || !found || work.Run == nil {
		t.Fatalf("CHAT completion idle handoff=(%+v,%t,%v), want selected resume Work", work, found, err)
	}
	before, err := owner.SnapshotCurrentActivities(ctx, []string{"mio"})
	if err != nil || before["mio"].Task.TaskID == game.TaskID {
		t.Fatalf("GAME discovery performed a handoff: snapshot=%+v err=%v", before, err)
	}
	if err := work.Run(ctx, nil); err != nil {
		t.Fatalf("run confirmed GAME resume Work: %v", err)
	}
	current, err := owner.SnapshotCurrentActivities(ctx, []string{"mio"})
	if err != nil {
		t.Fatalf("read resumed current activity: %v", err)
	}
	if current["mio"].Task.TaskID != game.TaskID || current["mio"].Task.Activity.Game == nil ||
		current["mio"].Task.Activity.Game.SessionID != "session-resume" {
		t.Fatalf("resumed current activity=%+v; want original confirmed GAME Task %s", current["mio"], game.TaskID)
	}
}

func TestConfirmedTerminalExternalGameExpiresThroughSchedulerWork(t *testing.T) {
	ctx := context.Background()
	owner, closeStore := newCurrentActivityTestOwner(t)
	defer closeStore()
	if err := initializeAgentCurrentActivities(ctx, owner, "mio", nil, nil); err != nil {
		t.Fatalf("initialize current activities: %v", err)
	}
	game, err := owner.EnsureExternalGameActivity(ctx, "mio", "nethack", "session-ended", "playing",
		[]domaintask.ActivityParticipant{{ActorID: "mio", Role: "player"}})
	if err != nil {
		t.Fatalf("adopt confirmed GAME session: %v", err)
	}
	reader := currentActivityGameReader{summary: gameObserverSessionSummary{
		GameID: "nethack", SessionID: "session-ended", Persona: "mio", Status: "completed",
	}}
	deps := &Dependencies{taskManager: owner, gameTaskAdmission: newGameTaskRuntimeAdmission(reader, owner)}
	work, found, err := externalGameResumeIdleSource(nil, deps)(ctx, "mio")
	if err != nil || !found || work.Run == nil {
		t.Fatalf("terminal GAME discovery=(%+v,%t,%v), want reserved terminal Work", work, found, err)
	}
	before, err := owner.SnapshotCurrentActivities(ctx, []string{"mio"})
	if err != nil || before["mio"].Task.TaskID != game.TaskID {
		t.Fatalf("terminal GAME discovery mutated current binding: snapshot=%+v err=%v", before, err)
	}
	if err := work.Run(ctx, nil); err != nil {
		t.Fatalf("complete observed terminal GAME: %v", err)
	}
	after, err := owner.SnapshotCurrentActivities(ctx, []string{"mio"})
	if err != nil || after["mio"].Task.TaskID == game.TaskID || after["mio"].Task.Activity == nil ||
		after["mio"].Task.Activity.Kind != domaintask.ActivityKindEvaluating {
		t.Fatalf("terminal GAME successor=%+v err=%v; want evaluating Task", after["mio"], err)
	}
	closed, err := owner.Get(ctx, game.TaskID)
	if err != nil || closed.Status != domaintask.StatusSucceeded {
		t.Fatalf("terminal GAME Task=%+v err=%v; want succeeded", closed, err)
	}
}

func TestStartupReconcilesOnlyObserverConfirmedTerminalGame(t *testing.T) {
	ctx := context.Background()
	owner, closeStore := newCurrentActivityTestOwner(t)
	defer closeStore()
	if err := initializeAgentCurrentActivities(ctx, owner, "mio", nil, nil); err != nil {
		t.Fatalf("initialize current activities: %v", err)
	}
	game, err := owner.EnsureExternalGameActivity(ctx, "mio", "nethack", "session-startup-ended", "playing",
		[]domaintask.ActivityParticipant{{ActorID: "mio", Role: "player"}})
	if err != nil {
		t.Fatalf("adopt confirmed GAME session: %v", err)
	}
	reader := currentActivityGameReader{summary: gameObserverSessionSummary{
		GameID: "nethack", SessionID: "session-startup-ended", Persona: "mio", Personas: []string{"mio"}, Status: "failed",
	}}
	if err := reconcileTerminalGameActivitiesAtStartup(ctx, owner, reader); err != nil {
		t.Fatalf("reconcile terminal GAME during startup: %v", err)
	}
	closed, err := owner.Get(ctx, game.TaskID)
	if err != nil || closed.Status != domaintask.StatusFailed {
		t.Fatalf("terminal GAME Task=%+v err=%v; want failed", closed, err)
	}
	current, err := owner.SnapshotCurrentActivities(ctx, []string{"mio"})
	if err != nil || current["mio"].Task.TaskID == game.TaskID || current["mio"].Task.Activity == nil ||
		current["mio"].Task.Activity.Kind != domaintask.ActivityKindEvaluating {
		t.Fatalf("startup GAME successor=%+v err=%v; want evaluating Task", current["mio"], err)
	}
}

func TestPassiveActivityDeadlineRunsOnlyAfterSchedulerReservationAndUsesCAS(t *testing.T) {
	ctx := context.Background()
	owner, closeStore := newCurrentActivityTestOwner(t)
	defer closeStore()
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(-time.Minute)
	timing := modulecore.TaskTiming{Timezone: "UTC", DeadlineAt: &deadline}
	activity := activityMetadataForActor(domaintask.ActivityKindRest, "Rest", "resting", true, "mio", "")
	rest, err := owner.CreateCurrentActivity(ctx, domaintask.Task{
		Title: "Agent activity: Rest", ModuleID: agentActivityModuleID, OwnerID: "mio", Assignee: "mio",
		Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow, InterruptPolicy: domaintask.InterruptSilent,
		ReadOnly: true, Timing: &timing,
	}, activity)
	if err != nil {
		t.Fatalf("create expired passive rest Task: %v", err)
	}
	runtime := &agentRoutineRuntime{tasks: owner, ownerID: "mio", now: func() time.Time { return now }}
	work, found, err := runtime.expiredPassiveActivityWork(ctx, "mio", now)
	if err != nil || !found {
		t.Fatalf("expiry discovery=(%+v,%t,%v), want reserved expiry work", work, found, err)
	}
	before, err := owner.SnapshotCurrentActivities(ctx, []string{"mio"})
	if err != nil || before["mio"].Task.TaskID != rest.TaskID || before["mio"].Task.Status != domaintask.StatusRunning {
		t.Fatalf("discovery mutated current Task: snapshot=%+v err=%v", before, err)
	}
	if err := work.Run(ctx, nil); err != nil {
		t.Fatalf("run reserved expiry work: %v", err)
	}
	after, err := owner.SnapshotCurrentActivities(ctx, []string{"mio"})
	if err != nil {
		t.Fatalf("snapshot after expiry: %v", err)
	}
	if after["mio"].Task.TaskID == rest.TaskID || after["mio"].Task.Activity == nil ||
		after["mio"].Task.Activity.Kind != domaintask.ActivityKindEvaluating || !after["mio"].Task.Activity.Passive {
		t.Fatalf("expired rest successor=%+v, want a passive evaluating Task", after["mio"])
	}
	closed, err := owner.Get(ctx, rest.TaskID)
	if err != nil || closed.Status != domaintask.StatusSucceeded {
		t.Fatalf("expired rest Task=%+v err=%v, want succeeded", closed, err)
	}
}

func TestPassiveActivityExpiryDoesNotOverwriteConcurrentForegroundTask(t *testing.T) {
	ctx := context.Background()
	owner, closeStore := newCurrentActivityTestOwner(t)
	defer closeStore()
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(-time.Minute)
	timing := modulecore.TaskTiming{Timezone: "UTC", DeadlineAt: &deadline}
	rest, err := owner.CreateCurrentActivity(ctx, domaintask.Task{
		Title: "Agent activity: Rest", ModuleID: agentActivityModuleID, OwnerID: "mio", Assignee: "mio",
		Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow, InterruptPolicy: domaintask.InterruptSilent,
		ReadOnly: true, Timing: &timing,
	}, activityMetadataForActor(domaintask.ActivityKindRest, "Rest", "resting", true, "mio", ""))
	if err != nil {
		t.Fatal(err)
	}
	runtime := &agentRoutineRuntime{tasks: owner, ownerID: "mio", now: func() time.Time { return now }}
	work, found, err := runtime.expiredPassiveActivityWork(ctx, "mio", now)
	if err != nil || !found {
		t.Fatalf("expiry discovery=(%+v,%t,%v)", work, found, err)
	}
	draft := domaintask.Task{
		TaskID: modulecore.NewTaskID(), Title: "Private task title", ModuleID: "test.foreground", OwnerID: "mio",
		Assignee: "mio", Route: domaintask.RouteCHAT, Priority: domaintask.PriorityHigh,
		InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked,
	}
	foreground, _, err := owner.CreateAndStartActivityRun(ctx, draft,
		activityMetadataForActor(domaintask.ActivityKindChat, "private chat title", "talking", false, "mio", ""),
		domaintask.SharedRoleContext{TaskID: draft.TaskID, UserIntent: "test", CurrentPlan: "test"})
	if err != nil {
		t.Fatalf("start concurrent foreground Task: %v", err)
	}
	if err := work.Run(ctx, nil); err == nil {
		t.Fatal("stale expiry Work succeeded after foreground handoff; want CAS conflict")
	}
	current, err := owner.SnapshotCurrentActivities(ctx, []string{"mio"})
	if err != nil || current["mio"].Task.TaskID != foreground.TaskID {
		t.Fatalf("concurrent current Task=%+v err=%v, want foreground %s", current["mio"], err, foreground.TaskID)
	}
	if closed, err := owner.Get(ctx, rest.TaskID); err != nil || closed.Status != domaintask.StatusCancelled {
		t.Fatalf("interrupted rest Task=%+v err=%v, want cancelled", closed, err)
	}
}

func newCurrentActivityTestOwner(t *testing.T) (*taskmanager.Manager, func()) {
	t.Helper()
	root, err := os.MkdirTemp(".", ".current-activity-tests-")
	if err != nil {
		t.Fatal(err)
	}
	store, err := taskpersistence.NewJSONLStoreWithOptions(root, taskpersistence.OpenOptions{Index: true})
	if err != nil {
		_ = os.RemoveAll(root)
		t.Fatal(err)
	}
	return taskmanager.New(store, taskmanager.DefaultParallelLimits()), func() {
		if err := store.Close(); err != nil {
			t.Errorf("close current activity store: %v", err)
		}
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove current activity store: %v", err)
		}
	}
}
