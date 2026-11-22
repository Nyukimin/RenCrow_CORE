package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
)

func TestAgentRoutineDaytimeRelaxCreatesCanonicalTaskAndRun(t *testing.T) {
	for _, room := range []string{"living", "mio-room"} {
		t.Run(room, func(t *testing.T) {
			ctx := context.Background()
			clock := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
			runtime, tasks := newDaytimeRoutineRuntime(t, &clock, room)
			work, found, err := runtime.idleSource(nil)(ctx, "mio")
			if err != nil || !found || !strings.HasSuffix(work.ID, ":relax") || work.Run == nil {
				t.Fatalf("daytime discovery = work=%+v found=%t err=%v; want a relaxation Work", work, found, err)
			}
			stop := make(chan struct{})
			if err := work.Run(ctx, stop); err != nil {
				t.Fatalf("run discovered daytime relaxation: %v", err)
			}
			state, err := runtime.store.Load(ctx, "mio")
			if err != nil || state.Pending != nil || state.LastFinishedIntentID == "" {
				t.Fatalf("completed routine state = %+v err=%v; want a finished intent", state, err)
			}
			if state.RestWindow == nil || state.RestWindow.RoomID != room || !state.RestWindow.Until.After(clock) {
				t.Fatalf("rest window = %+v; want an actual confirmed relaxation interval in %s", state.RestWindow, room)
			}
			facts, err := runtime.store.ListFacts(ctx, "mio", clock.Format("2006-01-02"))
			if err != nil || len(facts) != 1 || facts[0].Kind != agentroutine.ActivityRelax || !facts[0].Confirmed {
				t.Fatalf("relaxation facts = %+v err=%v; want one confirmed room observation", facts, err)
			}
			all, err := tasks.List(ctx, domaintask.Filter{Assignee: "mio", ModuleID: agentRoutineModuleID})
			if err != nil || len(all) != 1 || all[0].Status != domaintask.StatusSucceeded || all[0].Title != routineTaskTitle(agentroutine.ActivityRelax) {
				t.Fatalf("canonical routine execution Tasks = %+v err=%v; want one succeeded relax Task", all, err)
			}
			runs, err := tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: all[0].TaskID})
			if err != nil || len(runs) != 1 || runs[0].Status != domaintask.RunStatusSucceeded {
				t.Fatalf("canonical routine Runs = %+v err=%v; want one succeeded Run", runs, err)
			}
			current, err := tasks.SnapshotCurrentActivities(ctx, []string{"mio"})
			if err != nil {
				t.Fatalf("snapshot current rest successor: %v", err)
			}
			successor := current["mio"].Task
			if successor.ModuleID != agentActivityModuleID || successor.Activity == nil ||
				successor.Activity.Kind != domaintask.ActivityKindRest || !successor.Activity.Passive ||
				successor.Timing == nil || successor.Timing.DeadlineAt == nil || !successor.Timing.DeadlineAt.Equal(state.RestWindow.Until) {
				t.Fatalf("current Task after relax completion = %+v; want one passive Rest successor with the persisted interval deadline", successor)
			}
		})
	}
}

func TestAgentRoutineDaytimeFallbackPrecedenceAndSafeWait(t *testing.T) {
	clock := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
	t.Run("existing fallback work is preserved", func(t *testing.T) {
		runtime, _ := newDaytimeRoutineRuntime(t, &clock, "living")
		fallbackWork := agentheartbeat.Work{ID: "existing-daytime-work", Run: func(context.Context, <-chan struct{}) error { return nil }}
		fallback := func(context.Context, string) (agentheartbeat.Work, bool, error) { return fallbackWork, true, nil }
		work, found, err := runtime.idleSource(fallback)(context.Background(), "mio")
		if err != nil || !found || work.ID != fallbackWork.ID {
			t.Fatalf("fallback result = %+v found=%t err=%v; want existing fallback", work, found, err)
		}
	})
	t.Run("fallback error is propagated", func(t *testing.T) {
		runtime, _ := newDaytimeRoutineRuntime(t, &clock, "living")
		wantErr := errors.New("fallback failed")
		fallback := func(context.Context, string) (agentheartbeat.Work, bool, error) {
			return agentheartbeat.Work{}, false, wantErr
		}
		_, _, err := runtime.idleSource(fallback)(context.Background(), "mio")
		if !errors.Is(err, wantErr) {
			t.Fatalf("fallback error = %v; want original error", err)
		}
	})
	t.Run("unknown room remains idle", func(t *testing.T) {
		runtime, _ := newDaytimeRoutineRuntime(t, &clock, "unknown")
		work, found, err := runtime.idleSource(nil)(context.Background(), "mio")
		if err != nil || found {
			t.Fatalf("unknown-room result = %+v found=%t err=%v; want safe idle", work, found, err)
		}
	})
	t.Run("cooldown selects bounded rest", func(t *testing.T) {
		runtime, tasks := newDaytimeRoutineRuntime(t, &clock, "living")
		state, err := runtime.store.EnsureSchedule(context.Background(), "mio", "2026-11-15")
		if err != nil {
			t.Fatal(err)
		}
		state.CooldownUntil = clock.Add(time.Hour)
		if _, err := runtime.store.Save(context.Background(), state, state.Revision); err != nil {
			t.Fatal(err)
		}
		work, found, err := runtime.idleSource(nil)(context.Background(), "mio")
		if err != nil || !found {
			t.Fatalf("cooldown result = %+v found=%t err=%v; want a bounded rest activity", work, found, err)
		}
		if err := work.Run(context.Background(), make(chan struct{})); err != nil {
			t.Fatal(err)
		}
		current, err := tasks.SnapshotCurrentActivities(context.Background(), []string{"mio"})
		if err != nil {
			t.Fatal(err)
		}
		rest := current["mio"].Task
		if rest.Activity.Kind != domaintask.ActivityKindRest || rest.Timing == nil || rest.Timing.DeadlineAt == nil || !rest.Timing.DeadlineAt.Equal(clock.Add(45*time.Minute)) {
			t.Fatalf("cooldown current Task=%+v; want a real rest capped at 45 minutes", rest)
		}
	})
}

func TestAgentRoutineDaytimePreemptedOrStoppedWorkCreatesNoTask(t *testing.T) {
	for _, test := range []struct {
		name string
		stop bool
	}{
		{name: "fresh cooldown preempts discovered relaxation"},
		{name: "stop before run", stop: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			clock := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.FixedZone(agentroutine.JST, 9*60*60))
			runtime, tasks := newDaytimeRoutineRuntime(t, &clock, "living")
			work, found, err := runtime.idleSource(nil)(ctx, "mio")
			if err != nil || !found {
				t.Fatalf("daytime discovery = %+v found=%t err=%v; want reserved relaxation", work, found, err)
			}
			stop := make(chan struct{})
			if test.stop {
				close(stop)
			} else {
				state, loadErr := runtime.store.Load(ctx, "mio")
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				state.CooldownUntil = clock.Add(time.Hour)
				if _, saveErr := runtime.store.Save(ctx, state, state.Revision); saveErr != nil {
					t.Fatal(saveErr)
				}
			}
			if err := work.Run(ctx, stop); err != nil {
				t.Fatalf("preempted/stopped Work.Run() = %v; want clean no-op", err)
			}
			created, err := tasks.List(ctx, domaintask.Filter{Assignee: "mio", ModuleID: agentRoutineModuleID})
			if err != nil || len(created) != 0 {
				t.Fatalf("preempted/stopped execution Tasks = %+v err=%v; want no routine execution Task", created, err)
			}
			state, err := runtime.store.Load(ctx, "mio")
			if err != nil || state.Pending != nil {
				t.Fatalf("preempted/stopped state = %+v err=%v; want no pending intent", state, err)
			}
		})
	}
}

func TestDaytimeRelaxPlanDoesNotReplaceOtherRoutineBoundaries(t *testing.T) {
	for _, kind := range []agentroutine.ActivityKind{agentroutine.ActivitySleep, agentroutine.ActivityWake, agentroutine.ActivityRelax} {
		plan := agentroutine.Plan{Kind: kind, Date: "2026-11-15", Reason: "existing plan"}
		got := daytimeRelaxPlan(plan, agentroutine.Presence{SharedRoom: agentroutine.AvailabilityAvailable})
		if got.Kind != kind || got.Reason != plan.Reason {
			t.Errorf("daytimeRelaxPlan(%q) = %+v; want original boundary unchanged", kind, got)
		}
	}
}

func newDaytimeRoutineRuntime(t *testing.T, clock *time.Time, room string, activity ...string) (*agentRoutineRuntime, *taskmanager.Manager) {
	t.Helper()
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	runtime, tasks := newRoutineAdmissionRuntime(t, taskStore, clock)
	runtime.cfg = &config.Config{Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{
		SharedRooms: []string{"living"}, PrivateRooms: map[string]string{"mio": "mio-room"},
	}}}
	runtime.worldActorIDs = map[string]string{"mio-world": "mio"}
	actor := map[string]any{"room_id": room}
	if len(activity) > 0 {
		actor["activity_id"], actor["activity_kind"] = "active-world-operation", activity[0]
	}
	snapshot := map[string]any{
		"schema_version": 1, "world_id": "world", "timeline_id": "timeline", "timeline_epoch": "epoch",
		"state_seq": 1, "state_hash": strings.Repeat("0", 64),
		"state":            map[string]any{"actors": map[string]any{"mio-world": actor}},
		"home_move_points": map[string]any{},
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(encoded) }))
	t.Cleanup(server.Close)
	reader, err := worldread.New(worldread.Config{
		BaseURL: server.URL, Credential: "reader-token", WorldID: "world", TimelineID: "timeline", TimelineEpoch: "epoch",
		ActorIDs: []string{"mio-world"}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.world = reader
	return runtime, tasks
}
