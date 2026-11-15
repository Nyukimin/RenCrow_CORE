package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

func TestAgentRoutineNativeDaytimeRestAcceptance(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skipf("native acceptance is scoped to Mac 207 arm64; got %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if strings.TrimSpace(os.Getenv("REN_CROW_WORLD_BIN")) == "" &&
		strings.TrimSpace(os.Getenv("REN_CROW_WORLD_LAYOUT")) == "" &&
		strings.TrimSpace(os.Getenv("REN_CROW_TEST_TMP")) == "" {
		t.Skip("set REN_CROW_TEST_TMP, REN_CROW_WORLD_BIN, and REN_CROW_WORLD_LAYOUT to opt in to native owner acceptance")
	}
	binary := strings.TrimSpace(os.Getenv("REN_CROW_WORLD_BIN"))
	layoutSource := strings.TrimSpace(os.Getenv("REN_CROW_WORLD_LAYOUT"))
	if binary == "" || layoutSource == "" || strings.TrimSpace(os.Getenv("REN_CROW_TEST_TMP")) == "" {
		t.Fatal("REN_CROW_TEST_TMP, REN_CROW_WORLD_BIN, and REN_CROW_WORLD_LAYOUT must all identify the isolated owner acceptance inputs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	layoutBytes, err := os.ReadFile(layoutSource)
	if err != nil {
		t.Fatalf("read HOUSE layout: %v", err)
	}
	var layout nativeRoutineHomeLayout
	if err := json.Unmarshal(layoutBytes, &layout); err != nil {
		t.Fatalf("decode HOUSE layout: %v", err)
	}
	if len(layout.RoomAnchors) == 0 || len(layout.PrivateRooms) != len(nativeRoutineActors) || len(layout.SceneSHA256) != 64 {
		t.Fatalf("HOUSE layout incomplete: rooms=%d private_rooms=%d scene_digest_bytes=%d", len(layout.RoomAnchors), len(layout.PrivateRooms), len(layout.SceneSHA256))
	}
	worldRoot, worldConfigPath, worldURL, readerToken, err := nativeRoutineWorldConfig(t, layoutBytes, layout)
	if err != nil {
		t.Fatalf("prepare isolated WORLD config: %v", err)
	}
	process, err := startNativeRoutineWorld(ctx, binary, worldConfigPath, worldRoot, worldURL, readerToken)
	if err != nil {
		t.Fatalf("start isolated WORLD: %v", err)
	}
	t.Cleanup(func() {
		if process != nil {
			if err := stopNativeRoutineWorld(process); err != nil {
				t.Errorf("stop isolated WORLD: %v", err)
			}
		}
	})
	reader, err := worldread.New(worldread.Config{
		BaseURL: worldURL, Credential: readerToken, WorldID: nativeRoutineWorldID,
		TimelineID: nativeRoutineTimelineID, TimelineEpoch: nativeRoutineEpoch,
		ActorIDs: append([]string(nil), nativeRoutineActors...), Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("create authenticated WORLD reader: %v", err)
	}
	initial, err := reader.Snapshot(ctx)
	if err != nil || initial.HomeSceneSHA256 != layout.SceneSHA256 || len(initial.Actors) != len(nativeRoutineActors) {
		t.Fatalf("initial authenticated WORLD snapshot mismatch: actors=%d scene=%q err=%v", len(initial.Actors), initial.HomeSceneSHA256, err)
	}

	workspace, err := nativeRoutineTempDir(t, "rencrow-core-daytime-rest-")
	if err != nil {
		t.Fatalf("create private CORE workspace: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(workspace); err != nil {
			t.Errorf("remove isolated CORE workspace: %v", err)
		}
	})
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(workspace, "canonical-tasks"))
	if err != nil {
		t.Fatalf("open isolated canonical Task store: %v", err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close isolated canonical Task store: %v", err)
		}
	})
	limits := taskmanager.DefaultParallelLimits()
	limits.Global, limits.PerModule = len(nativeRoutineActors), len(nativeRoutineActors)
	tasks := taskmanager.New(taskStore, limits)
	bindings, privateRooms := map[string]string{}, map[string]string{}
	for _, actorID := range nativeRoutineActors {
		bindings[actorID] = actorID
		privateRooms[actorID] = layout.PrivateRooms[actorID]
	}
	cfg := &config.Config{
		WorkspaceDir:  filepath.Join(workspace, "core-workspace"),
		LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, UserID: nativeRoutineOwner},
		Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{
			Enabled: true, WorldURL: worldURL, WorldID: nativeRoutineWorldID,
			TimelineID: nativeRoutineTimelineID, TimelineEpoch: nativeRoutineEpoch,
			WorldReadTokenFile: filepath.Join(worldRoot, "credentials", "reader.bearer"),
			WorldLifeTokenFile: filepath.Join(worldRoot, "credentials", "life.bearer"),
			WorldActors:        bindings, SharedRooms: []string{"living", "dining"}, PrivateRooms: privateRooms,
		}},
	}
	routine, err := newAgentRoutineRuntime(cfg, &Dependencies{taskManager: tasks}, nil, nil)
	if err != nil {
		t.Fatalf("construct CORE routine runtime: %v", err)
	}
	t.Cleanup(func() {
		if err := routine.Close(); err != nil {
			t.Errorf("close isolated CORE routine store: %v", err)
		}
	})
	if routine.world == nil || routine.worldLife == nil || routine.worldNow != nil {
		t.Fatal("CORE did not load distinct authenticated reader/life credentials with the owner wall clock")
	}

	loc := routine.location()
	day := time.Now().In(loc).AddDate(0, 0, 1).Format("2006-01-02")
	var clockMu sync.RWMutex
	schedulerNow := parseNativeRoutineDate(day, loc).Add(12 * time.Hour)
	routine.now = func() time.Time {
		clockMu.RLock()
		defer clockMu.RUnlock()
		return schedulerNow
	}
	for _, actorID := range nativeRoutineActors {
		state, err := routine.store.Load(ctx, actorID)
		if err != nil {
			t.Fatalf("load schedule for %s: %v", actorID, err)
		}
		sleepAt, wakeAt, scheduleErr := agentroutine.ScheduleFor(actorID, day)
		if scheduleErr != nil {
			t.Fatalf("plan sleep schedule for %s: %v", actorID, scheduleErr)
		}
		state.ActivatedOn, state.NextDiaryDate = day, day
		state.ScheduleDate, state.PlannedSleep, state.PlannedWake = day, sleepAt, wakeAt
		state.CooldownUntil, state.LogicalSleepUntil = time.Time{}, time.Time{}
		if _, err := routine.store.Save(ctx, state, state.Revision); err != nil {
			t.Fatalf("seed isolated daytime schedule for %s: %v", actorID, err)
		}
	}
	if stored, err := tasks.List(ctx, domaintask.Filter{ModuleID: agentRoutineModuleID, Limit: 20}); err != nil || len(stored) != 0 {
		t.Fatalf("initial canonical routine Tasks = %d err=%v; want no explicit work", len(stored), err)
	}

	scheduler := agentheartbeat.New(agentHeartbeatActorIDs, agentheartbeat.Sources{Idle: routine.idleSource(nil)}, nil)
	schedulerCtx, stopScheduler := context.WithCancel(ctx)
	schedulerDone := make(chan error, 1)
	go func() { schedulerDone <- scheduler.Start(schedulerCtx) }()
	schedulerStarted := true
	stopAndDrainScheduler := func() {
		if !schedulerStarted {
			return
		}
		stopScheduler()
		if err := <-schedulerDone; err != nil {
			t.Errorf("stop CORE scheduler: %v", err)
		}
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer waitCancel()
		if err := scheduler.Wait(waitCtx); err != nil {
			t.Errorf("drain CORE scheduler reservations: %v", err)
		}
		schedulerStarted = false
	}
	t.Cleanup(stopAndDrainScheduler)

	deadline := time.NewTimer(4 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		allFinished := true
		for _, actorID := range nativeRoutineActors {
			state, err := routine.store.Load(ctx, actorID)
			if err != nil {
				t.Fatalf("load completed daytime routine for %s: %v", actorID, err)
			}
			if state.Pending != nil || state.LastFinishedIntentID == "" || state.RestWindow == nil {
				allFinished = false
				break
			}
		}
		if allFinished {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for all eight daytime rest Tasks: %v", ctx.Err())
		case <-deadline.C:
			t.Fatal("timed out waiting for all eight daytime rest Tasks")
		case <-ticker.C:
		}
	}
	storedTasks, err := tasks.List(ctx, domaintask.Filter{ModuleID: agentRoutineModuleID, Limit: 20})
	if err != nil || len(storedTasks) != len(nativeRoutineActors) {
		t.Fatalf("canonical daytime Task count=%d err=%v, want one per actor", len(storedTasks), err)
	}
	taskByActor := make(map[string]domaintask.Task, len(storedTasks))
	for _, task := range storedTasks {
		if task.OwnerID != nativeRoutineOwner || task.Status != domaintask.StatusSucceeded || task.Title != routineTaskTitle(agentroutine.ActivityRelax) {
			t.Errorf("canonical daytime Task owner=%q status=%s title=%q; want owner=%q succeeded quiet time", task.OwnerID, task.Status, task.Title, nativeRoutineOwner)
		}
		taskByActor[task.Assignee] = task
		runs, runErr := tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
		if runErr != nil || len(runs) != 1 || runs[0].Status != domaintask.RunStatusSucceeded {
			t.Errorf("canonical daytime Runs actor=%s = %+v err=%v; want one succeeded Run", task.Assignee, runs, runErr)
		}
	}

	stateRevisions := make(map[string]int64, len(nativeRoutineActors))
	for _, actorID := range nativeRoutineActors {
		state, err := routine.store.Load(ctx, actorID)
		if err != nil {
			t.Fatalf("load rest window for %s: %v", actorID, err)
		}
		stateRevisions[actorID] = state.Revision
		if state.Pending != nil || state.LastFinishedIntentID == "" || state.RestWindow == nil ||
			!state.RestWindow.Until.After(state.RestWindow.ObservedAt) || !routine.routineRelaxRoomAllowed(actorID, state.RestWindow.RoomID) {
			t.Fatalf("rest checkpoint for %s = %+v; want a bounded confirmed interval in an allowed room", actorID, state)
		}
		task, ok := taskByActor[actorID]
		if !ok {
			t.Fatalf("canonical daytime Task is missing actor %s", actorID)
		}
		facts, err := routine.store.ListFacts(ctx, actorID, day)
		if err != nil || len(facts) != 1 || facts[0].Kind != agentroutine.ActivityRelax || !facts[0].Confirmed {
			t.Fatalf("confirmed daytime relaxation fact for %s = %+v err=%v; want one confirmed fact", actorID, facts, err)
		}
		if runs, err := tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID}); err != nil || len(runs) != 1 {
			t.Fatalf("routine reads changed actor %s Task/Run count: runs=%d err=%v", actorID, len(runs), err)
		}
	}
	activity, err := routine.CurrentRoutineActivity(ctx, nativeRoutineActors[0])
	if err != nil || activity.Availability != "available" || activity.OngoingActivity != "rest" {
		t.Fatalf("fresh same-room CurrentRoutineActivity for %s = %+v err=%v; want available/rest", nativeRoutineActors[0], activity, err)
	}
	for read := 0; read < 2; read++ {
		activities, err := routine.CurrentRoutineActivities(ctx, nativeRoutineActors)
		if err != nil {
			t.Fatalf("fresh batch routine projection %d: %v", read+1, err)
		}
		for _, actorID := range nativeRoutineActors {
			activity := activities[actorID]
			if activity.Availability != "available" || activity.OngoingActivity != "rest" {
				t.Errorf("fresh batch same-room projection %d for %s = %+v; want available/rest", read+1, actorID, activity)
			}
		}
	}

	beforeWakeTaskCount := len(storedTasks)
	for _, actorID := range nativeRoutineActors {
		work, found, err := routine.idleSource(nil)(ctx, actorID)
		if err != nil || found {
			t.Errorf("repeated idle discovery during rest for %s = %+v found=%t err=%v; want no new Work", actorID, work, found, err)
		}
		if err := scheduler.Wake(actorID); err != nil {
			t.Fatalf("wake CORE scheduler scan for %s during rest: %v", actorID, err)
		}
	}
	wakeDeadline := time.NewTimer(10 * time.Second)
	wakeTicker := time.NewTicker(50 * time.Millisecond)
	defer wakeDeadline.Stop()
	defer wakeTicker.Stop()
	for {
		activities, ready := scheduler.LivingSnapshot(ctx, nil)
		allWaiting := ready && len(activities) == len(nativeRoutineActors)
		for _, activity := range activities {
			allWaiting = allWaiting && activity.State == "waiting"
		}
		if allWaiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out draining post-rest Wake scans: %v", ctx.Err())
		case <-wakeDeadline.C:
			t.Fatal("timed out draining post-rest Wake scans")
		case <-wakeTicker.C:
		}
	}
	stopAndDrainScheduler()
	afterWakeTasks, err := tasks.List(ctx, domaintask.Filter{ModuleID: agentRoutineModuleID, Limit: 20})
	if err != nil || len(afterWakeTasks) != beforeWakeTaskCount {
		t.Fatalf("Wake during rest changed canonical Task count from %d to %d (err=%v); want no duplicate activity", beforeWakeTaskCount, len(afterWakeTasks), err)
	}
	for _, actorID := range nativeRoutineActors {
		state, err := routine.store.Load(ctx, actorID)
		if err != nil || state.Revision != stateRevisions[actorID] || state.Pending != nil || state.RestWindow == nil {
			t.Errorf("Wake/repeat read changed %s routine state: revision=%d want=%d pending=%+v rest=%+v err=%v", actorID, state.Revision, stateRevisions[actorID], state.Pending, state.RestWindow, err)
		}
	}
	t.Logf("native daytime acceptance: 8 actors, real WORLD=%s, shared-room moves/observations, 8 succeeded relaxation Tasks and Runs, confirmed bounded rest windows, repeated fresh rest reads and scheduler Wake added no Tasks", nativeRoutineWorldID)
}
