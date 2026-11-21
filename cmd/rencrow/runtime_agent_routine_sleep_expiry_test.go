package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestAgentRoutinePendingSleepRetryExpiresAtTwoWithoutReadMutation(t *testing.T) {
	ctx := context.Background()
	loc := time.FixedZone(agentroutine.JST, 9*60*60)
	date := "2026-11-15"
	sleepAt, _, err := agentroutine.ScheduleFor("aka", date)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, time.November, 15, 1, 59, 0, 0, loc)
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	runtime, _ := newRoutineAdmissionRuntime(t, taskStore, &clock)
	if _, err := runtime.store.EnsureSchedule(ctx, "aka", date); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	progress := routineProgress{Actors: []routineActorProgress{{ActorID: "aka", TaskID: modulecore.NewTaskID()}}}
	state := draftRoutineBedtimeIntent(t, runtime, "aka", date, agentroutine.ActivitySleep,
		sleepAt, time.Date(2026, time.November, 15, 2, 14, 0, 0, loc), progress, "intent-sleep-retry")
	beforeRevision := state.Revision
	idle := runtime.idleSource(nil)
	if work, found, err := idle(ctx, "aka"); err != nil || found {
		t.Fatalf("01:59 sleep retry discovery = %+v found=%t err=%v, want RetryAfter to remain active", work, found, err)
	}
	afterRead, err := runtime.store.Load(ctx, "aka")
	if err != nil || afterRead.Revision != beforeRevision || afterRead.Pending == nil || afterRead.Pending.ID != "intent-sleep-retry" ||
		!afterRead.Pending.RetryAfter.Equal(time.Date(2026, time.November, 15, 2, 14, 0, 0, loc)) {
		t.Fatalf("01:59 read-only state = %+v err=%v, want unchanged pending sleep", afterRead, err)
	}

	clock = time.Date(2026, time.November, 15, 2, 0, 0, 0, loc)
	work, found, err := idle(ctx, "aka")
	if err != nil || !found || !strings.HasSuffix(work.ID, ":sleep") {
		t.Fatalf("02:00 sleep cleanup discovery = %+v found=%t err=%v, want exact pending sleep despite future RetryAfter", work, found, err)
	}
	afterDiscovery, err := runtime.store.Load(ctx, "aka")
	if err != nil || afterDiscovery.Revision != beforeRevision || afterDiscovery.Pending == nil || afterDiscovery.Pending.ID != "intent-sleep-retry" {
		t.Fatalf("02:00 read-only state = %+v err=%v, want unchanged pending sleep", afterDiscovery, err)
	}
}

func TestRoutinePendingSleepDeadlineUsesIntentDateNotCreatedAt(t *testing.T) {
	loc := time.FixedZone(agentroutine.JST, 9*60*60)
	intent := &agentroutine.Intent{
		Kind: agentroutine.ActivitySleep, Date: "2026-11-15",
		CreatedAt: time.Date(2026, time.November, 15, 2, 30, 0, 0, loc),
	}
	want := time.Date(2026, time.November, 15, 2, 0, 0, 0, loc)
	if got := routinePendingSleepDeadline(intent, loc); !got.Equal(want) {
		t.Fatalf("sleep deadline with out-of-window CreatedAt = %v, want intent-date boundary %v", got, want)
	}
}

func TestRoutineMissedSleepRecoveryStillValidatesActualWORLDStartedAt(t *testing.T) {
	ctx := context.Background()
	loc := time.FixedZone(agentroutine.JST, 9*60*60)
	date := "2026-11-15"
	sleepAt, _, err := agentroutine.ScheduleFor("aka", date)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, time.November, 15, 2, 0, 0, 0, loc)
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	runtime, _ := newRoutineAdmissionRuntime(t, taskStore, &clock)
	if _, err := runtime.store.EnsureSchedule(ctx, "aka", date); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	progress := routineProgress{Actors: []routineActorProgress{{ActorID: "aka", TaskID: modulecore.NewTaskID()}},
		World: &routineWorldProgress{Operations: []routineWorldOperation{{
			Purpose: "sleep-start", Key: routineWorldOperationKey("intent-late-sleep", "aka", "sleep-start"),
			Action: worldread.HomeActionSleep, EventKind: "life.home.sleep.started", ActorID: "aka",
			Payload: worldread.HomeActionPayload{ActorID: "aka"}, ReceiptID: "receipt-late-sleep", ReceiptStatus: "committed",
			EventVerified: true, ActivityID: "sleep-late", ActivityKind: "sleep", StartedAt: clock,
		}}}}
	state := draftRoutineBedtimeIntent(t, runtime, "aka", date, agentroutine.ActivitySleep,
		sleepAt, clock.Add(15*time.Minute), progress, "intent-late-sleep")
	if _, err := runtime.routineMissedSleepCanResume(ctx, state, progress); err == nil || !strings.Contains(err.Error(), "outside its original") {
		t.Fatalf("recovery for WORLD sleep started at 02:00 = %v, want original-window rejection", err)
	}
}

func TestAgentRoutineRejectedBedMoveExpiresWithoutInventingSleep(t *testing.T) {
	ctx := context.Background()
	loc := time.FixedZone(agentroutine.JST, 9*60*60)
	date := "2026-11-15"
	sleepAt, _, err := agentroutine.ScheduleFor("aka", date)
	if err != nil {
		t.Fatal(err)
	}
	clock := sleepAt
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
	if _, err := runtime.store.EnsureSchedule(ctx, "aka", date); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	moveCommands, sleepCommands := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/snapshot") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schema_version": 1, "world_id": "world-home", "timeline_id": "timeline-home", "timeline_epoch": "epoch-home",
				"state_seq": 0, "state_hash": strings.Repeat("a", 64),
				"state": map[string]any{"actors": map[string]any{"aka-world": map[string]any{"room_id": "aka-hall"}}},
			})
			return
		}
		if request.Method != http.MethodPost {
			http.Error(w, "unexpected request", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Action string `json:"action"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		switch body.Action {
		case worldread.HomeActionMove:
			moveCommands++
		case worldread.HomeActionSleep:
			sleepCommands++
		default:
			http.Error(w, "unexpected action", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"schema_version":1,"receipt":{"receipt_id":"receipt-move-rejected","status":"rejected","reason":"unknown_resource","event_ids":[],"first_seq":0,"last_seq":0,"state_seq":0,"recorded_at":"2026-11-15T00:59:00Z"}}`)
	}))
	defer server.Close()
	life, err := worldread.NewLife(worldread.LifeConfig{
		BaseURL: server.URL, Credential: "life-token", WorldID: "world-home", TimelineID: "timeline-home",
		TimelineEpoch: "epoch-home", ActorIDs: []string{"aka"}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("create life client: %v", err)
	}
	read, err := worldread.New(worldread.Config{
		BaseURL: server.URL, Credential: "reader-token", WorldID: "world-home", TimelineID: "timeline-home",
		TimelineEpoch: "epoch-home", ActorIDs: []string{"aka-world"}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	runtime.world, runtime.worldLife = read, life
	runtime.worldActorIDs = map[string]string{"aka-world": "aka"}
	runtime.cfg = &config.Config{Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{
		WorldActors: map[string]string{"aka": "aka-world"}, PrivateRooms: map[string]string{"aka": "aka-bedroom"},
	}}}
	progress := routineProgress{Actors: []routineActorProgress{{ActorID: "aka", TaskID: modulecore.NewTaskID()}}}
	state := draftRoutineBedtimeIntent(t, runtime, "aka", date, agentroutine.ActivitySleep,
		sleepAt, time.Date(2026, time.November, 15, 2, 14, 0, 0, loc), progress, "intent-rejected-bed-move")
	taskID := modulecore.TaskID(progress.Actors[0].TaskID)
	if _, err := tasks.Create(ctx, domaintask.Task{
		TaskID: taskID, Title: routineTaskTitle(agentroutine.ActivitySleep), ModuleID: agentRoutineModuleID, OwnerID: runtime.ownerID,
		Assignee: "aka", Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow,
		InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true,
	}, domaintask.SharedRoleContext{TaskID: taskID, UserIntent: "CORE routine step", CurrentPlan: routineTaskTitle(agentroutine.ActivitySleep)}); err != nil {
		t.Fatalf("create exact pending Task: %v", err)
	}
	state, progress, _, err = runtime.performRoutineWorldAction(ctx, state, progress, "sleep-bed:move", worldread.HomeActionMove,
		"life.home.move.started", worldread.HomeActionPayload{ActorID: "aka", RoomID: "aka-bedroom", SlotID: "bed-aka"})
	if !errors.Is(err, worldread.ErrCommandRejected) || state.Pending == nil {
		t.Fatalf("durable move rejection = state=%+v progress=%+v err=%v, want rejected receipt checkpoint", state, progress, err)
	}
	if len(progress.World.Operations) != 1 || progress.World.Operations[0].ReceiptID != "receipt-move-rejected" ||
		progress.World.Operations[0].ReceiptStatus != "rejected" || progress.World.Operations[0].ReceiptReason != "unknown_resource" {
		t.Fatalf("durable WORLD move checkpoint = %+v, want exact unknown_resource rejection", progress.World)
	}
	clock = time.Date(2026, time.November, 15, 2, 0, 0, 0, loc)
	work, found, err := runtime.idleSource(nil)(ctx, "aka")
	if err != nil || !found {
		t.Fatalf("02:00 rejected sleep cleanup discovery = %+v found=%t err=%v, want cleanup despite retry", work, found, err)
	}
	if err := work.Run(ctx, nil); err != nil {
		t.Fatalf("cancel missed unstarted sleep: %v", err)
	}
	after, err := runtime.store.Load(ctx, "aka")
	if err != nil || after.Pending != nil || after.LastFinishedIntentID != "intent-rejected-bed-move" ||
		!after.LogicalSleepUntil.IsZero() || !after.CooldownUntil.IsZero() {
		t.Fatalf("missed sleep after cleanup = %+v err=%v, want terminal intent without logical sleep or cooldown", after, err)
	}
	task, err := tasks.Get(ctx, taskID)
	if err != nil || task.Status != domaintask.StatusCancelled {
		t.Fatalf("exact missed-sleep Task = %+v err=%v, want cancelled", task, err)
	}
	facts, err := runtime.store.ListFacts(ctx, "aka", date)
	if err != nil || len(facts) != 0 {
		t.Fatalf("missed-sleep facts = %+v err=%v, want no invented sleep fact", facts, err)
	}
	clock = time.Date(2026, time.November, 15, 9, 0, 0, 0, loc)
	if work, found, err := runtime.idleSource(nil)(ctx, "aka"); err != nil || found && strings.HasSuffix(work.ID, ":sleep") {
		t.Fatalf("09:00 after missed sleep = %+v found=%t err=%v, want no late pajamas activity", work, found, err)
	}
	if moveCommands != 1 || sleepCommands != 0 {
		t.Fatalf("WORLD actions after rejection and expiry = move:%d sleep:%d, want one rejected move and no sleep command", moveCommands, sleepCommands)
	}
}

func TestAgentRoutineExpiredSleepFinishesExactActiveWORLDCheckpoint(t *testing.T) {
	ctx := context.Background()
	loc := time.FixedZone(agentroutine.JST, 9*60*60)
	date := "2026-11-15"
	sleepAt, wakeAt, err := agentroutine.ScheduleFor("aka", date)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, time.November, 15, 2, 0, 0, 0, loc)
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
	if _, err := runtime.store.EnsureSchedule(ctx, "aka", date); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	start := sleepAt
	operation := routineWorldOperation{
		Purpose: "sleep-start", Key: routineWorldOperationKey("intent-active-sleep", "aka", "sleep-start"),
		Action: worldread.HomeActionSleep, EventKind: "life.home.sleep.started", ActorID: "aka",
		Payload: worldread.HomeActionPayload{ActorID: "aka"}, ReceiptID: "receipt-sleep-start", ReceiptStatus: "committed",
		TransactionID: "txn-sleep-start", EventIDs: []string{"evt-sleep-start"}, FirstSeq: 4, LastSeq: 4, StateSeq: 4,
		RecordedAt: start, EventVerified: true, ActivityID: "sleep-activity-aka", ActivityKind: "sleep",
		RoomID: "aka-bedroom", SlotID: "bed-aka", StartedAt: start,
	}
	progress := routineProgress{Actors: []routineActorProgress{{ActorID: "aka", TaskID: modulecore.NewTaskID()}},
		World: &routineWorldProgress{Operations: []routineWorldOperation{operation}}}
	_ = draftRoutineBedtimeIntent(t, runtime, "aka", date, agentroutine.ActivitySleep,
		sleepAt, time.Date(2026, time.November, 15, 2, 14, 0, 0, loc), progress, "intent-active-sleep")

	sleepCommands := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			sleepCommands++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = fmt.Fprint(w, `{"schema_version":1,"receipt":{"receipt_id":"receipt-unexpected-sleep","status":"rejected","reason":"unexpected_replay","event_ids":[],"first_seq":0,"last_seq":0,"state_seq":4,"recorded_at":"2026-11-15T02:00:00Z"}}`)
			return
		}
		if request.Method != http.MethodGet || !strings.HasSuffix(request.URL.Path, "/snapshot") {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"schema_version": 1, "world_id": "world-home", "timeline_id": "timeline-home", "timeline_epoch": "epoch-home",
			"state_seq": 5, "state_hash": strings.Repeat("a", 64),
			"state": map[string]any{"actors": map[string]any{"aka-world": map[string]any{
				"room_id": "aka-bedroom", "slot_id": "bed-aka", "activity_id": "sleep-activity-aka", "activity_kind": "sleep",
				"activity_started_at": start.Format(time.RFC3339Nano),
			}}},
		})
	}))
	defer server.Close()
	read, err := worldread.New(worldread.Config{
		BaseURL: server.URL, Credential: "reader-token", WorldID: "world-home", TimelineID: "timeline-home",
		TimelineEpoch: "epoch-home", ActorIDs: []string{"aka-world"}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	life, err := worldread.NewLife(worldread.LifeConfig{
		BaseURL: server.URL, Credential: "life-token", WorldID: "world-home", TimelineID: "timeline-home",
		TimelineEpoch: "epoch-home", ActorIDs: []string{"aka"}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("create life client: %v", err)
	}
	runtime.world, runtime.worldLife = read, life
	runtime.worldActorIDs = map[string]string{"aka-world": "aka"}
	runtime.cfg = &config.Config{Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{
		WorldActors: map[string]string{"aka": "aka-world"}, PrivateRooms: map[string]string{"aka": "aka-bedroom"},
	}}}
	work, found, err := runtime.idleSource(nil)(ctx, "aka")
	if err != nil || !found {
		t.Fatalf("02:00 active-sleep recovery discovery = %+v found=%t err=%v, want exact pending intent", work, found, err)
	}
	if err := work.Run(ctx, nil); err != nil {
		t.Fatalf("complete exact active sleep checkpoint: %v", err)
	}
	current, err := tasks.SnapshotCurrentActivities(ctx, []string{"aka"})
	if err != nil {
		t.Fatalf("snapshot sleep successor after exact WORLD receipt: %v", err)
	}
	sleepTask := current["aka"].Task
	if sleepTask.Activity == nil || sleepTask.Activity.Kind != domaintask.ActivityKindSleep ||
		sleepTask.Activity.Phase != "sleeping" || !sleepTask.Activity.Passive || sleepTask.Timing == nil ||
		sleepTask.Timing.DeadlineAt == nil || !sleepTask.Timing.DeadlineAt.Equal(wakeAt) {
		t.Fatalf("current Task after WORLD-confirmed sleep start = %+v, want passive sleeping Task through planned wake %s", sleepTask, wakeAt)
	}
	after, err := runtime.store.Load(ctx, "aka")
	if err != nil || after.Pending != nil || after.LastFinishedIntentID != "intent-active-sleep" || !after.LogicalSleepUntil.Equal(wakeAt) {
		t.Fatalf("active sleep checkpoint after recovery = %+v err=%v, want existing sleep gate through planned wake", after, err)
	}
	taskID := modulecore.TaskID(progress.Actors[0].TaskID)
	task, err := tasks.Get(ctx, taskID)
	if err != nil || task.Status != domaintask.StatusSucceeded {
		t.Fatalf("exact active-sleep Task = %+v err=%v, want succeeded", task, err)
	}
	if sleepCommands != 0 {
		t.Fatalf("active sleep recovery issued %d new sleep commands, want none", sleepCommands)
	}
	if work, found, err := runtime.idleSource(nil)(ctx, "aka"); err != nil || found {
		t.Fatalf("02:00 completed sleep discovery = %+v found=%t err=%v, want logical sleep gate to suppress work", work, found, err)
	}
	clock = wakeAt
	work, found, err = runtime.idleSource(nil)(ctx, "aka")
	if err != nil || !found || !strings.HasPrefix(work.ID, "activity-expiry:aka:") || work.Run == nil {
		t.Fatalf("planned sleep expiry discovery = %+v found=%t err=%v, want canonical expiry Work before wake selection", work, found, err)
	}
	if err := work.Run(ctx, nil); err != nil {
		t.Fatalf("expire planned sleep Task at its persisted deadline: %v", err)
	}
	expiredSleep, err := tasks.Get(ctx, sleepTask.TaskID)
	if err != nil || expiredSleep.Status != domaintask.StatusSucceeded {
		t.Fatalf("sleep Task after deadline owner transition = %+v err=%v, want the exact passive interval closed", expiredSleep, err)
	}
	current, err = tasks.SnapshotCurrentActivities(ctx, []string{"aka"})
	if err != nil || current["aka"].Task.Activity == nil ||
		current["aka"].Task.Activity.Kind != domaintask.ActivityKindEvaluating {
		t.Fatalf("current activity after sleep interval expiry = %+v err=%v, want evaluating before wake selection", current["aka"].Task.Activity, err)
	}
	work, found, err = runtime.idleSource(nil)(ctx, "aka")
	if err != nil || !found || !strings.HasSuffix(work.ID, ":wake") {
		t.Fatalf("planned wake discovery after sleep Task expiry = %+v found=%t err=%v, want legitimate wake intent", work, found, err)
	}
}

func TestAgentRoutineWorldSleepStartBlockedWhenBedArrivalCrossesTwo(t *testing.T) {
	ctx := context.Background()
	loc := time.FixedZone(agentroutine.JST, 9*60*60)
	date := "2026-11-15"
	sleepAt, _, err := agentroutine.ScheduleFor("aka", date)
	if err != nil {
		t.Fatal(err)
	}
	two := time.Date(2026, time.November, 15, 2, 0, 0, 0, loc)
	clock := two.Add(-time.Minute)
	due := two
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
	if _, err := runtime.store.EnsureSchedule(ctx, "aka", date); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	intentID := "intent-bed-arrival-crosses-two"
	movePurpose := "sleep-bed:move"
	finishPurpose := "sleep-bed:finish:move-aka-1"
	scene := strings.Repeat("b", 64)
	progress := routineProgress{Actors: []routineActorProgress{{ActorID: "aka", TaskID: modulecore.NewTaskID()}}, World: &routineWorldProgress{Operations: []routineWorldOperation{
		{
			Purpose: movePurpose, Key: routineWorldOperationKey(intentID, "aka", movePurpose), Action: worldread.HomeActionMove,
			EventKind: "life.home.move.started", ActorID: "aka", Payload: worldread.HomeActionPayload{ActorID: "aka", RoomID: "aka-bedroom", SlotID: "bed-aka"},
			ReceiptID: "receipt-move-start", ReceiptStatus: "committed", TransactionID: "txn-move-start", EventIDs: []string{"evt-move-start"},
			FirstSeq: 4, LastSeq: 4, StateSeq: 4, RecordedAt: two.Add(-time.Minute), EventVerified: true,
			ActivityID: "move-aka-1", ActivityKind: "move", TargetRoomID: "aka-bedroom",
			RouteID: "route-aka-1", SceneSHA256: scene, StartedAt: two.Add(-2 * time.Minute), DueAt: due,
		},
		{
			Purpose: finishPurpose, Key: routineWorldOperationKey(intentID, "aka", finishPurpose), Action: worldread.HomeActionFinish,
			EventKind: "life.home.move.arrived", ActorID: "aka", Payload: worldread.HomeActionPayload{ActorID: "aka", ActivityID: "move-aka-1"},
			ReceiptID: "receipt-move-finish", ReceiptStatus: "committed", TransactionID: "txn-move-finish", EventIDs: []string{"evt-move-finish"},
			FirstSeq: 5, LastSeq: 5, StateSeq: 5, RecordedAt: two, EventVerified: true,
			ActivityID: "move-aka-1", ActivityKind: "move", RoomID: "aka-bedroom", SlotID: "bed-aka", EndedAt: due,
		},
	}}}
	state := draftRoutineBedtimeIntent(t, runtime, "aka", date, agentroutine.ActivitySleep,
		sleepAt, two.Add(15*time.Minute), progress, intentID)

	snapshotCalls, sleepCommands := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			sleepCommands++
			http.Error(w, "sleep action must not be sent after two", http.StatusConflict)
			return
		}
		if request.Method != http.MethodGet || !strings.HasSuffix(request.URL.Path, "/snapshot") {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		snapshotCalls++
		if snapshotCalls == 1 {
			clock = two // The already-running bed move crosses the 02:00 boundary during WORLD I/O.
		}
		actor := map[string]any{"room_id": "aka-hall", "activity_id": "move-aka-1", "activity_kind": "move",
			"activity_started_at": two.Add(-2 * time.Minute).Format(time.RFC3339Nano), "activity_due_at": due.Format(time.RFC3339Nano),
			"target_room_id": "aka-bedroom", "target_slot_id": "bed-aka", "move_route_id": "route-aka-1", "home_scene_sha256": scene}
		stateSeq := uint64(4)
		if snapshotCalls >= 3 {
			actor = map[string]any{"room_id": "aka-bedroom", "slot_id": "bed-aka"}
			stateSeq = 5
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"schema_version": 1, "world_id": "world-home", "timeline_id": "timeline-home", "timeline_epoch": "epoch-home",
			"state_seq": stateSeq, "state_hash": strings.Repeat("a", 64),
			"home_scene_sha256": scene, "state": map[string]any{"actors": map[string]any{"aka-world": actor}},
		})
	}))
	defer server.Close()
	read, err := worldread.New(worldread.Config{
		BaseURL: server.URL, Credential: "reader-token", WorldID: "world-home", TimelineID: "timeline-home",
		TimelineEpoch: "epoch-home", ActorIDs: []string{"aka-world"}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	life, err := worldread.NewLife(worldread.LifeConfig{
		BaseURL: server.URL, Credential: "life-token", WorldID: "world-home", TimelineID: "timeline-home",
		TimelineEpoch: "epoch-home", ActorIDs: []string{"aka"}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("create life client: %v", err)
	}
	runtime.world, runtime.worldLife = read, life
	runtime.worldActorIDs = map[string]string{"aka-world": "aka"}
	runtime.worldNow = func() time.Time { return due.UTC() }
	runtime.cfg = &config.Config{Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{
		WorldActors: map[string]string{"aka": "aka-world"}, PrivateRooms: map[string]string{"aka": "aka-bedroom"},
	}}}
	taskID := modulecore.TaskID(progress.Actors[0].TaskID)
	_, run, err := tasks.CreateAndStartActivityRun(ctx, domaintask.Task{
		TaskID: taskID, Title: routineTaskTitle(agentroutine.ActivitySleep), ModuleID: agentRoutineModuleID,
		OwnerID: runtime.ownerID, Assignee: "aka", Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow,
		InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true,
	}, agentRoutineTaskActivity(state.Pending, "aka"), domaintask.SharedRoleContext{
		TaskID: taskID, UserIntent: "CORE routine sleep activity", CurrentPlan: "complete the exact sleep boundary",
	})
	if err != nil {
		t.Fatalf("create canonical routine Task/Run for persisted WORLD movement: %v", err)
	}
	progress.Actors[0].RunID = run.RunID
	state, progress, err = runtime.saveProgress(ctx, state, "world_move_checkpoint", progress)
	if err != nil {
		t.Fatalf("save exact active routine Run checkpoint: %v", err)
	}
	_, _, err = runtime.executeRoutineWorldActivity(ctx, nil, state, progress)
	if err == nil || !strings.Contains(err.Error(), "02:00") {
		t.Fatalf("sleep action after bed arrival crossed 02:00 = %v, want a boundary error", err)
	}
	if snapshotCalls < 3 || sleepCommands != 0 {
		t.Fatalf("WORLD calls after crossing boundary = snapshots:%d sleep:%d, want move settlement and no sleep command", snapshotCalls, sleepCommands)
	}
}

func TestAgentRoutineSleepPreSendBoundaryReleasesOnlyNewPreparedOperation(t *testing.T) {
	ctx := context.Background()
	loc := time.FixedZone(agentroutine.JST, 9*60*60)
	date := "2026-11-15"
	sleepAt, _, err := agentroutine.ScheduleFor("aka", date)
	if err != nil {
		t.Fatal(err)
	}
	two := time.Date(2026, time.November, 15, 2, 0, 0, 0, loc)
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(w, "unexpected request", http.StatusMethodNotAllowed)
			return
		}
		posts++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"schema_version":1,"receipt":{"receipt_id":"receipt-unexpected-sleep","status":"rejected","reason":"unexpected_send","event_ids":[],"first_seq":0,"last_seq":0,"state_seq":0,"recorded_at":"2026-11-15T02:00:00Z"}}`)
	}))
	defer server.Close()
	read, err := worldread.New(worldread.Config{
		BaseURL: server.URL, Credential: "reader-token", WorldID: "world-home", TimelineID: "timeline-home",
		TimelineEpoch: "epoch-home", ActorIDs: []string{"aka-world"}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	life, err := worldread.NewLife(worldread.LifeConfig{
		BaseURL: server.URL, Credential: "life-token", WorldID: "world-home", TimelineID: "timeline-home",
		TimelineEpoch: "epoch-home", ActorIDs: []string{"aka"}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("create life client: %v", err)
	}

	for _, test := range []struct {
		name          string
		precheckpoint bool
		wantOps       int
	}{
		{name: "new operation is released", wantOps: 0},
		{name: "older ambiguous operation is retained", precheckpoint: true, wantOps: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := taskStore.Close(); err != nil {
					t.Errorf("close Task store: %v", err)
				}
			})
			clock := two
			runtime, _ := newRoutineAdmissionRuntime(t, taskStore, &clock)
			runtime.world, runtime.worldLife = read, life
			if _, err := runtime.store.EnsureSchedule(ctx, "aka", date); err != nil {
				t.Fatalf("seed schedule: %v", err)
			}
			progress := routineProgress{Actors: []routineActorProgress{{ActorID: "aka", TaskID: modulecore.NewTaskID()}}}
			if test.precheckpoint {
				purpose := "sleep-start"
				progress.World = &routineWorldProgress{Operations: []routineWorldOperation{{
					Purpose: purpose, Key: routineWorldOperationKey("intent-prepared-sleep", "aka", purpose),
					Action: worldread.HomeActionSleep, EventKind: "life.home.sleep.started", ActorID: "aka",
					Payload: worldread.HomeActionPayload{ActorID: "aka"},
				}}}
			}
			intentID := "intent-new-sleep"
			if test.precheckpoint {
				intentID = "intent-prepared-sleep"
			}
			state := draftRoutineBedtimeIntent(t, runtime, "aka", date, agentroutine.ActivitySleep,
				sleepAt, two.Add(15*time.Minute), progress, intentID)
			state, progress, _, err = runtime.performRoutineWorldAction(ctx, state, progress, "sleep-start", worldread.HomeActionSleep,
				"life.home.sleep.started", worldread.HomeActionPayload{ActorID: "aka"})
			if !errors.Is(err, errRoutineWorldSleepWindow) {
				t.Fatalf("02:00 pre-send outcome = %v, want sleep-window boundary", err)
			}
			after, err := runtime.store.Load(ctx, "aka")
			if err != nil || after.Pending == nil || after.Pending.ID != intentID {
				t.Fatalf("pending sleep after pre-send boundary = %+v err=%v", after, err)
			}
			persisted, err := decodeRoutineProgress(after.Pending.Output)
			if err != nil || persisted.World == nil && test.wantOps != 0 {
				t.Fatalf("persisted pre-send checkpoint = %+v err=%v", persisted.World, err)
			}
			gotOps := 0
			if persisted.World != nil {
				gotOps = len(persisted.World.Operations)
			}
			if gotOps != test.wantOps || len(progress.World.Operations) != test.wantOps {
				t.Fatalf("WORLD sleep operations after pre-send boundary = persisted:%d returned:%d, want %d", gotOps, len(progress.World.Operations), test.wantOps)
			}
			if test.precheckpoint && state.Pending.Step != "planned" {
				t.Fatalf("older ambiguous operation checkpoint step = %q, want unchanged", state.Pending.Step)
			}
			if !test.precheckpoint && after.Pending.Step != "world_sleep_start_not_sent" {
				t.Fatalf("new unsent operation checkpoint step = %q, want explicit not-sent receipt", after.Pending.Step)
			}
		})
	}
	if posts != 0 {
		t.Fatalf("pre-send boundary issued %d WORLD commands, want zero", posts)
	}
}
