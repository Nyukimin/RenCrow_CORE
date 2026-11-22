package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRoutineSelectionLeavesEvaluationDuringDeferredActivity(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "cooldown"
		if pending {
			name = "diary retry"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			clock := time.Date(2026, 11, 15, 12, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
			runtime, tasks := newDaytimeRoutineRuntime(t, &clock, "living")
			state, err := runtime.store.Load(ctx, "mio")
			if err != nil {
				t.Fatal(err)
			}
			until := clock.Add(15 * time.Minute)
			if pending {
				intent, err := runtime.newRoutineIntent("mio", agentroutine.Plan{Kind: agentroutine.ActivityDiary, Date: "2026-11-14"}, clock)
				if err != nil {
					t.Fatal(err)
				}
				intent.RetryAfter = until
				diaryTask, diaryRun, err := tasks.CreateAndStartActivityRun(ctx, domaintask.Task{
					TaskID: modulecore.NewTaskID(), Title: "CORE routine diary", ModuleID: agentRoutineModuleID,
					OwnerID: runtime.ownerID, Assignee: "mio", Route: domaintask.RouteCHAT,
					Priority: domaintask.PriorityLow, InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true,
				}, agentRoutineTaskActivity(&intent, "mio"), domaintask.SharedRoleContext{
					UserIntent: "CORE routine step", CurrentPlan: "CORE routine diary",
				})
				if err != nil {
					t.Fatalf("create saved diary Task and Run: %v", err)
				}
				if err := runtime.pauseRuns(ctx, []routinePreparedRun{{task: diaryTask, run: diaryRun}}, "saved diary retry fixture"); err != nil {
					t.Fatalf("pause saved diary Run: %v", err)
				}
				boardServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					http.Error(w, "unexpected BOARD call for completed checkpoint", http.StatusInternalServerError)
				}))
				t.Cleanup(boardServer.Close)
				boardClient, err := boardcommunication.NewClient(boardcommunication.Options{
					URL: boardServer.URL, TokenSource: func(context.Context) ([]byte, error) { return []byte("test-token"), nil },
					ExpectedConnection: boardcommunication.ConnectionIdentity{
						PrincipalID: "rencrow-comm", JournalInstanceID: "journal_0123456789abcdef0123456789abcdef",
					},
				})
				if err != nil {
					t.Fatalf("create test BOARD client: %v", err)
				}
				t.Cleanup(boardClient.Close)
				runtime.deps = &Dependencies{}
				runtime.deps.boardCommunicationActorBoots = map[string]*boardCommunicationBoot{
					"mio": {composition: &boardCommunicationComposition{boardClient: boardClient}},
				}
				intent.TaskID = string(diaryTask.TaskID)
				progress := routineProgress{
					Actors:          []routineActorProgress{{ActorID: "mio", TaskID: diaryTask.TaskID, RunID: diaryRun.RunID}},
					EffectCompleted: true, CompletedAt: clock,
				}
				encoded, err := json.Marshal(progress)
				if err != nil {
					t.Fatal(err)
				}
				intent.Output = string(encoded)
				state, err = runtime.store.DraftIntent(ctx, intent)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				state.CooldownUntil = until
				state, err = runtime.store.Save(ctx, state, state.Revision)
				if err != nil {
					t.Fatal(err)
				}
			}
			before, err := runtime.store.Load(ctx, "mio")
			if err != nil {
				t.Fatal(err)
			}
			work, found, err := runtime.idleSource(nil)(ctx, "mio")
			if err != nil || !found {
				t.Fatalf("deferred selection found=%t err=%v; want a bounded real activity", found, err)
			}
			discovered, err := runtime.store.Load(ctx, "mio")
			if err != nil || !reflect.DeepEqual(before, discovered) {
				t.Fatalf("discovery changed durable intent: err=%v", err)
			}
			if err := work.Run(ctx, make(chan struct{})); err != nil {
				t.Fatal(err)
			}
			current, err := tasks.SnapshotCurrentActivities(ctx, []string{"mio"})
			if err != nil {
				t.Fatal(err)
			}
			task := current["mio"].Task
			if task.Activity.Kind != domaintask.ActivityKindRest || !task.Activity.Passive || task.Timing == nil || task.Timing.DeadlineAt == nil || !task.Timing.DeadlineAt.Equal(until) {
				t.Fatalf("current=%+v; want verified rest until retry deadline", task)
			}
			after, err := runtime.store.Load(ctx, "mio")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("rest overwrote pending/checkpoint/cooldown: err=%v", err)
			}
			runs, err := tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
			if err != nil || len(runs) != 0 {
				t.Fatalf("passive rest runs=%+v err=%v", runs, err)
			}
			if _, found, err := runtime.idleSource(nil)(ctx, "mio"); err != nil || found {
				t.Fatalf("duplicate rest selection found=%t err=%v", found, err)
			}
			clock = until
			expiry, found, err := runtime.idleSource(nil)(ctx, "mio")
			if err != nil || !found {
				t.Fatalf("expiry found=%t err=%v", found, err)
			}
			if err := expiry.Run(ctx, make(chan struct{})); err != nil {
				t.Fatal(err)
			}
			finished, err := tasks.Get(ctx, task.TaskID)
			if err != nil || finished.Status != domaintask.StatusSucceeded {
				t.Fatalf("rest did not expire: %+v %v", finished, err)
			}
			currentAfterExpiry, err := tasks.SnapshotCurrentActivities(ctx, []string{"mio"})
			if err != nil {
				t.Fatal(err)
			}
			successor := currentAfterExpiry["mio"]
			if successor.Task.TaskID == task.TaskID || successor.Participant.Revision <= current["mio"].Participant.Revision ||
				successor.Task.Activity == nil || successor.Task.Activity.Kind != domaintask.ActivityKindEvaluating {
				t.Fatalf("rest expiry current=%+v; want a new higher-revision canonical evaluating Task", successor)
			}
			if pending {
				saved, err := runtime.store.Load(ctx, "mio")
				if err != nil {
					t.Fatal(err)
				}
				if saved.Pending == nil || saved.Pending.ID != before.Pending.ID || saved.Pending.TaskID != before.Pending.TaskID {
					t.Fatalf("expiry changed the saved diary reservation: %+v", saved.Pending)
				}
				progress, err := decodeRoutineProgress(saved.Pending.Output)
				if err != nil || len(progress.Actors) != 1 || string(progress.Actors[0].TaskID) != saved.Pending.TaskID {
					t.Fatalf("saved exact diary checkpoint=%+v err=%v", progress, err)
				}
				resume, found, err := runtime.idleSource(nil)(ctx, "mio")
				if err != nil || !found || !strings.Contains(resume.ID, string(agentroutine.ActivityDiary)) {
					t.Fatalf("expired diary selection=%q found=%t err=%v; want exact saved Diary activity", resume.ID, found, err)
				}
				if err := resume.Run(ctx, make(chan struct{})); err != nil {
					t.Fatalf("dispatch and resume saved diary after rest expiry: %v", err)
				}
				runs, err := tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: progress.Actors[0].TaskID})
				if err != nil || len(runs) != 2 || runs[0].RunID != progress.Actors[0].RunID ||
					runs[1].RunID == runs[0].RunID || runs[1].StartReason != domaintask.RunStartReasonCheckpointResume ||
					runs[1].Status != domaintask.RunStatusSucceeded {
					t.Fatalf("saved diary Run lifecycle=%+v err=%v; want a fresh completed checkpoint Run", runs, err)
				}
				diaryTask, err := tasks.Get(ctx, progress.Actors[0].TaskID)
				if err != nil || diaryTask.Status != domaintask.StatusSucceeded {
					t.Fatalf("saved diary Task=%+v err=%v; want the same canonical Task completed", diaryTask, err)
				}
				completed, err := runtime.store.Load(ctx, "mio")
				if err != nil || completed.Pending != nil || completed.LastFinishedIntentID != saved.Pending.ID {
					t.Fatalf("completed diary state=%+v err=%v; want saved intent finished and no Pending", completed, err)
				}
				rebound, err := tasks.SnapshotCurrentActivities(ctx, []string{"mio"})
				if err != nil {
					t.Fatal(err)
				}
				if rebound["mio"].Task.TaskID == task.TaskID || rebound["mio"].Task.Activity == nil ||
					rebound["mio"].Task.Activity.Kind != domaintask.ActivityKindEvaluating ||
					rebound["mio"].Participant.Revision <= successor.Participant.Revision {
					t.Fatalf("resumed diary current=%+v; want newer canonical successor after exact Task completion", rebound["mio"])
				}
			} else {
				next, found, err := runtime.idleSource(nil)(ctx, "mio")
				if err != nil || !found || !strings.Contains(next.ID, string(agentroutine.ActivityRelax)) {
					t.Fatalf("post-cooldown selection=%q found=%t err=%v; want normal Relax work", next.ID, found, err)
				}
				if err := next.Run(ctx, make(chan struct{})); err != nil {
					t.Fatalf("run post-cooldown normal Relax work: %v", err)
				}
				state, err := runtime.store.Load(ctx, "mio")
				if err != nil || state.Pending != nil || state.LastFinishedIntentID == "" {
					t.Fatalf("post-cooldown Relax lifecycle state=%+v err=%v; want completed normal activity", state, err)
				}
				afterRelax, err := tasks.SnapshotCurrentActivities(ctx, []string{"mio"})
				if err != nil {
					t.Fatal(err)
				}
				rest := afterRelax["mio"]
				if rest.Task.TaskID == task.TaskID || rest.Task.Activity == nil || rest.Task.Activity.Kind != domaintask.ActivityKindRest ||
					!rest.Task.Activity.Passive || rest.Task.Timing == nil || rest.Task.Timing.DeadlineAt == nil || !rest.Task.Timing.DeadlineAt.After(clock) {
					t.Fatalf("post-cooldown current=%+v; want new non-evaluating Rest Task with a future deadline", rest)
				}
			}
		})
	}
}

func TestRoutineSelectionRestRevalidatesRoomAndCurrentTask(t *testing.T) {
	for _, scenario := range []string{"unknown room", "active WORLD operation", "night shared room", "foreground wins", "stop"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			clock := time.Date(2026, 11, 15, 12, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
			if scenario == "night shared room" {
				clock = clock.Add(10 * time.Hour)
			}
			room := "living"
			if scenario == "unknown room" {
				room = "unknown"
			}
			var worldActivity []string
			if scenario == "active WORLD operation" {
				worldActivity = []string{"move"}
			}
			runtime, tasks := newDaytimeRoutineRuntime(t, &clock, room, worldActivity...)
			state, err := runtime.store.Load(ctx, "mio")
			if err != nil {
				t.Fatal(err)
			}
			state.CooldownUntil = clock.Add(15 * time.Minute)
			if _, err := runtime.store.Save(ctx, state, state.Revision); err != nil {
				t.Fatal(err)
			}
			work, found, err := runtime.idleSource(nil)(ctx, "mio")
			if err != nil || !found {
				t.Fatalf("discovery=%t %v", found, err)
			}
			if scenario == "foreground wins" {
				_, _, err = tasks.CreateAndStartActivityRun(ctx, domaintask.Task{Title: "foreground", Assignee: "mio", OwnerID: "mio", Route: domaintask.RouteCHAT}, activityMetadataForActor(domaintask.ActivityKindChat, "Chat", "talking", false, "mio", ""), domaintask.SharedRoleContext{})
				if err != nil {
					t.Fatal(err)
				}
			}
			before, err := tasks.SnapshotCurrentActivities(ctx, []string{"mio"})
			if err != nil {
				t.Fatal(err)
			}
			stop := make(chan struct{})
			if scenario == "stop" {
				close(stop)
			}
			runErr := work.Run(ctx, stop)
			if scenario != "stop" && runErr == nil {
				t.Fatal("stale/unverified rest was accepted")
			}
			after, err := tasks.SnapshotCurrentActivities(ctx, []string{"mio"})
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("rest overwrote current Task: %v", err)
			}
		})
	}
}

func TestRoutineSelectionPreservesLogicalSleepBoundaryForRevalidation(t *testing.T) {
	ctx := context.Background()
	sleepAt, wakeAt, err := agentroutine.ScheduleFor("mio", "2026-11-16")
	if err != nil {
		t.Fatal(err)
	}
	clock := sleepAt.Add(time.Minute)
	runtime, _ := newDaytimeRoutineRuntime(t, &clock, "living")
	state, err := runtime.store.EnsureSchedule(ctx, "mio", "2026-11-16")
	if err != nil {
		t.Fatal(err)
	}
	state.LogicalSleepUntil = wakeAt
	if _, err := runtime.store.Save(ctx, state, state.Revision); err != nil {
		t.Fatal(err)
	}
	plan, _, err := runtime.planFresh(ctx, "mio", true, nil)
	if err != nil || plan.Kind != agentroutine.ActivitySleep || !plan.LogicalSleep {
		t.Fatalf("sleep boundary lost: %+v %v", plan, err)
	}
}
