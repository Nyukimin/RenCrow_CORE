package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestCurrentRoutineActivityProjectsVerifiedMoveHomeAfterDueEvenDuringSleep(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 15, 22, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	store, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := &agentRoutineRuntime{
		store: store, now: func() time.Time { return clock },
		cfg:           &config.Config{Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{PrivateRooms: map[string]string{"mio": "private-mio"}}}},
		worldActorIDs: map[string]string{"mio-world": "mio"},
	}
	sleepAt, wakeAt, err := agentroutine.ScheduleFor("mio", "2026-11-15")
	if err != nil {
		t.Fatal(err)
	}
	clock = sleepAt.Add(10 * time.Minute)
	if !clock.Before(wakeAt) {
		t.Fatal("fixture clock is outside the logical sleep window")
	}
	state, err := store.EnsureSchedule(ctx, "mio", "2026-11-15")
	if err != nil {
		t.Fatal(err)
	}
	state.LogicalSleepUntil = wakeAt
	state, err = store.Save(ctx, state, state.Revision)
	if err != nil {
		t.Fatal(err)
	}

	const intentID = "intent-moving-home"
	const activityID = "home-mio-move-7"
	const routeID = "route-mio-7"
	const scene = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	dueAt := clock.Add(-time.Minute)
	worldActivityID, worldTargetRoom, worldRouteID, worldScene, worldPhase := activityID, "private-mio", routeID, scene, "in_transit"
	worldStatus := 0
	purpose := "sleep-bed:move"
	progress := routineProgress{Actors: []routineActorProgress{{ActorID: "mio", TaskID: core.NewTaskID()}}, World: &routineWorldProgress{
		Operations: []routineWorldOperation{{
			Purpose: purpose, Key: routineWorldOperationKey(intentID, "mio", purpose),
			Action: worldread.HomeActionMove, EventKind: "life.home.move.started", ActorID: "mio",
			Payload:   worldread.HomeActionPayload{ActorID: "mio", RoomID: "private-mio", SlotID: "bed-mio"},
			ReceiptID: "receipt-move-start-7", ReceiptStatus: "committed", EventVerified: true,
			ActivityID: activityID, ActivityKind: "move", TargetRoomID: "private-mio",
			RouteID: routeID, SceneSHA256: scene, StartedAt: clock.Add(-10 * time.Minute), DueAt: dueAt,
		}},
	}}
	output, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	state, err = store.DraftIntent(ctx, agentroutine.Intent{
		ID: intentID, ActorID: "mio", Date: "2026-11-15", Kind: agentroutine.ActivitySleep,
		Step: "world_move_started", Output: string(output), CreatedAt: clock.Add(-10 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if worldStatus != 0 {
			w.WriteHeader(worldStatus)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"schema_version": 1, "world_id": "world-home", "timeline_id": "timeline-home", "timeline_epoch": "epoch-home",
			"state_seq": 7, "state_hash": strings.Repeat("a", 64), "home_scene_sha256": worldScene,
			"state": map[string]any{"actors": map[string]any{"mio-world": map[string]any{
				"room_id": "hallway", "activity_id": worldActivityID, "activity_kind": "move",
				"activity_started_at": clock.Add(-10 * time.Minute).Format(time.RFC3339Nano),
				"activity_due_at":     dueAt.Format(time.RFC3339Nano), "target_room_id": worldTargetRoom,
				"target_slot_id": "bed-mio", "move_phase": worldPhase, "move_route_id": worldRouteID,
				"home_scene_sha256": worldScene,
			}}}, "home_move_points": map[string]any{},
		})
	}))
	defer server.Close()
	runtime.world, err = worldread.New(worldread.Config{
		BaseURL: server.URL, Credential: "read-token", WorldID: "world-home", TimelineID: "timeline-home",
		TimelineEpoch: "epoch-home", ActorIDs: []string{"mio-world"}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := runtime.CurrentRoutineActivity(ctx, "mio")
	if err != nil || got.Availability != "available" || got.OngoingActivity != "moving_home" {
		t.Fatalf("active private-room move projection = %+v, %v; want available/moving_home (pending=%+v)", got, err, state.Pending)
	}

	for _, tc := range []struct {
		name   string
		mutate func()
	}{
		{name: "activity mismatch", mutate: func() { worldActivityID = "other-activity" }},
		{name: "target mismatch", mutate: func() { worldTargetRoom = "other-room" }},
		{name: "route mismatch", mutate: func() { worldRouteID = "other-route" }},
		{name: "scene mismatch", mutate: func() { worldScene = strings.Repeat("c", 64) }},
		{name: "legacy phase", mutate: func() { worldPhase = "moving" }},
		{name: "unexpected phase", mutate: func() { worldPhase = "arriving" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			worldActivityID, worldTargetRoom, worldRouteID, worldScene, worldPhase = activityID, "private-mio", routeID, scene, "in_transit"
			tc.mutate()
			got, err := runtime.CurrentRoutineActivity(ctx, "mio")
			if err != nil || got.OngoingActivity == "moving_home" {
				t.Fatalf("mismatched active WORLD move projected as movement: %+v, %v", got, err)
			}
		})
	}

	for _, tc := range []struct {
		name   string
		mutate func(*routineWorldOperation)
	}{
		{name: "receipt missing", mutate: func(op *routineWorldOperation) { op.ReceiptID = "" }},
		{name: "event not verified", mutate: func(op *routineWorldOperation) { op.EventVerified = false }},
		{name: "receipt not committed", mutate: func(op *routineWorldOperation) { op.ReceiptStatus = "retry" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := progress
			changed.World = &routineWorldProgress{Operations: append([]routineWorldOperation(nil), progress.World.Operations...)}
			tc.mutate(&changed.World.Operations[0])
			encoded, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			latest, err := store.Load(ctx, "mio")
			if err != nil {
				t.Fatal(err)
			}
			latest.Pending.Output = string(encoded)
			if _, err := store.Save(ctx, latest, latest.Revision); err != nil {
				t.Fatal(err)
			}
			got, err := runtime.CurrentRoutineActivity(ctx, "mio")
			if err != nil || got.OngoingActivity == "moving_home" {
				t.Fatalf("unconfirmed receipt projected as movement: %+v, %v", got, err)
			}
			latest, err = store.Load(ctx, "mio")
			if err != nil {
				t.Fatal(err)
			}
			latest.Pending.Output = string(output)
			if _, err := store.Save(ctx, latest, latest.Revision); err != nil {
				t.Fatal(err)
			}
		})
	}

	worldActivityID, worldTargetRoom, worldRouteID, worldScene, worldPhase = activityID, "private-mio", routeID, scene, "in_transit"
	worldStatus = http.StatusServiceUnavailable
	got, err = runtime.CurrentRoutineActivity(ctx, "mio")
	if err != nil || got.Availability != "unavailable" || got.OngoingActivity != "" {
		t.Fatalf("WORLD read failure projected a false move: %+v, %v", got, err)
	}
}
