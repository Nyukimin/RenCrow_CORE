package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestAgentRoutineCapacityRetryUsesHeartbeatAndCompletesSameTaskIDs(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 14, 7, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	runtime, tasks := newRoutineAdmissionRuntime(t, taskStore, &clock)

	blockers := []routineAdmissionRun{
		startRoutineAdmissionRun(t, tasks, "capacity-blocker-a", "blocker-a", modulecore.NewTaskID()),
		startRoutineAdmissionRun(t, tasks, "capacity-blocker-b", "blocker-b", modulecore.NewTaskID()),
	}
	progress := routineProgress{
		Actors: []routineActorProgress{
			{ActorID: "mio", TaskID: modulecore.NewTaskID()},
			{ActorID: "shiro", TaskID: modulecore.NewTaskID()},
		},
		EffectCompleted: true,
		CompletedAt:     clock.UTC(),
	}
	state := draftRoutineAdmissionMorningIntent(t, runtime, progress, clock)
	firstTaskID := progress.Actors[0].TaskID
	secondTaskID := progress.Actors[1].TaskID
	planned := agentroutine.Plan{Kind: agentroutine.ActivityMorningChat, Participants: []string{"shiro"}}

	firstErr := runtime.run(ctx, nil, "mio", planned)
	if !errors.Is(firstErr, taskmanager.ErrParallelLimit) {
		t.Fatalf("first routine attempt error = %v, want preserved ErrParallelLimit", firstErr)
	}
	if errors.Is(firstErr, taskmanager.ErrStartedRunNotClosed) {
		t.Fatalf("first routine attempt error = %v, want the partial Run already closed", firstErr)
	}
	state, err = runtime.store.Load(ctx, "mio")
	if err != nil {
		t.Fatalf("load deferred routine state: %v", err)
	}
	if state.Pending == nil || !state.Pending.RetryAfter.Equal(clock.Add(agentheartbeat.Interval)) {
		t.Fatalf("capacity retry = %v, want one heartbeat after %v", state.Pending, clock)
	}
	firstTask, err := tasks.Get(ctx, firstTaskID)
	if err != nil || firstTask.Status != domaintask.StatusWaiting {
		t.Fatalf("first actor Task after capacity refusal = %+v err=%v, want same Task waiting", firstTask, err)
	}
	firstRuns, err := tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: firstTaskID})
	if err != nil || len(firstRuns) != 1 || firstRuns[0].Status != domaintask.RunStatusWaiting {
		t.Fatalf("first actor Runs after capacity refusal = %+v err=%v, want its started Run closed as waiting", firstRuns, err)
	}
	if _, err := tasks.Get(ctx, secondTaskID); !errors.Is(err, taskmanager.ErrNotFound) {
		t.Fatalf("second actor Task after refused admission error = %v, want no persisted Task", err)
	}

	for _, blocker := range blockers {
		if _, err := tasks.CompleteRun(ctx, blocker.task.TaskID, blocker.run.RunID, blocker.task.Assignee,
			domaintask.StatusSucceeded, "test capacity released", ""); err != nil {
			t.Fatalf("release global capacity for %s: %v", blocker.task.Assignee, err)
		}
	}
	clock = clock.Add(agentheartbeat.Interval)
	if err := runtime.run(ctx, nil, "mio", planned); err != nil {
		t.Fatalf("retry same routine reservation after one heartbeat: %v", err)
	}

	state, err = runtime.store.Load(ctx, "mio")
	if err != nil {
		t.Fatalf("load completed routine state: %v", err)
	}
	if state.Pending != nil || state.LastFinishedIntentID == "" {
		t.Fatalf("routine reservation after retry = pending=%+v finished=%q, want completed owned reservation", state.Pending, state.LastFinishedIntentID)
	}
	for _, actor := range progress.Actors {
		task, err := tasks.Get(ctx, actor.TaskID)
		if err != nil || task.Status != domaintask.StatusSucceeded {
			t.Fatalf("actor %s Task after retry = %+v err=%v, want the same TaskID succeeded", actor.ActorID, task, err)
		}
		runs, err := tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: actor.TaskID})
		if err != nil || len(runs) == 0 || runs[len(runs)-1].Status != domaintask.RunStatusSucceeded {
			t.Fatalf("actor %s Runs after retry = %+v err=%v, want completed latest Run", actor.ActorID, runs, err)
		}
	}
	firstRuns, err = tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: firstTaskID})
	if err != nil || len(firstRuns) != 2 || firstRuns[0].RunID == firstRuns[1].RunID || firstRuns[0].Status != domaintask.RunStatusWaiting {
		t.Fatalf("first actor Run recovery = %+v err=%v, want the same TaskID with a fresh successor Run", firstRuns, err)
	}
}

func TestAgentRoutineCapacityRetryKeepsNormalDelayWhenDeferralPauseFails(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 14, 7, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	baseStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := baseStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	fault := &routineAdmissionPauseFault{}
	taskStore := &routineAdmissionFaultStore{Store: baseStore, fault: fault}
	runtime, tasks := newRoutineAdmissionRuntime(t, taskStore, &clock)

	blockers := []routineAdmissionRun{
		startRoutineAdmissionRun(t, tasks, "capacity-blocker-a", "blocker-a", modulecore.NewTaskID()),
		startRoutineAdmissionRun(t, tasks, "capacity-blocker-b", "blocker-b", modulecore.NewTaskID()),
	}
	progress := routineProgress{
		Actors: []routineActorProgress{
			{ActorID: "mio", TaskID: modulecore.NewTaskID()},
			{ActorID: "shiro", TaskID: modulecore.NewTaskID()},
		},
		EffectCompleted: true,
		CompletedAt:     clock.UTC(),
	}
	state := draftRoutineAdmissionMorningIntent(t, runtime, progress, clock)
	activeRoutine := startRoutineAdmissionRun(t, tasks, agentRoutineModuleID, "shiro", progress.Actors[1].TaskID)
	fault.failNextWaitingRun = true

	err = runtime.run(ctx, nil, "mio", agentroutine.Plan{Kind: agentroutine.ActivityMorningChat, Participants: []string{"shiro"}})
	if !errors.Is(err, taskmanager.ErrParallelLimit) || !errors.Is(err, taskmanager.ErrStartedRunNotClosed) {
		t.Fatalf("capacity attempt with deferral pause failure = %v, want both typed errors", err)
	}
	state, err = runtime.store.Load(ctx, "mio")
	if err != nil {
		t.Fatalf("load state after deferral pause failure: %v", err)
	}
	if state.Pending == nil || !state.Pending.RetryAfter.Equal(clock.Add(15*time.Minute)) {
		t.Fatalf("retry after failed Run closure = %v, want normal 15m delay", state.Pending)
	}
	currentRuns, err := tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: activeRoutine.task.TaskID})
	if err != nil || len(currentRuns) != 1 || currentRuns[0].Status != domaintask.RunStatusRunning {
		t.Fatalf("Run after injected pause failure = %+v err=%v, want original Run still running", currentRuns, err)
	}

	for _, active := range append(blockers, activeRoutine) {
		if _, err := tasks.CompleteRun(ctx, active.task.TaskID, active.run.RunID, active.task.Assignee,
			domaintask.StatusFailed, "test cleanup", ""); err != nil {
			t.Errorf("close active test Run for %s: %v", active.task.Assignee, err)
		}
	}
}

func newRoutineAdmissionRuntime(t *testing.T, taskStore taskmanager.Store, clock *time.Time) (*agentRoutineRuntime, *taskmanager.Manager) {
	t.Helper()
	stateStore, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stateStore.Close(); err != nil {
			t.Errorf("close routine state: %v", err)
		}
	})
	tasks := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	runtime := &agentRoutineRuntime{
		store:   stateStore,
		tasks:   tasks,
		ownerID: "ren",
		now:     func() time.Time { return *clock },
	}
	return runtime, tasks
}

func draftRoutineAdmissionMorningIntent(t *testing.T, runtime *agentRoutineRuntime, progress routineProgress, now time.Time) agentroutine.State {
	t.Helper()
	encoded, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	state, err := runtime.store.DraftIntent(context.Background(), agentroutine.Intent{
		ID: string(modulecore.NewRequestID()), ActorID: "mio", Date: now.In(runtime.location()).Format("2006-01-02"),
		TaskID: string(progress.Actors[0].TaskID), Kind: agentroutine.ActivityMorningChat,
		Label: "morning_chat", Participants: []string{"shiro"}, Step: "effect_complete", Output: string(encoded), CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("draft routine admission intent: %v", err)
	}
	return state
}

type routineAdmissionRun struct {
	task domaintask.Task
	run  domaintask.Run
}

func startRoutineAdmissionRun(t *testing.T, tasks *taskmanager.Manager, moduleID, actorID string, taskID modulecore.TaskID) routineAdmissionRun {
	t.Helper()
	task, run, err := tasks.CreateAndStartRun(context.Background(), domaintask.Task{
		TaskID: taskID, Title: "routine admission capacity fixture", ModuleID: moduleID, OwnerID: "ren", Assignee: actorID,
		Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow, InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true,
	}, domaintask.SharedRoleContext{TaskID: taskID, UserIntent: "routine admission test", CurrentPlan: "capacity retry"})
	if err != nil {
		t.Fatalf("start active Task for %s: %v", actorID, err)
	}
	return routineAdmissionRun{task: task, run: run}
}

type routineAdmissionPauseFault struct {
	failNextWaitingRun bool
}

type routineAdmissionFaultStore struct {
	domaintask.Store
	fault *routineAdmissionPauseFault
}

func (s *routineAdmissionFaultStore) TaskTransaction(ctx context.Context, taskID modulecore.TaskID, fn func(domaintask.Store) error) error {
	return s.Store.TaskTransaction(ctx, taskID, func(store domaintask.Store) error {
		return fn(&routineAdmissionFaultStore{Store: store, fault: s.fault})
	})
}

func (s *routineAdmissionFaultStore) SaveRun(ctx context.Context, run domaintask.Run) error {
	if s.fault.failNextWaitingRun && run.Status == domaintask.RunStatusWaiting {
		s.fault.failNextWaitingRun = false
		return errors.New("injected routine Run pause write failure")
	}
	return s.Store.SaveRun(ctx, run)
}
