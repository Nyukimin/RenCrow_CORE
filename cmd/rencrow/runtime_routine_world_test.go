package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
)

func TestRoutineWorldSleepWakeBindingRequiresExactAuthenticatedStartAndTerminal(t *testing.T) {
	startedAt := time.Date(2026, time.November, 14, 1, 15, 0, 0, time.UTC)
	endedAt := startedAt.Add(5 * time.Hour)
	layoutSHA, sceneSHA := strings.Repeat("a", 64), strings.Repeat("b", 64)
	active := worldread.Actor{
		ActorID: "aka-world", Present: true, ActivityID: "sleep-aka-1", ActivityKind: "sleep",
		RoomID: "aka-room", SlotID: "bed-aka", ActivityStartedAt: startedAt, HomeSceneSHA256: sceneSHA,
	}
	snapshot := worldread.Snapshot{
		WorldID: "world-home", TimelineID: "timeline-home", TimelineEpoch: "epoch-1", StateSeq: 3,
		HomeSceneSHA256: sceneSHA, Actors: []worldread.Actor{active},
	}
	startFields, _ := json.Marshal(map[string]any{
		"actor_id": "aka-world", "activity_id": "sleep-aka-1", "activity_kind": "sleep", "room_id": "aka-room",
		"slot_id": "bed-aka", "started_at": startedAt.Format(time.RFC3339Nano), "layout_sha256": layoutSHA, "scene_sha256": sceneSHA,
	})
	feed := worldread.Feed{
		Snapshot: snapshot, Cursor: snapshot.StateSeq,
		Events: []worldread.Event{{
			Seq: 2, TransactionID: "txn-sleep-1", EventID: "evt-sleep-1", Kind: "life.home.sleep.started",
			// The owner stamps the envelope before the handler computes StartedAt.
			Payload: startFields, RecordedAt: startedAt.Add(-time.Second),
		}},
	}
	binding, err := routineWorldSleepBindingFromFeed(feed, snapshot, "aka", "aka-world", "aka-room", "bed-aka", active)
	if err != nil {
		t.Fatalf("valid authenticated sleep start rejected: %v", err)
	}
	progress := routineProgress{World: &routineWorldProgress{SleepStart: &binding}}
	terminal := routineWorldOperation{
		Action: worldread.HomeActionWake, EventKind: "life.home.sleep.woke", ActorID: "aka",
		Payload:    worldread.HomeActionPayload{ActorID: "aka", ActivityID: active.ActivityID},
		ActivityID: active.ActivityID, ActivityKind: "sleep", RoomID: active.RoomID, SlotID: active.SlotID,
		StartedAt: startedAt, EndedAt: endedAt, FirstSeq: 4, LayoutSHA256: layoutSHA, SceneSHA256: sceneSHA,
	}
	if err := (&agentRoutineRuntime{}).verifyRoutineWorldActivityBinding(progress, terminal); err != nil {
		t.Fatalf("valid wake terminal rejected: %v", err)
	}
	terminal.SlotID = "another-bed"
	if err := (&agentRoutineRuntime{}).verifyRoutineWorldActivityBinding(progress, terminal); err == nil {
		t.Fatal("wake terminal for a different bed passed the sleep binding")
	}
	terminal.SlotID, terminal.FirstSeq = active.SlotID, binding.Seq
	if err := (&agentRoutineRuntime{}).verifyRoutineWorldActivityBinding(progress, terminal); err == nil {
		t.Fatal("wake terminal preceding its authenticated sleep-start event passed the binding")
	}

	var wrongActor map[string]any
	if err := json.Unmarshal(startFields, &wrongActor); err != nil {
		t.Fatal(err)
	}
	wrongActor["actor_id"] = "different-world-actor"
	wrongPayload, _ := json.Marshal(wrongActor)
	feed.Events[0].Payload = wrongPayload
	if _, err := routineWorldSleepBindingFromFeed(feed, snapshot, "aka", "aka-world", "aka-room", "bed-aka", active); err == nil {
		t.Fatal("sleep-start event for a different WORLD Actor passed the binding")
	}
}

func TestRoutineWorldWakePreparedOperationCanRecoverMissingReceipt(t *testing.T) {
	state := agentroutine.State{Pending: &agentroutine.Intent{ID: "intent-1"}}
	binding := &routineWorldSleepBinding{
		ActorID: "aka", ActivityID: "sleep-aka-1", EventID: "evt-sleep-1", TransactionID: "txn-sleep-1", Seq: 2,
	}
	progress := routineProgress{World: &routineWorldProgress{SleepStart: binding}}
	purpose := "wake:" + binding.ActivityID
	operation := routineWorldOperation{
		Purpose: purpose, Key: routineWorldOperationKey(state.Pending.ID, "aka", purpose), Action: worldread.HomeActionWake,
		EventKind: "life.home.sleep.woke", ActorID: "aka",
		Payload: worldread.HomeActionPayload{ActorID: "aka", ActivityID: binding.ActivityID},
	}
	if !routineWorldWakeOperationCanReplay(state, progress, "aka", operation) {
		t.Fatal("matching prepared wake operation without a saved receipt was not eligible for idempotent recovery")
	}
	operation.Key = "different-key"
	if routineWorldWakeOperationCanReplay(state, progress, "aka", operation) {
		t.Fatal("wake recovery accepted a key outside the current pending intent")
	}
	operation.Key = routineWorldOperationKey(state.Pending.ID, "aka", purpose)
	progress.World.SleepStart = nil
	if routineWorldWakeOperationCanReplay(state, progress, "aka", operation) {
		t.Fatal("wake recovery accepted an unbound operation without authenticated sleep-start evidence")
	}
}

func TestRoutineWorldDueWaitUsesOwnerClockInsteadOfSchedulerClock(t *testing.T) {
	ownerNow := time.Date(2026, time.November, 14, 1, 0, 0, 0, time.UTC)
	schedulerNow := ownerNow.Add(24 * 365 * time.Hour)
	due := ownerNow.Add(60 * time.Millisecond)
	runtime := &agentRoutineRuntime{
		now:      func() time.Time { return schedulerNow },
		worldNow: func() time.Time { return ownerNow },
	}
	started := time.Now()
	if err := runtime.waitRoutineWorldDue(context.Background(), nil, due, time.Second); err != nil {
		t.Fatalf("waitRoutineWorldDue: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 40*time.Millisecond {
		t.Fatalf("owner due wait returned after %s; scheduler clock incorrectly made owner activity immediately due", elapsed)
	}
}

func TestRoutineWorldDiaryIncludesLifecycleStartAndSafeRoomLabel(t *testing.T) {
	tests := []struct {
		kind       string
		payload    string
		want       string
		wantKind   string
		wantNoText string
	}{
		{
			kind: "life.home.sleep.started", payload: `{"actor_id":"mio","room_id":"upper-room-2-1","activity_kind":"sleep","started_at":"2026-11-14T01:00:00Z"}`,
			want: "ミオの部屋", wantKind: "sleep", wantNoText: "upper-room-2-1",
		},
		{
			kind: "life.home.move.started", payload: `{"actor_id":"shiro","target_room_id":"living","activity_kind":"move","started_at":"2026-11-14T01:00:00Z"}`,
			want: "リビング", wantKind: "relax", wantNoText: "target_room_id",
		},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			summary, ok := routineWorldEventSummary(worldread.Event{Kind: test.kind, Payload: []byte(test.payload)})
			if !ok || !strings.Contains(summary, test.want) {
				t.Fatalf("summary=%q relevant=%v, want readable room %q", summary, ok, test.want)
			}
			if strings.Contains(summary, test.wantNoText) {
				t.Fatalf("diary summary exposed raw room/payload identifier: %q", summary)
			}
			kind := routineWorldDiaryActivityKind(worldread.Event{Kind: test.kind, Payload: []byte(test.payload)})
			if string(kind) != test.wantKind {
				t.Fatalf("diary activity kind = %q, want %q", kind, test.wantKind)
			}
		})
	}
}
