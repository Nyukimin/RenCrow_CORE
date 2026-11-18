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

func TestRoutineWorldFashionShowAndReadingMapToConfirmedHomeHobbies(t *testing.T) {
	for _, test := range []struct {
		actorID string
		label   string
		kind    string
		event   string
		want    string
	}{
		{"mio", "fashion", worldread.HomeActivityFashionShow, worldread.HomeEventFashionShowCompleted, "ファッションショー"},
		{"midori", "reading", worldread.HomeActivityReading, worldread.HomeEventReadingCompleted, "BL読書"},
	} {
		t.Run(test.actorID, func(t *testing.T) {
			if !routineWorldHomeHobby(test.actorID, test.label) {
				t.Fatalf("routineWorldHomeHobby(%q, %q) = false", test.actorID, test.label)
			}
			payload, _ := json.Marshal(map[string]any{"actor_id": test.actorID, "activity_kind": test.kind})
			summary, ok := routineWorldEventSummary(worldread.Event{Kind: test.event, Payload: payload})
			if !ok || !strings.Contains(summary, test.want) {
				t.Fatalf("event summary = %q, relevant=%v; want %q", summary, ok, test.want)
			}
		})
	}
	if routineWorldHomeHobby("mio", "video_creativity") || routineWorldHomeHobby("midori", "bl_story_ideas") {
		t.Fatal("non-WORLD hobbies were changed into home actions")
	}
}

func TestRoutineWorldHobbyLifecycleSpecCoversExistingAndNewActors(t *testing.T) {
	for _, test := range []struct {
		actorID, label, kind, action, startEvent, finishEvent, slot string
	}{
		{"shiro", "fitness_research", "exercise", worldread.HomeActionExercise, "life.home.exercise.started", "life.home.exercise.completed", "shiro-exercise"},
		{"kuro", "creative_mirror_ideas", "mirror_pose", worldread.HomeActionMirror, "life.home.mirror_pose.started", "life.home.mirror_pose.completed", "kuro-mirror"},
		{"mio", "fashion", worldread.HomeActivityFashionShow, worldread.HomeActionFashionShow, worldread.HomeEventFashionShowStarted, worldread.HomeEventFashionShowCompleted, "mio-fashion"},
		{"midori", "reading", worldread.HomeActivityReading, worldread.HomeActionReading, worldread.HomeEventReadingStarted, worldread.HomeEventReadingCompleted, "midori-reading"},
	} {
		spec, ok := routineWorldHomeHobbyFor(test.actorID, test.label)
		if !ok || spec.activityKind != test.kind || spec.startAction != test.action || spec.startEvent != test.startEvent ||
			spec.finishEvent != test.finishEvent || spec.slotID != test.slot || !routineWorldSettlementActivityKind(spec.activityKind) {
			t.Errorf("WORLD hobby spec (%s,%s) = %+v, ok=%v", test.actorID, test.label, spec, ok)
		}
	}
	if routineWorldHomeHobby("midori", "bl_story_ideas") || routineWorldSettlementActivityKind("sleep") {
		t.Fatal("non-home hobby or sleep entered the physical hobby settlement path")
	}
	for _, pair := range []struct{ action, event string }{
		{worldread.HomeActionFashionShow, worldread.HomeEventFashionShowStarted},
		{worldread.HomeActionFashionShow, worldread.HomeEventFashionShowCompleted},
		{worldread.HomeActionReading, worldread.HomeEventReadingStarted},
		{worldread.HomeActionReading, worldread.HomeEventReadingCompleted},
		{worldread.HomeActionFinish, worldread.HomeEventFashionShowCompleted},
		{worldread.HomeActionFinish, worldread.HomeEventReadingCompleted},
	} {
		if !routineWorldActionEventAllowed(pair.action, pair.event) {
			t.Errorf("WORLD action/event pair (%q,%q) is not allowed", pair.action, pair.event)
		}
	}
	if routineWorldActionEventAllowed(worldread.HomeActionFashionShow, worldread.HomeEventReadingStarted) ||
		routineWorldActionEventAllowed(worldread.HomeActionReading, worldread.HomeEventFashionShowCompleted) {
		t.Fatal("cross-kind lifecycle event was accepted for a WORLD action")
	}
}

func TestRoutineWorldNewHobbiesRequireConfirmedFinishForRecoveryAndSettlement(t *testing.T) {
	finishedAt := time.Date(2026, time.November, 17, 1, 4, 0, 0, time.UTC)
	for _, test := range []struct{ actorID, label, activityID string }{
		{"mio", "fashion", "home:mio:4"}, {"midori", "reading", "home:midori:3"},
	} {
		t.Run(test.actorID, func(t *testing.T) {
			spec, ok := routineWorldHomeHobbyFor(test.actorID, test.label)
			if !ok {
				t.Fatal("missing hobby spec")
			}
			intent := &agentroutine.Intent{ID: "intent-" + test.actorID, ActorID: test.actorID, Kind: agentroutine.ActivityHobby, Label: test.label}
			start := routineWorldOperation{
				Purpose: spec.startPurpose, Action: spec.startAction, EventKind: spec.startEvent,
				ActorID: test.actorID, Payload: worldread.HomeActionPayload{ActorID: test.actorID},
				ActivityID: test.activityID, ActivityKind: spec.activityKind, ReceiptID: "start-receipt", ReceiptStatus: "committed",
				EventVerified: true, StartedAt: finishedAt.Add(-time.Minute), DueAt: finishedAt.Add(-time.Second),
			}
			start.Key = routineWorldOperationKey(intent.ID, test.actorID, start.Purpose)
			pending := routineProgress{World: &routineWorldProgress{Operations: []routineWorldOperation{start}}}
			if !routineProgressNeedsWorldSettlement(pending) {
				t.Fatal("confirmed start without terminal receipt was treated as settled")
			}
			terminal := routineWorldOperation{
				Purpose: spec.startPurpose + ":finish:" + test.activityID, Action: worldread.HomeActionFinish, EventKind: spec.finishEvent,
				ActorID: test.actorID, Payload: worldread.HomeActionPayload{ActorID: test.actorID, ActivityID: test.activityID},
				ActivityID: test.activityID, ActivityKind: spec.activityKind, ReceiptID: "finish-receipt", ReceiptStatus: "committed",
				EventVerified: true, EndedAt: finishedAt,
			}
			terminal.Key = routineWorldOperationKey(intent.ID, test.actorID, terminal.Purpose)
			settled := routineProgress{World: &routineWorldProgress{Operations: []routineWorldOperation{start, terminal}}}
			if routineProgressNeedsWorldSettlement(settled) {
				t.Fatal("committed exact terminal receipt still requires WORLD settlement")
			}
			got, confirmed := (&agentRoutineRuntime{}).routineVerifiedBedtimeHobbyCompletion(intent, settled)
			if !confirmed || !got.Equal(finishedAt) {
				t.Fatalf("bedtime recovery completion = %s, confirmed=%v; want %s", got, confirmed, finishedAt)
			}
		})
	}
}

func TestRoutineMirrorPoseIDsIncludeKITRepertoireWithoutDroppingExistingPoses(t *testing.T) {
	want := []string{
		"mirror-greeting", "mirror-hero", "mirror-side", "mirror-focus", "mirror-hands-on-hips",
		"mirror-over-shoulder", "mirror-open-arms", "mirror-bow", "mirror-crossed-arms", "mirror-hand-near-face",
	}
	if len(routineMirrorPoseIDs) != len(want) {
		t.Fatalf("pose count = %d, want %d", len(routineMirrorPoseIDs), len(want))
	}
	for index, poseID := range want {
		if routineMirrorPoseIDs[index] != poseID || !routineMirrorPoseID(poseID) {
			t.Errorf("pose[%d] = %q; want accepted %q", index, routineMirrorPoseIDs[index], poseID)
		}
	}
	if routineMirrorPoseLabel("mirror-crossed-arms") != "腕組み" || routineMirrorPoseLabel("mirror-hand-near-face") != "顔に手を添える" {
		t.Fatal("new KIT poses have no matched diary labels")
	}
}

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

func TestRoutineTaskSummaryMatchesConfirmedRoutinePath(t *testing.T) {
	verifiedWorld := func(action, eventKind string) *routineWorldProgress {
		return &routineWorldProgress{Operations: []routineWorldOperation{{
			Action: action, EventKind: eventKind, ReceiptID: "receipt-1", ReceiptStatus: "committed", EventVerified: true, ActivityID: "activity-1",
		}}}
	}
	tests := []struct {
		name     string
		kind     agentroutine.ActivityKind
		progress routineProgress
		want     string
		avoid    string
	}{
		{name: "logical sleep fallback", kind: agentroutine.ActivitySleep, want: "Logical sleep gate", avoid: "verified sleep"},
		{name: "WORLD sleep", kind: agentroutine.ActivitySleep, progress: routineProgress{World: verifiedWorld(worldread.HomeActionSleep, "life.home.sleep.started")}, want: "verified sleep", avoid: "no physical sleep"},
		{name: "WORLD wake", kind: agentroutine.ActivityWake, progress: routineProgress{World: verifiedWorld(worldread.HomeActionWake, "life.home.sleep.woke")}, want: "WORLD wake transition", avoid: "no physical WORLD action"},
		{name: "unverified WORLD wake fallback", kind: agentroutine.ActivityWake, progress: routineProgress{World: &routineWorldProgress{Operations: []routineWorldOperation{{
			Action: worldread.HomeActionWake, EventKind: "life.home.sleep.woke", ReceiptStatus: "committed", ActivityID: "activity-1",
		}}}}, want: "Logical wake gate", avoid: "verified WORLD wake"},
		{name: "WebGather hobby", kind: agentroutine.ActivityHobby, progress: routineProgress{SearchResult: "public source", HobbyText: "PRIVATE_NOTE_SENTINEL"}, want: "WebGather", avoid: "PRIVATE_NOTE_SENTINEL"},
		{name: "WORLD exercise", kind: agentroutine.ActivityHobby, progress: routineProgress{World: verifiedWorld(worldread.HomeActionFinish, "life.home.exercise.completed")}, want: "WORLD exercise", avoid: "WebGather"},
		{name: "WORLD mirror pose", kind: agentroutine.ActivityHobby, progress: routineProgress{World: verifiedWorld(worldread.HomeActionFinish, "life.home.mirror_pose.completed")}, want: "mirror pose", avoid: "WebGather"},
		{name: "media playback", kind: agentroutine.ActivityHobby, progress: routineProgress{Media: &routineMediaProgress{Verified: true, Title: "PRIVATE_MEDIA_TITLE_SENTINEL"}}, want: "verified bounded playback", avoid: "PRIVATE_MEDIA_TITLE_SENTINEL"},
		{name: "observed relaxation", kind: agentroutine.ActivityRelax, progress: routineProgress{World: &routineWorldProgress{RelaxRoomID: "living", RelaxObservedAt: time.Now().UTC()}}, want: "Confirmed quiet time", avoid: "no room activity"},
		{name: "unobserved relaxation", kind: agentroutine.ActivityRelax, want: "no room activity was confirmed", avoid: "Confirmed quiet time"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := routineTaskSummary(test.kind, test.progress)
			if !strings.Contains(got, test.want) || strings.Contains(got, test.avoid) {
				t.Fatalf("routineTaskSummary() = %q, want %q and not %q", got, test.want, test.avoid)
			}
		})
	}
}

func TestRoutineWorldWakePreparedOperationCanRecoverMissingReceipt(t *testing.T) {
	state := agentroutine.State{Pending: &agentroutine.Intent{ID: "intent-1", ActorID: "aka"}}
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
	state.Pending.ActorID = "mio"
	if routineWorldWakeOperationCanReplay(state, progress, "aka", operation) {
		t.Fatal("wake recovery accepted a binding outside the current pending Actor")
	}
	state.Pending.ActorID = "aka"
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
