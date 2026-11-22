package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
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
			if pending {
				saved, err := runtime.store.Load(ctx, "mio")
				if err != nil {
					t.Fatal(err)
				}
				decision := runtime.selectRoutineActivity("mio", saved, clock, agentroutine.Presence{})
				if decision.Plan.Kind != agentroutine.ActivityDiary || !decision.Plan.Resuming || decision.Plan.IntentID != before.Pending.ID {
					t.Fatalf("expired retry did not return the exact saved diary: %+v", decision)
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
