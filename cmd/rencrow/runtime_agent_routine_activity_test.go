package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

func TestFinishRoutineIntentStoresRelaxRestBoundedByPlannedSleep(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 15, 23, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	runtime := &agentRoutineRuntime{now: func() time.Time { return clock }}
	store, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime.store = store
	taskRoot, err := os.MkdirTemp(".", ".routine-activity-tasks-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(taskRoot); err != nil {
			t.Errorf("remove Task store root: %v", err)
		}
	})
	taskStore, err := taskpersistence.NewJSONLStore(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	runtime.tasks, runtime.ownerID = taskmanager.New(taskStore, taskmanager.DefaultParallelLimits()), "mio"
	runtime.cfg = &config.Config{Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{
		SharedRooms: []string{"living"}, PrivateRooms: map[string]string{"mio": "private-mio"},
	}}}
	sleepAt, wakeAt, err := agentroutine.ScheduleFor("mio", "2026-11-16")
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.EnsureSchedule(ctx, "mio", "2026-11-16")
	if err != nil || !state.PlannedSleep.Equal(sleepAt) || !state.PlannedWake.Equal(wakeAt) {
		t.Fatalf("EnsureSchedule = %+v, %v", state, err)
	}
	if err := initializeAgentCurrentActivities(ctx, runtime.tasks, runtime.ownerID, runtime, nil); err != nil {
		t.Fatalf("initialize current Task: %v", err)
	}
	intent := agentroutine.Intent{ID: "relax-world-confirmed", ActorID: "mio", Date: "2026-11-15", Kind: agentroutine.ActivityRelax}
	state, err = store.DraftIntent(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	completedAt := sleepAt.Add(-20 * time.Minute)
	progress := routineProgress{CompletedAt: completedAt, World: &routineWorldProgress{RelaxRoomID: "living", RelaxObservedAt: completedAt.Add(-time.Minute)}}
	finished, err := runtime.finishRoutineIntent(ctx, state, progress)
	if err != nil || finished.Pending != nil || finished.RestWindow == nil || !finished.RestWindow.Until.Equal(sleepAt) || !finished.CooldownUntil.Equal(sleepAt) || finished.LastFinishedIntentID != intent.ID {
		t.Fatalf("finish rest = %+v, %v; want bounded window ending at %v", finished, err, sleepAt)
	}
}

func TestCurrentRoutineActivityProjectsSleepAndRequiresFreshSameRoomForRest(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 15, 6, 0, 0, 0, time.FixedZone("JST", 9*60*60))
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
	sleepAt, wakeAt, err := agentroutine.ScheduleFor("mio", "2026-11-15")
	if err != nil {
		t.Fatal(err)
	}
	state, err := runtime.store.EnsureSchedule(ctx, "mio", "2026-11-15")
	if err != nil {
		t.Fatal(err)
	}
	clock = sleepAt.Add(20 * time.Minute)
	if !clock.Before(wakeAt) {
		t.Fatal("routine sleep window is invalid")
	}
	state.LogicalSleepUntil = wakeAt
	state, err = runtime.store.Save(ctx, state, state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	type taskRunSnapshot struct {
		TaskID string
		Runs   []domaintask.Run
	}
	type projectionReadSnapshot struct {
		Tasks   []domaintask.Task
		Runs    []taskRunSnapshot
		Current map[string]domaintask.CurrentActivitySnapshot
	}
	readTaskState := func() projectionReadSnapshot {
		t.Helper()
		allTasks, listErr := tasks.List(ctx, domaintask.Filter{})
		if listErr != nil {
			t.Fatalf("list Tasks around read-only projection: %v", listErr)
		}
		state := projectionReadSnapshot{Tasks: allTasks, Runs: make([]taskRunSnapshot, 0, len(allTasks))}
		for _, task := range allTasks {
			runs, runsErr := tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
			if runsErr != nil {
				t.Fatalf("list Runs for Task %s around projection: %v", task.TaskID, runsErr)
			}
			state.Runs = append(state.Runs, taskRunSnapshot{TaskID: string(task.TaskID), Runs: runs})
		}
		state.Current, err = tasks.SnapshotCurrentActivities(ctx, []string{"mio"})
		if err != nil {
			t.Fatalf("snapshot canonical current activity around projection: %v", err)
		}
		return state
	}
	beforeProjection := readTaskState()
	got, err := runtime.CurrentRoutineActivity(ctx, "mio")
	if err != nil || got.Availability != "available" || got.OngoingActivity != "sleep_window" {
		t.Fatalf("sleep projection = %+v, %v; want logical sleep window", got, err)
	}

	clock = time.Date(2026, time.November, 15, 18, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	state, err = runtime.store.Load(ctx, "mio")
	if err != nil {
		t.Fatal(err)
	}
	state.LogicalSleepUntil = time.Time{}
	state.LastFinishedIntentID = "verified-relax"
	state.RestWindow = &agentroutine.RestWindow{ObservedAt: clock.Add(-time.Minute), Until: clock.Add(30 * time.Minute), RoomID: "living"}
	state, err = runtime.store.Save(ctx, state, state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	runtime.cfg = &config.Config{Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{SharedRooms: []string{"living"}}}}
	runtime.worldActorIDs = map[string]string{"mio-world": "mio"}
	worldRoom := "living"
	worldActivityID := ""
	worldActivityKind := ""
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"schema_version": 1, "world_id": "world", "timeline_id": "timeline", "timeline_epoch": "epoch",
			"state_seq": requests, "state_hash": strings.Repeat("0", 64),
			"state": map[string]any{"actors": map[string]any{"mio-world": map[string]any{
				"room_id": worldRoom, "activity_id": worldActivityID, "activity_kind": worldActivityKind,
			}}},
			"home_move_points": map[string]any{},
		})
	}))
	defer server.Close()
	runtime.world, err = worldread.New(worldread.Config{BaseURL: server.URL, Credential: "read-token", WorldID: "world", TimelineID: "timeline", TimelineEpoch: "epoch", ActorIDs: []string{"mio-world"}, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	got, err = runtime.CurrentRoutineActivity(ctx, "mio")
	if err != nil || got.Availability != "available" || got.OngoingActivity != "rest" || requests != 1 {
		t.Fatalf("same-room rest projection = %+v, %v requests=%d", got, err, requests)
	}
	worldRoom = "other-room"
	got, err = runtime.CurrentRoutineActivity(ctx, "mio")
	if err != nil || got.OngoingActivity != "waiting" || requests != 2 {
		t.Fatalf("different-room projection = %+v, %v requests=%d; want waiting", got, err, requests)
	}
	worldRoom, worldActivityID, worldActivityKind = "living", "activity-private", "exercise"
	got, err = runtime.CurrentRoutineActivity(ctx, "mio")
	if err != nil || got.OngoingActivity != "waiting" || requests != 3 {
		t.Fatalf("same-room active WORLD projection = %+v, %v requests=%d; want waiting", got, err, requests)
	}
	clock = clock.Add(time.Hour)
	got, err = runtime.CurrentRoutineActivity(ctx, "mio")
	if err != nil || got.OngoingActivity != "waiting" || requests != 3 {
		t.Fatalf("expired rest projection = %+v, %v requests=%d; expired window must skip WORLD read", got, err, requests)
	}
	afterProjection := readTaskState()
	if !reflect.DeepEqual(afterProjection, beforeProjection) {
		t.Fatalf("read-only routine projection changed Task, Activity metadata, current binding, or Run state:\nbefore=%+v\nafter=%+v", beforeProjection, afterProjection)
	}
}

func TestCurrentRoutineActivitiesUsesOneFreshWorldSnapshotForEightRestingActors(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 15, 18, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	store, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := &agentRoutineRuntime{
		store: store, now: func() time.Time { return clock },
		cfg:           &config.Config{Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{SharedRooms: []string{"living"}}}},
		worldActorIDs: make(map[string]string),
	}
	actorIDs := append([]string(nil), agentHeartbeatActorIDs...)
	actors := make(map[string]any, len(actorIDs))
	worldIDs := make([]string, 0, len(actorIDs))
	for _, actorID := range actorIDs {
		worldID := actorID + "-world"
		worldIDs = append(worldIDs, worldID)
		runtime.worldActorIDs[worldID] = actorID
		actors[worldID] = map[string]any{"room_id": "living"}
		state := agentroutine.State{
			ActorID: actorID, LastFinishedIntentID: "relax-" + actorID,
			RestWindow: &agentroutine.RestWindow{ObservedAt: clock.Add(-time.Minute), Until: clock.Add(20 * time.Minute), RoomID: "living"},
		}
		if _, err := store.Save(ctx, state, 0); err != nil {
			t.Fatalf("save actor %s: %v", actorID, err)
		}
	}
	var worldReads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		worldReads++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"schema_version": 1, "world_id": "world", "timeline_id": "timeline", "timeline_epoch": "epoch",
			"state_seq": 1, "state_hash": strings.Repeat("0", 64),
			"state": map[string]any{"actors": actors}, "home_move_points": map[string]any{},
		})
	}))
	defer server.Close()
	runtime.world, err = worldread.New(worldread.Config{BaseURL: server.URL, Credential: "read-token", WorldID: "world", TimelineID: "timeline", TimelineEpoch: "epoch", ActorIDs: worldIDs, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	values, err := runtime.CurrentRoutineActivities(ctx, actorIDs)
	if err != nil {
		t.Fatalf("batch activity read: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
		t.Fatalf("eight-actor projection took %s, want under 500ms", elapsed)
	}
	if worldReads != 1 || len(values) != len(actorIDs) {
		t.Fatalf("WORLD reads=%d projections=%d, want one snapshot and eight projections", worldReads, len(values))
	}
	for _, actorID := range actorIDs {
		if value := values[actorID]; value.Availability != "available" || value.OngoingActivity != "rest" {
			t.Errorf("projection for %s = %+v, want available/rest", actorID, value)
		}
	}
}
