package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestAgentRoutineActivationPersistsAllActorsAndDiscoversPriorDiaryWithoutWORLD(t *testing.T) {
	cfg := &config.Config{
		WorkspaceDir:  filepath.Join(t.TempDir(), "workspace"),
		LocalAgentOps: config.LocalAgentOpsConfig{UserID: "ren"},
		Heartbeat:     config.HeartbeatConfig{AgentSchedulerEnabled: true, Routine: config.RoutineHeartbeatConfig{Enabled: true}},
	}
	runtime, err := newAgentRoutineRuntime(cfg, &Dependencies{taskManager: taskmanager.New(nil, taskmanager.DefaultParallelLimits())}, nil, nil)
	if err != nil {
		t.Fatalf("newAgentRoutineRuntime: %v", err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("close routine store: %v", err)
		}
	})
	if runtime.world != nil {
		t.Fatal("WORLD reader unexpectedly configured")
	}
	for _, actorID := range agentHeartbeatActorIDs {
		state, err := runtime.store.Load(context.Background(), actorID)
		if err != nil {
			t.Fatalf("Load(%s): %v", actorID, err)
		}
		if state.ActivatedOn == "" || state.NextDiaryDate != state.ActivatedOn {
			t.Errorf("actor %s activation state = activated=%q next_diary=%q, want durable activation for each actor", actorID, state.ActivatedOn, state.NextDiaryDate)
		}
	}

	actorID := "mio"
	state, err := runtime.store.Load(context.Background(), actorID)
	if err != nil {
		t.Fatal(err)
	}
	state.ActivatedOn = "2026-11-12"
	state.NextDiaryDate = "2026-11-12"
	state, err = runtime.store.Save(context.Background(), state, state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	runtime.now = func() time.Time { return time.Date(2026, time.November, 13, 10, 30, 0, 0, runtime.location()) }
	plan, _, err := runtime.planFresh(context.Background(), actorID, false, nil)
	if err != nil {
		t.Fatalf("planFresh without WORLD: %v", err)
	}
	if plan.Kind != agentroutine.ActivityDiary || plan.Date != "2026-11-12" {
		t.Fatalf("planFresh without WORLD = kind=%s date=%s, want prior-day diary", plan.Kind, plan.Date)
	}
	stored, err := runtime.store.Load(context.Background(), actorID)
	if err != nil {
		t.Fatal(err)
	}
	revision := stored.Revision
	if _, _, err := runtime.loadDecisionState(context.Background(), actorID); err != nil {
		t.Fatalf("read-only discovery state: %v", err)
	}
	stored, err = runtime.store.Load(context.Background(), actorID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Revision != revision {
		t.Fatalf("discovery changed durable revision from %d to %d", revision, stored.Revision)
	}
}

func TestAgentRoutineCanonicalRunsPauseResumeAndRecoverAfterRestart(t *testing.T) {
	ctx := context.Background()
	routineStore, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := routineStore.Close(); err != nil {
			t.Errorf("close routine state: %v", err)
		}
	})
	taskRoot := filepath.Join(t.TempDir(), "tasks")
	taskStore, err := taskpersistence.NewJSONLStore(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	taskStoreClosed := false
	t.Cleanup(func() {
		if !taskStoreClosed {
			if err := taskStore.Close(); err != nil {
				t.Errorf("close initial task store: %v", err)
			}
		}
	})
	runtime := &agentRoutineRuntime{store: routineStore, tasks: taskmanager.New(taskStore, taskmanager.DefaultParallelLimits()), ownerID: "ren"}
	progress := routineProgress{Actors: []routineActorProgress{
		{ActorID: "mio", TaskID: modulecore.NewTaskID()},
		{ActorID: "shiro", TaskID: modulecore.NewTaskID()},
	}}
	encoded, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.November, 13, 7, 0, 0, 0, time.UTC)
	state, err := routineStore.DraftIntent(ctx, agentroutine.Intent{
		ID: string(modulecore.NewRequestID()), ActorID: "mio", Date: "2026-11-13", TaskID: string(progress.Actors[0].TaskID),
		Kind: agentroutine.ActivityMorningChat, Label: "morning_chat", Participants: []string{"shiro"},
		Step: "planned", Output: string(encoded), CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("DraftIntent: %v", err)
	}

	first, state, progress, err := runtime.prepareRuns(ctx, nil, state, progress)
	if err != nil {
		t.Fatalf("prepare first Run generation: %v", err)
	}
	progress.Actors[0].Output = "first actor reply saved before stop"
	state, progress, err = runtime.saveProgress(ctx, state, "first_reply_saved", progress)
	if err != nil {
		t.Fatalf("save first reply before stop: %v", err)
	}
	if err := runtime.pauseRuns(ctx, first, "test interruption"); err != nil {
		t.Fatalf("pause first Run generation: %v", err)
	}
	second, state, progress, err := runtime.prepareRuns(ctx, nil, state, progress)
	if err != nil {
		t.Fatalf("resume second Run generation: %v", err)
	}
	if second[0].run.RunID == first[0].run.RunID || second[1].run.RunID == first[1].run.RunID {
		t.Fatal("checkpoint resume reused an already closed RunID")
	}
	if progress.Actors[0].Output != "first actor reply saved before stop" {
		t.Fatalf("first actor reply after resume = %q, want durable prior reply", progress.Actors[0].Output)
	}

	if err := taskStore.Close(); err != nil {
		t.Fatal(err)
	}
	taskStoreClosed = true
	restartedStore, err := taskpersistence.NewJSONLStore(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := restartedStore.Close(); err != nil {
			t.Errorf("close restarted task store: %v", err)
		}
	})
	runtime.tasks = taskmanager.New(restartedStore, taskmanager.DefaultParallelLimits())
	third, state, progress, err := runtime.prepareRuns(ctx, nil, state, progress)
	if err != nil {
		t.Fatalf("recover stale second Run generation after restart: %v", err)
	}
	if third[0].run.RunID == second[0].run.RunID || third[1].run.RunID == second[1].run.RunID {
		t.Fatal("process restart recovery did not create a successor Run")
	}
	if progress.Actors[0].Output != "first actor reply saved before stop" {
		t.Fatalf("first actor reply after process restart = %q, want durable prior reply", progress.Actors[0].Output)
	}
	for _, item := range third {
		if item.task.OwnerID != "ren" || item.task.ModuleID != agentRoutineModuleID || item.run.Status != domaintask.RunStatusRunning {
			t.Errorf("recovered canonical activity = task owner=%q module=%q run=%s, want exact owner/module and running successor", item.task.OwnerID, item.task.ModuleID, item.run.Status)
		}
	}
}

func TestAgentRoutineExpiresPausedRunsAndPreservesTerminalSuccess(t *testing.T) {
	ctx := context.Background()
	routineStore, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := routineStore.Close(); err != nil {
			t.Errorf("close routine state: %v", err)
		}
	})
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close task store: %v", err)
		}
	})
	runtime := &agentRoutineRuntime{
		store: routineStore, tasks: taskmanager.New(taskStore, taskmanager.DefaultParallelLimits()), ownerID: "ren",
		now: func() time.Time { return time.Date(2026, time.November, 14, 10, 0, 0, 0, time.UTC) },
	}
	progress := routineProgress{Actors: []routineActorProgress{
		{ActorID: "mio", TaskID: modulecore.NewTaskID()},
		{ActorID: "shiro", TaskID: modulecore.NewTaskID()},
	}}
	encoded, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	state, err := routineStore.DraftIntent(ctx, agentroutine.Intent{
		ID: string(modulecore.NewRequestID()), ActorID: "mio", Date: "2026-11-13", TaskID: string(progress.Actors[0].TaskID),
		Kind: agentroutine.ActivityMorningChat, Label: "morning_chat", Participants: []string{"shiro"},
		Step: "planned", Output: string(encoded), CreatedAt: time.Date(2026, time.November, 13, 7, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("DraftIntent: %v", err)
	}
	prepared, state, _, err := runtime.prepareRuns(ctx, nil, state, progress)
	if err != nil {
		t.Fatalf("prepare canonical runs: %v", err)
	}
	if _, err := runtime.tasks.CompleteRun(ctx, prepared[0].task.TaskID, prepared[0].run.RunID, "mio",
		domaintask.StatusSucceeded, "completed", ""); err != nil {
		t.Fatalf("complete successful actor Task: %v", err)
	}
	if err := runtime.pauseRuns(ctx, prepared[1:], "routine stopped"); err != nil {
		t.Fatalf("pause second actor Run: %v", err)
	}
	if err := runtime.cancelStaleIntent(ctx, state); err != nil {
		t.Fatalf("cancel expired routine intent: %v", err)
	}
	state, err = routineStore.Load(ctx, "mio")
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending != nil {
		t.Fatalf("expired intent still pending: %+v", state.Pending)
	}
	firstTask, err := runtime.tasks.Get(ctx, prepared[0].task.TaskID)
	if err != nil || firstTask.Status != domaintask.StatusSucceeded {
		t.Fatalf("terminal successful Task=%+v err=%v, want success preserved", firstTask, err)
	}
	waitingTask, err := runtime.tasks.Get(ctx, prepared[1].task.TaskID)
	if err != nil || waitingTask.Status != domaintask.StatusCancelled {
		t.Fatalf("expired paused Task=%+v err=%v, want cancelled", waitingTask, err)
	}
	waitingRuns, err := runtime.tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: prepared[1].task.TaskID})
	if err != nil || len(waitingRuns) != 1 || waitingRuns[0].Status != domaintask.RunStatusWaiting {
		t.Fatalf("expired Task runs=%+v err=%v, want prior checkpoint Run retained without an open Task", waitingRuns, err)
	}
}

func TestAgentRoutineHobbyFactUsesSafeJapaneseTopicAndKeepsOutputPrivate(t *testing.T) {
	store, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close routine store: %v", err)
		}
	})
	runtime := &agentRoutineRuntime{store: store}
	loc, err := time.LoadLocation(agentroutine.JST)
	if err != nil {
		t.Fatal(err)
	}
	intent := agentroutine.Intent{
		ID: string(modulecore.NewRequestID()), ActorID: "mio", Date: "2026-11-13",
		Kind: agentroutine.ActivityHobby, Label: "fashion",
	}
	progress := routineProgress{
		SearchResult: `[{"url":"https://example.invalid/private-url-sentinel"}]`,
		HobbyText:    "private-note-sentinel",
	}
	completedAt := time.Date(2026, time.November, 13, 10, 30, 0, 0, loc)
	if err := runtime.recordRoutineFacts(context.Background(), intent, progress, completedAt); err != nil {
		t.Fatalf("recordRoutineFacts: %v", err)
	}
	facts, err := store.ListFacts(context.Background(), "mio", "2026-11-13")
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("saved facts = %d, want one hobby fact", len(facts))
	}
	if !strings.Contains(facts[0].Summary, "ファッション") || strings.Contains(facts[0].Summary, "private-note-sentinel") || strings.Contains(facts[0].Summary, "private-url-sentinel") {
		t.Fatalf("public-safe fact summary = %q, want only the static Japanese hobby topic", facts[0].Summary)
	}
	if !strings.Contains(facts[0].Reference, "private-note-sentinel") || !strings.Contains(facts[0].Reference, "private-url-sentinel") {
		t.Fatalf("private reference = %q, want raw search references and generated text retained privately", facts[0].Reference)
	}
}
