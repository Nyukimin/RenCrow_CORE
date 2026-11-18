package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
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

func TestAgentRoutineNightActivityBackoffExpiresAtProjectedBedtime(t *testing.T) {
	ctx := context.Background()
	loc := time.FixedZone(agentroutine.JST, 9*60*60)
	sleepAt, _, err := agentroutine.ScheduleFor("midori", "2026-11-15")
	if err != nil {
		t.Fatal(err)
	}
	if sleepAt.Hour() != 1 || sleepAt.Minute() != 58 {
		t.Fatalf("Midori's projected sleep = %s, want fixture boundary 01:58", sleepAt.In(loc).Format("15:04"))
	}

	for _, kind := range []agentroutine.ActivityKind{agentroutine.ActivityHobby, agentroutine.ActivityRelax} {
		t.Run(string(kind), func(t *testing.T) {
			clock := sleepAt.Add(-time.Minute)
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
			if _, err := runtime.store.EnsureSchedule(ctx, "midori", "2026-11-14"); err != nil {
				t.Fatalf("seed prior-day schedule: %v", err)
			}
			progress := routineProgress{Actors: []routineActorProgress{{ActorID: "midori", TaskID: modulecore.NewTaskID()}}}
			state := draftRoutineBedtimeIntent(t, runtime, "midori", "2026-11-14", kind,
				time.Date(2026, time.November, 14, 23, 30, 0, 0, loc), sleepAt.Add(15*time.Minute), progress)
			originalIntentID := state.Pending.ID
			originalTaskID := progress.Actors[0].TaskID
			idle := runtime.idleSource(nil)

			if work, found, err := idle(ctx, "midori"); err != nil || found {
				t.Fatalf("before projected bedtime idle work = %+v found=%t err=%v, want no work", work, found, err)
			}
			before, err := runtime.store.Load(ctx, "midori")
			if err != nil || before.Pending == nil || before.Pending.ID != originalIntentID ||
				!before.Pending.RetryAfter.Equal(sleepAt.Add(15*time.Minute)) || before.ScheduleDate != "2026-11-14" ||
				!before.PlannedSleep.Before(sleepAt) {
				t.Fatalf("read-only pre-bedtime state = %+v err=%v, want the original intent and prior persisted schedule", before, err)
			}

			clock = sleepAt // 01:58 JST; the saved retry is still in the future.
			work, found, err := idle(ctx, "midori")
			if err != nil || !found {
				t.Fatalf("01:58 projected-bedtime discovery = %+v found=%t err=%v, want stale intent cleanup despite future retry", work, found, err)
			}
			if !strings.HasSuffix(work.ID, ":"+string(kind)) {
				t.Fatalf("bedtime cleanup Work ID = %q, want the pending %s kind", work.ID, kind)
			}
			if err := work.Run(ctx, nil); err != nil {
				t.Fatalf("run bedtime cleanup for %s: %v", kind, err)
			}
			after, err := runtime.store.Load(ctx, "midori")
			if err != nil || after.Pending != nil || after.LastFinishedIntentID != originalIntentID {
				t.Fatalf("expired %s intent after cleanup = %+v err=%v, want exact intent settled", kind, after, err)
			}
			if _, err := tasks.Get(ctx, originalTaskID); !errors.Is(err, taskmanager.ErrNotFound) {
				t.Fatalf("bedtime cleanup created or changed canonical Task %s: %v", originalTaskID, err)
			}
			clock = sleepAt.Add(7 * time.Minute) // 02:05 JST must not synthesize a late sleep Work.
			if work, found, err := idle(ctx, "midori"); err != nil || (found && strings.HasSuffix(work.ID, ":sleep")) {
				t.Fatalf("02:05 discovery = %+v found=%t err=%v, want no synthetic sleep Work after the 02:00 window", work, found, err)
			}
		})
	}
}

func TestAgentRoutineNightBackoffCompletesCheckpointedEffectOnce(t *testing.T) {
	ctx := context.Background()
	loc := time.FixedZone(agentroutine.JST, 9*60*60)
	sleepAt, _, err := agentroutine.ScheduleFor("midori", "2026-11-15")
	if err != nil {
		t.Fatal(err)
	}
	clock := sleepAt.Add(5 * time.Minute)
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
	progress := routineProgress{
		Actors:          []routineActorProgress{{ActorID: "midori", TaskID: modulecore.NewTaskID()}},
		SearchResult:    "saved source",
		HobbyText:       "saved hobby note",
		EffectCompleted: true,
		CompletedAt:     clock.Add(-time.Minute).UTC(),
	}
	state := draftRoutineBedtimeIntent(t, runtime, "midori", "2026-11-14", agentroutine.ActivityHobby,
		time.Date(2026, time.November, 14, 23, 30, 0, 0, loc), sleepAt.Add(15*time.Minute), progress)
	taskID, intentID := progress.Actors[0].TaskID, state.Pending.ID
	work, found, err := runtime.idleSource(nil)(ctx, "midori")
	if err != nil || !found {
		t.Fatalf("completed-effect bedtime discovery = %+v found=%t err=%v, want one completion Work", work, found, err)
	}
	if err := work.Run(ctx, nil); err != nil {
		t.Fatalf("finish completed hobby checkpoint at bedtime: %v", err)
	}
	if err := work.Run(ctx, nil); err != nil {
		t.Fatalf("repeat delivered bedtime Work: %v", err)
	}

	after, err := runtime.store.Load(ctx, "midori")
	if err != nil || after.Pending != nil || after.LastFinishedIntentID != intentID {
		t.Fatalf("completed hobby intent after repeat delivery = %+v err=%v", after, err)
	}
	task, err := tasks.Get(ctx, taskID)
	if err != nil || task.Status != domaintask.StatusSucceeded {
		t.Fatalf("canonical completed hobby Task = %+v err=%v, want succeeded", task, err)
	}
	runs, err := tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
	if err != nil || len(runs) != 1 || runs[0].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("canonical completed hobby Runs = %+v err=%v, want one successful Run", runs, err)
	}
	facts, err := runtime.store.ListFacts(ctx, "midori", progress.CompletedAt.In(loc).Format("2006-01-02"))
	if err != nil || len(facts) != 1 || facts[0].Key != intentID+":"+string(agentroutine.ActivityHobby) {
		t.Fatalf("completed hobby facts = %+v err=%v, want one idempotent fact for the existing intent", facts, err)
	}
}

func TestAgentRoutineNightBackoffPromotesExactSettledWorldHobby(t *testing.T) {
	ctx := context.Background()
	loc := time.FixedZone(agentroutine.JST, 9*60*60)
	sleepAt, _, err := agentroutine.ScheduleFor("shiro", "2026-11-15")
	if err != nil {
		t.Fatal(err)
	}
	clock := sleepAt.Add(5 * time.Minute)
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
	intentID := "intent-exercise-1"
	progress := routineProgress{
		Actors: []routineActorProgress{{ActorID: "shiro", TaskID: modulecore.NewTaskID()}},
		World: &routineWorldProgress{Operations: []routineWorldOperation{
			{
				Purpose: "exercise-start", Key: routineWorldOperationKey(intentID, "shiro", "exercise-start"),
				Action: worldread.HomeActionExercise, EventKind: "life.home.exercise.started", ActorID: "shiro",
				Payload:   worldread.HomeActionPayload{ActorID: "shiro"},
				ReceiptID: "receipt-exercise-start-1", ReceiptStatus: "committed", EventVerified: true,
				ActivityID: "exercise-activity-1", ActivityKind: "exercise", StartedAt: clock.Add(-5 * time.Minute).UTC(), DueAt: clock.Add(-2 * time.Minute).UTC(),
			},
			{
				Purpose: "interrupt-finish:exercise-activity-1", Key: routineWorldOperationKey(intentID, "shiro", "interrupt-finish:exercise-activity-1"),
				Action: worldread.HomeActionFinish, EventKind: "life.home.exercise.completed", ActorID: "shiro",
				Payload:   worldread.HomeActionPayload{ActorID: "shiro", ActivityID: "exercise-activity-1"},
				ReceiptID: "receipt-exercise-finish-1", ReceiptStatus: "committed", EventVerified: true,
				ActivityID: "exercise-activity-1", ActivityKind: "exercise", EndedAt: clock.Add(-time.Minute).UTC(),
			},
		}},
	}
	state := draftRoutineBedtimeIntent(t, runtime, "shiro", "2026-11-14", agentroutine.ActivityHobby,
		time.Date(2026, time.November, 14, 23, 30, 0, 0, loc), sleepAt.Add(15*time.Minute), progress, intentID)
	runtime.cfg = &config.Config{Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{
		PrivateRooms: map[string]string{"shiro": "shiro-bedroom"},
	}}}
	runtime.worldLife = &worldread.LifeClient{} // Verified WORLD lifecycle receipts own physical hobby facts.
	taskID, intentID := progress.Actors[0].TaskID, state.Pending.ID
	work, found, err := runtime.idleSource(nil)(ctx, "shiro")
	if err != nil || !found {
		t.Fatalf("settled WORLD hobby bedtime discovery = %+v found=%t err=%v, want one completion Work", work, found, err)
	}
	if err := work.Run(ctx, nil); err != nil {
		t.Fatalf("finish exact settled WORLD hobby at bedtime: %v", err)
	}
	after, err := runtime.store.Load(ctx, "shiro")
	if err != nil || after.Pending != nil || after.LastFinishedIntentID != intentID {
		t.Fatalf("settled WORLD hobby after bedtime = %+v err=%v", after, err)
	}
	task, err := tasks.Get(ctx, taskID)
	if err != nil || task.Status != domaintask.StatusSucceeded {
		t.Fatalf("exact settled WORLD hobby Task = %+v err=%v, want succeeded without replay", task, err)
	}
	facts, err := runtime.store.ListFacts(ctx, "shiro", clock.Add(-time.Minute).In(loc).Format("2006-01-02"))
	if err != nil || len(facts) != 0 {
		t.Fatalf("configured WORLD hobby CORE facts = %+v err=%v, want WORLD-owned lifecycle receipts without a duplicate Store fact", facts, err)
	}
}

func TestAgentRoutineBedtimeSettlementFailureKeepsExactPendingIntent(t *testing.T) {
	ctx := context.Background()
	loc := time.FixedZone(agentroutine.JST, 9*60*60)
	sleepAt, _, err := agentroutine.ScheduleFor("shiro", "2026-11-15")
	if err != nil {
		t.Fatal(err)
	}
	clock := sleepAt.Add(5 * time.Minute)
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
	intentID := "intent-exercise-active"
	progress := routineProgress{
		Actors: []routineActorProgress{{ActorID: "shiro", TaskID: modulecore.NewTaskID()}},
		World: &routineWorldProgress{Operations: []routineWorldOperation{{
			Purpose: "exercise-start", Key: routineWorldOperationKey(intentID, "shiro", "exercise-start"),
			Action: worldread.HomeActionExercise, EventKind: "life.home.exercise.started", ActorID: "shiro",
			ReceiptID: "receipt-exercise-start-active", ReceiptStatus: "committed", EventVerified: true,
			ActivityID: "exercise-activity-active", ActivityKind: "exercise", StartedAt: clock.Add(-time.Minute).UTC(), DueAt: clock.Add(time.Minute).UTC(),
		}}},
	}
	state := draftRoutineBedtimeIntent(t, runtime, "shiro", "2026-11-14", agentroutine.ActivityHobby,
		time.Date(2026, time.November, 14, 23, 30, 0, 0, loc), sleepAt.Add(15*time.Minute), progress, intentID)
	intentID, taskID := state.Pending.ID, progress.Actors[0].TaskID
	work, found, err := runtime.idleSource(nil)(ctx, "shiro")
	if err != nil || !found {
		t.Fatalf("unsettled WORLD bedtime discovery = %+v found=%t err=%v, want exact cleanup Work", work, found, err)
	}
	if err := work.Run(ctx, nil); err == nil {
		t.Fatal("bedtime cleanup without WORLD clients succeeded; want the exact active operation retained")
	}
	after, err := runtime.store.Load(ctx, "shiro")
	if err != nil || after.Pending == nil || after.Pending.ID != intentID || after.LastFinishedIntentID != "" {
		t.Fatalf("pending state after unavailable WORLD settlement = %+v err=%v, want exact intent retained", after, err)
	}
	if !after.Pending.RetryAfter.Equal(sleepAt.Add(15 * time.Minute)) {
		t.Fatalf("RetryAfter after failed WORLD settlement = %v, want unchanged future retry %v", after.Pending.RetryAfter, sleepAt.Add(15*time.Minute))
	}
	if _, err := tasks.Get(ctx, taskID); !errors.Is(err, taskmanager.ErrNotFound) {
		t.Fatalf("failed WORLD settlement created or closed canonical Task %s: %v", taskID, err)
	}
}

func draftRoutineBedtimeIntent(
	t *testing.T,
	runtime *agentRoutineRuntime,
	actorID, date string,
	kind agentroutine.ActivityKind,
	createdAt, retryAfter time.Time,
	progress routineProgress,
	intentIDs ...string,
) agentroutine.State {
	t.Helper()
	encoded, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	label := string(kind)
	if actorID == "shiro" && kind == agentroutine.ActivityHobby {
		label = "fitness_research"
	}
	intentID := string(modulecore.NewRequestID())
	if len(intentIDs) > 0 {
		intentID = intentIDs[0]
	}
	state, err := runtime.store.DraftIntent(context.Background(), agentroutine.Intent{
		ID: intentID, ActorID: actorID, Date: date, TaskID: string(progress.Actors[0].TaskID),
		Kind: kind, Label: label, Step: "planned", Output: string(encoded), CreatedAt: createdAt.UTC(),
	})
	if err != nil {
		t.Fatalf("draft bedtime routine intent: %v", err)
	}
	state.Pending.RetryAfter = retryAfter
	state, err = runtime.store.Save(context.Background(), state, state.Revision)
	if err != nil {
		t.Fatalf("save bedtime routine retry: %v", err)
	}
	return state
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
