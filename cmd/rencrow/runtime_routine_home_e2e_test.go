package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

const (
	nativeRoutineWorldID    = "house-105"
	nativeRoutineTimelineID = "core-routine-native"
	nativeRoutineEpoch      = "native-e2e-v1"
	nativeRoutineOwner      = "rencrow-world"
)

var nativeRoutineActors = []string{"mio", "shiro", "kuro", "midori", "aka", "ao", "kin", "gin"}

type nativeRoutineHomeLayout struct {
	SceneSHA256  string                  `json:"scene_sha256"`
	RoomAnchors  map[string][3]float64   `json:"room_anchors"`
	PrivateRooms map[string]string       `json:"private_rooms"`
	Slots        []nativeRoutineHomeSlot `json:"slots"`
}

type nativeRoutineHomeSlot struct {
	ID     string     `json:"id"`
	Actor  string     `json:"actor_id"`
	RoomID string     `json:"room_id"`
	Anchor [3]float64 `json:"anchor"`
}

type nativeRoutineWorldProcess struct{ cmd *exec.Cmd }
type nativeRoutineWork struct{ id string }
type nativeRoutinePlanningWorker struct{}

func (nativeRoutinePlanningWorker) ExecutePrivateWork(context.Context, conversation.TurnInput, []string) (string, error) {
	return "", errors.New("planning-only fixture worker must not execute")
}

type nativeRoutineDiscovery struct {
	work  nativeRoutineWork
	found bool
	err   error
}

func TestAgentRoutineNativeHomeAcceptance(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skipf("native acceptance is scoped to Mac 207 arm64; got %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if strings.TrimSpace(os.Getenv("REN_CROW_WORLD_BIN")) == "" &&
		strings.TrimSpace(os.Getenv("REN_CROW_WORLD_LAYOUT")) == "" &&
		strings.TrimSpace(os.Getenv("REN_CROW_TEST_TMP")) == "" {
		t.Skip("set REN_CROW_TEST_TMP, REN_CROW_WORLD_BIN, and REN_CROW_WORLD_LAYOUT to opt in to native owner acceptance")
	}
	binary := strings.TrimSpace(os.Getenv("REN_CROW_WORLD_BIN"))
	layoutSource := strings.TrimSpace(os.Getenv("REN_CROW_WORLD_LAYOUT"))
	if binary == "" || layoutSource == "" {
		t.Fatal("REN_CROW_WORLD_BIN and REN_CROW_WORLD_LAYOUT must point to the isolated owner binary and HOUSE layout")
	}
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
	readClient, err := worldread.New(worldread.Config{
		BaseURL: worldURL, Credential: readerToken, WorldID: nativeRoutineWorldID,
		TimelineID: nativeRoutineTimelineID, TimelineEpoch: nativeRoutineEpoch,
		ActorIDs: append([]string(nil), nativeRoutineActors...), Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("create authenticated reader: %v", err)
	}
	initial, err := readClient.Snapshot(ctx)
	if err != nil || initial.HomeSceneSHA256 != layout.SceneSHA256 || len(initial.Actors) != len(nativeRoutineActors) {
		t.Fatalf("initial authenticated WORLD snapshot mismatch: actors=%d scene=%q err=%v", len(initial.Actors), initial.HomeSceneSHA256, err)
	}

	workspace, err := nativeRoutineTempDir(t, "rencrow-core-workspace-")
	if err != nil {
		t.Fatalf("create private CORE workspace: %v", err)
	}
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(workspace, "canonical-tasks"))
	if err != nil {
		t.Fatalf("open isolated canonical Task store: %v", err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close isolated Task store: %v", err)
		}
	})
	tasks := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
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
			WorldActors:        bindings, SharedRooms: []string{"dining"}, PrivateRooms: privateRooms,
		}},
	}
	routine, err := newAgentRoutineRuntime(cfg, &Dependencies{taskManager: tasks}, map[string]privateTaskWorker{
		"mio": nativeRoutinePlanningWorker{},
	}, nil)
	if err != nil {
		t.Fatalf("construct CORE routine runtime: %v", err)
	}
	t.Cleanup(func() {
		if err := routine.Close(); err != nil {
			t.Errorf("close isolated CORE routine store: %v", err)
		}
	})
	if routine.world == nil || routine.worldLife == nil || routine.worldNow != nil {
		t.Fatal("CORE did not load distinct authenticated reader/life credentials with owner wall clock")
	}

	loc := routine.location()
	day := time.Now().In(loc).AddDate(0, 0, 1).Format("2006-01-02")
	for _, actorID := range nativeRoutineActors {
		state, err := routine.store.Load(ctx, actorID)
		if err != nil {
			t.Fatalf("load schedule for %s: %v", actorID, err)
		}
		state.ActivatedOn, state.NextDiaryDate = day, day
		if _, err := routine.store.Save(ctx, state, state.Revision); err != nil {
			t.Fatalf("seed isolated schedule for %s: %v", actorID, err)
		}
	}
	var clockMu sync.RWMutex
	scheduleNow := time.Time{}
	routine.now = func() time.Time {
		clockMu.RLock()
		defer clockMu.RUnlock()
		return scheduleNow
	}
	setScheduleNow := func(value time.Time) {
		clockMu.Lock()
		scheduleNow = value.In(loc)
		clockMu.Unlock()
	}
	readActor := func(actorID string) worldread.Actor {
		t.Helper()
		snapshot, err := readClient.Snapshot(ctx)
		if err != nil {
			t.Fatalf("read authenticated WORLD snapshot: %v", err)
		}
		for _, actor := range snapshot.Actors {
			if actor.ActorID == actorID {
				return actor
			}
		}
		t.Fatalf("snapshot omitted actor %q", actorID)
		return worldread.Actor{}
	}

	kuroSleepAt, kuroWakeAt, err := agentroutine.ScheduleFor("kuro", day)
	if err != nil {
		t.Fatalf("schedule isolated 3:1 selection: %v", err)
	}
	for choice := uint64(0); choice < 4; choice++ {
		state, err := routine.store.Load(ctx, "kuro")
		if err != nil {
			t.Fatalf("load persisted Kuro choice position %d: %v", choice, err)
		}
		state.ScheduleDate, state.PlannedSleep, state.PlannedWake = day, kuroSleepAt, kuroWakeAt
		state.LogicalSleepUntil, state.CooldownUntil = time.Time{}, time.Time{}
		state.ChoiceIndex = choice
		if _, err := routine.store.Save(ctx, state, state.Revision); err != nil {
			t.Fatalf("persist Kuro choice position %d: %v", choice, err)
		}
		setScheduleNow(parseNativeRoutineDate(day, loc).Add(8*time.Hour + 30*time.Minute + time.Duration(choice)*time.Minute))
		work, found, err := routine.idleSource(nil)(ctx, "kuro")
		if err != nil || !found {
			t.Fatalf("CORE idleSource choice position %d found=%v err=%v", choice, found, err)
		}
		wantSuffix := ":morning_chat"
		if choice == 3 {
			wantSuffix = ":hobby"
		}
		if !strings.HasSuffix(work.ID, wantSuffix) {
			t.Fatalf("CORE idleSource choice position %d selected %q, want suffix %q", choice, work.ID, wantSuffix)
		}
		if choice < 3 && (len(work.Participants) != 1 || work.Participants[0] != "mio") {
			t.Fatalf("CORE idleSource chat at position %d selected peers %v, want the one present fixture worker Mio", choice, work.Participants)
		}
		persisted, err := routine.store.Load(ctx, "kuro")
		if err != nil || persisted.ChoiceIndex != choice || persisted.Pending != nil {
			t.Fatalf("CORE idleSource changed uncommitted choice state: position=%d state=%+v err=%v", choice, persisted, err)
		}
	}
	t.Log("native CORE idleSource used authenticated WORLD presence for persisted Kuro 3:1 positions: chat/chat/chat/hobby; index unchanged before Run")

	sleepAt, wakeAt, err := agentroutine.ScheduleFor("aka", day)
	if err != nil {
		t.Fatalf("schedule isolated sleep/wake: %v", err)
	}
	setScheduleNow(sleepAt.Add(time.Minute))
	sleepWork := runNativeRoutineScheduler(t, routine, "aka")
	if !strings.HasSuffix(sleepWork.id, ":sleep") {
		t.Fatalf("idleSource selected %q for planned sleep", sleepWork.id)
	}
	beforeRestart := readActor("aka")
	if beforeRestart.ActivityKind != "sleep" || beforeRestart.ActivityID == "" || beforeRestart.SlotID != "bed-aka" {
		t.Fatalf("WORLD did not confirm sleep before restart: kind=%q id=%q slot=%q", beforeRestart.ActivityKind, beforeRestart.ActivityID, beforeRestart.SlotID)
	}
	coreSleep, err := routine.store.Load(ctx, "aka")
	if err != nil || !coreSleep.LogicalSleepUntil.Equal(coreSleep.PlannedWake) {
		t.Fatalf("CORE logical sleep checkpoint: state=%+v err=%v", coreSleep, err)
	}
	if err := stopNativeRoutineWorld(process); err != nil {
		t.Fatalf("stop isolated WORLD for restart: %v", err)
	}
	process = nil
	process, err = startNativeRoutineWorld(ctx, binary, worldConfigPath, worldRoot, worldURL, readerToken)
	if err != nil {
		t.Fatalf("restart isolated WORLD on the same temporary database: %v", err)
	}
	afterRestart := readActor("aka")
	if beforeRestart.ActivityID != afterRestart.ActivityID || beforeRestart.ActivityKind != afterRestart.ActivityKind ||
		beforeRestart.RoomID != afterRestart.RoomID || beforeRestart.SlotID != afterRestart.SlotID ||
		!beforeRestart.ActivityStartedAt.Equal(afterRestart.ActivityStartedAt) {
		t.Fatalf("WORLD sleep did not persist across restart: before=%+v after=%+v", beforeRestart, afterRestart)
	}
	wakeFeed, err := readClient.ReadFeed(ctx, 0)
	if err != nil {
		t.Fatalf("read authenticated WORLD feed before wake: %v", err)
	}
	for _, event := range wakeFeed.Events {
		if event.Kind != "life.home.sleep.started" {
			continue
		}
		var fields struct {
			ActorID      string    `json:"actor_id"`
			ActivityID   string    `json:"activity_id"`
			ActivityKind string    `json:"activity_kind"`
			RoomID       string    `json:"room_id"`
			SlotID       string    `json:"slot_id"`
			StartedAt    time.Time `json:"started_at"`
			LayoutSHA256 string    `json:"layout_sha256"`
			SceneSHA256  string    `json:"scene_sha256"`
		}
		if json.Unmarshal(event.Payload, &fields) != nil || fields.ActorID != "aka" {
			continue
		}
		t.Logf("WORLD wake binding diagnostic: event=%s event_id=%s seq=%d feed_state_seq=%d actor=%s activity=%s kind=%s event_room=%q active_room=%q event_slot=%q active_slot=%q event_started_at=%s active_started_at=%s recorded_at=%s recorded_minus_start=%s room_match=%t slot_match=%t start_match=%t layout_digest_valid=%t scene_digest_valid=%t",
			event.Kind, event.EventID, event.Seq, wakeFeed.Snapshot.StateSeq, fields.ActorID, fields.ActivityID, fields.ActivityKind,
			fields.RoomID, afterRestart.RoomID, fields.SlotID, afterRestart.SlotID,
			fields.StartedAt.Format(time.RFC3339Nano), afterRestart.ActivityStartedAt.Format(time.RFC3339Nano),
			event.RecordedAt.Format(time.RFC3339Nano), event.RecordedAt.Sub(fields.StartedAt),
			fields.RoomID == afterRestart.RoomID, fields.SlotID == afterRestart.SlotID,
			fields.StartedAt.Equal(afterRestart.ActivityStartedAt),
			routineWorldDigest(fields.LayoutSHA256), routineWorldDigest(fields.SceneSHA256))
	}
	setScheduleNow(wakeAt.Add(time.Minute))
	wakeWork := runNativeRoutineScheduler(t, routine, "aka")
	if !strings.HasSuffix(wakeWork.id, ":wake") {
		t.Fatalf("idleSource selected %q for planned wake", wakeWork.id)
	}
	afterWake := readActor("aka")
	if afterWake.ActivityID == beforeRestart.ActivityID || afterWake.ActivityKind == "sleep" {
		t.Fatalf("WORLD did not confirm wake: before=%+v after=%+v", beforeRestart, afterWake)
	}

	testDate := parseNativeRoutineDate(day, loc)
	setScheduleNow(time.Date(testDate.Year(), testDate.Month(), testDate.Day(), 22, 0, 0, 0, loc))
	moveWork := runNativeRoutineScheduler(t, routine, "mio")
	if !strings.HasSuffix(moveWork.id, ":relax") {
		t.Fatalf("idleSource selected %q for Mio's isolated move", moveWork.id)
	}
	mio := readActor("mio")
	if mio.RoomID != layout.PrivateRooms["mio"] || mio.ActivityID != "" {
		t.Fatalf("CORE relax Work.Run did not finish in Mio private room: room=%q activity=%q", mio.RoomID, mio.ActivityID)
	}

	events, err := readClient.Events(ctx, 0)
	if err != nil {
		t.Fatalf("read isolated WORLD event ledger: %v", err)
	}
	required := map[string]map[string]bool{
		"life.home.move.started": {"mio": false}, "life.home.move.arrived": {"mio": false},
		"life.home.sleep.started": {"aka": false}, "life.home.sleep.woke": {"aka": false},
	}
	for _, event := range events.Events {
		var payload struct {
			ActorID string `json:"actor_id"`
		}
		if json.Unmarshal(event.Payload, &payload) == nil {
			if actors, ok := required[event.Kind]; ok {
				if _, ok := actors[payload.ActorID]; ok {
					actors[payload.ActorID] = true
				}
			}
		}
	}
	for kind, actors := range required {
		for actorID, found := range actors {
			if !found {
				t.Errorf("WORLD ledger lacks %s for %s", kind, actorID)
			}
		}
	}
	worldEventDate := time.Now().In(loc).Format("2006-01-02")
	akaDiary, err := routine.buildDiaryBody(ctx, "aka", worldEventDate)
	if err != nil {
		t.Fatalf("build Aka diary from actual WORLD owner events: %v", err)
	}
	for _, line := range []string{"WORLDでアカの部屋のベッドで就寝を確認しました。", "WORLDで起床を確認しました。"} {
		if !strings.Contains(akaDiary, line) {
			t.Errorf("Aka's actual WORLD diary for %s omitted %q: %s", worldEventDate, line, akaDiary)
		}
	}
	mioDiary, err := routine.buildDiaryBody(ctx, "mio", worldEventDate)
	if err != nil {
		t.Fatalf("build Mio diary from actual WORLD owner events: %v", err)
	}
	for _, line := range []string{
		"WORLDで自宅内のミオの部屋への移動を開始しました。",
		"WORLDで自宅内のミオの部屋への移動完了を確認しました。",
	} {
		if !strings.Contains(mioDiary, line) {
			t.Errorf("Mio's actual WORLD diary for %s omitted %q: %s", worldEventDate, line, mioDiary)
		}
	}
	t.Logf("native diary builder read actual authenticated WORLD owner events for JST %s: Aka sleep/wake and Mio private-room move", worldEventDate)
	storedTasks, err := tasks.List(ctx, domaintask.Filter{ModuleID: agentRoutineModuleID, Limit: 20})
	if err != nil || len(storedTasks) != 3 {
		t.Fatalf("canonical Task count=%d err=%v, want sleep, wake, and move", len(storedTasks), err)
	}
	for _, task := range storedTasks {
		if task.OwnerID != nativeRoutineOwner || task.Status != domaintask.StatusSucceeded {
			t.Errorf("canonical Task owner=%q status=%s, want %q and succeeded", task.OwnerID, task.Status, nativeRoutineOwner)
		}
	}
	t.Logf("native CORE to WORLD receipt: host=%s/%s owner=%s isolated timeline=%s events=%d canonical_tasks=%d",
		runtime.GOOS, runtime.GOARCH, nativeRoutineOwner, nativeRoutineTimelineID, len(events.Events), len(storedTasks))
}

func nativeRoutineTempDir(t *testing.T, prefix string) (string, error) {
	t.Helper()
	base := strings.TrimSpace(os.Getenv("REN_CROW_TEST_TMP"))
	if base == "" {
		return "", errors.New("REN_CROW_TEST_TMP must point to a private owner temp directory")
	}
	root, err := os.MkdirTemp(base, prefix)
	if err != nil {
		return "", err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return "", err
	}
	return root, nil
}
func nativeRoutineWorldConfig(t *testing.T, layoutBytes []byte, layout nativeRoutineHomeLayout) (string, string, string, string, error) {
	t.Helper()
	root, err := nativeRoutineTempDir(t, "rencrow-core-home-e2e-")
	if err != nil {
		return "", "", "", "", err
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove isolated WORLD temp root: %v", err)
		}
	})
	if err := os.Chmod(root, 0o700); err != nil {
		return "", "", "", "", err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", "", "", err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	worldURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	scope := map[string]string{"world_id": nativeRoutineWorldID, "timeline_id": nativeRoutineTimelineID, "timeline_epoch": nativeRoutineEpoch}
	credentialDir := filepath.Join(root, "credentials")
	if err := os.Mkdir(credentialDir, 0o700); err != nil {
		return "", "", "", "", err
	}
	readerToken, err := nativeRoutineBearer()
	if err != nil {
		return "", "", "", "", err
	}
	lifeToken, err := nativeRoutineBearer()
	if err != nil {
		return "", "", "", "", err
	}
	if err := nativeRoutinePrivateWrite(filepath.Join(credentialDir, "reader.bearer"), []byte(readerToken+"\n")); err != nil {
		return "", "", "", "", err
	}
	if err := nativeRoutinePrivateWrite(filepath.Join(credentialDir, "life.bearer"), []byte(lifeToken+"\n")); err != nil {
		return "", "", "", "", err
	}
	layoutPath := filepath.Join(root, "home-layout.json")
	if err := nativeRoutinePrivateWrite(layoutPath, layoutBytes); err != nil {
		return "", "", "", "", err
	}
	tokens := []map[string]any{
		{"id": "reader", "publisher_id": "house-105-native-reader", "sha256": nativeRoutineDigest(readerToken), "scopes": []string{"read"}, "scope": scope},
		{"id": "core", "publisher_id": "house-105-native-core", "sha256": nativeRoutineDigest(lifeToken), "scopes": []string{"life"}, "scope": scope, "life_actor_ids": nativeRoutineActors},
	}
	actors := make(map[string]any, len(nativeRoutineActors))
	for _, actorID := range nativeRoutineActors {
		room := "dining"
		if actorID == "mio" || actorID == "kuro" || actorID == "aka" || actorID == "kin" {
			room = "living"
		}
		position, ok := layout.RoomAnchors[room]
		if !ok {
			return "", "", "", "", fmt.Errorf("HOUSE layout lacks %s anchor", room)
		}
		if actorID == "mio" {
			room = "landing-second"
			position, ok = layout.RoomAnchors[room]
			if !ok {
				return "", "", "", "", fmt.Errorf("HOUSE layout lacks %s anchor", room)
			}
		}
		actor := map[string]any{"room_id": room, "position": position}
		if actorID == "aka" {
			bed, ok := nativeRoutineSlot(layout, "bed-aka")
			if !ok || bed.Actor != actorID || bed.RoomID != layout.PrivateRooms[actorID] {
				return "", "", "", "", errors.New("HOUSE layout lacks Aka's assigned bed slot")
			}
			actor["room_id"], actor["slot_id"], actor["position"] = bed.RoomID, bed.ID, bed.Anchor
		}
		actors[actorID] = actor
	}
	worldConfig := map[string]any{
		"listen": fmt.Sprintf("127.0.0.1:%d", port), "store_path": filepath.Join(root, "world.sqlite"),
		"scope": scope, "tokens": tokens, "home_layout_path": layoutPath,
		"initial_state": map[string]any{"actors": actors, "resources": map[string]any{}},
	}
	encoded, err := json.MarshalIndent(worldConfig, "", "  ")
	if err != nil {
		return "", "", "", "", err
	}
	configPath := filepath.Join(root, "world.json")
	if err := nativeRoutinePrivateWrite(configPath, append(encoded, '\n')); err != nil {
		return "", "", "", "", err
	}
	return root, configPath, worldURL, readerToken, nil
}

func nativeRoutineSlot(layout nativeRoutineHomeLayout, id string) (nativeRoutineHomeSlot, bool) {
	for _, slot := range layout.Slots {
		if slot.ID == id {
			return slot, true
		}
	}
	return nativeRoutineHomeSlot{}, false
}

func nativeRoutineBearer() (string, error) {
	raw := make([]byte, 48)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func nativeRoutineDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func nativeRoutinePrivateWrite(path string, contents []byte) error {
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func startNativeRoutineWorld(ctx context.Context, binary, configPath, root, worldURL, readerToken string) (*nativeRoutineWorldProcess, error) {
	logFile, err := os.OpenFile(filepath.Join(root, fmt.Sprintf("world-%d.log", time.Now().UnixNano())), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(binary, "serve", "--config", configPath)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	_ = logFile.Close()
	process := &nativeRoutineWorldProcess{cmd: cmd}
	if err := waitNativeRoutineWorld(ctx, worldURL, readerToken, cmd); err != nil {
		_ = stopNativeRoutineWorld(process)
		return nil, err
	}
	return process, nil
}

func waitNativeRoutineWorld(ctx context.Context, worldURL, readerToken string, cmd *exec.Cmd) error {
	client := &http.Client{Timeout: time.Second}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if cmd.ProcessState != nil {
			return errors.New("WORLD process exited before readiness")
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, worldURL+"/health/ready", nil)
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+readerToken)
		response, requestErr := client.Do(request)
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("authenticated WORLD readiness timed out")
		case <-ticker.C:
		}
	}
}

func stopNativeRoutineWorld(process *nativeRoutineWorldProcess) error {
	if process == nil || process.cmd == nil || process.cmd.Process == nil {
		return nil
	}
	_ = process.cmd.Process.Signal(syscall.SIGTERM)
	if err := process.cmd.Wait(); err != nil {
		return fmt.Errorf("WORLD process exit: %w", err)
	}
	return nil
}

func runNativeRoutineScheduler(t *testing.T, routine *agentRoutineRuntime, actorID string) nativeRoutineWork {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 260*time.Second)
	defer cancel()
	idle := routine.idleSource(nil)
	discovered := make(chan nativeRoutineDiscovery, 1)
	completed := make(chan error, 1)
	var issued sync.Once
	source := func(sourceCtx context.Context, scannedActor string) (agentheartbeat.Work, bool, error) {
		if scannedActor != actorID {
			return agentheartbeat.Work{}, false, nil
		}
		var work agentheartbeat.Work
		var found bool
		var err error
		issued.Do(func() {
			work, found, err = idle(sourceCtx, scannedActor)
			discovered <- nativeRoutineDiscovery{work: nativeRoutineWork{id: work.ID}, found: found, err: err}
			if found {
				run := work.Run
				work.Run = func(runCtx context.Context, stop <-chan struct{}) error {
					runErr := run(runCtx, stop)
					completed <- runErr
					return runErr
				}
			}
		})
		if err != nil || !found {
			return agentheartbeat.Work{}, false, err
		}
		return work, true, nil
	}
	scheduler := agentheartbeat.New(agentHeartbeatActorIDs, agentheartbeat.Sources{Idle: source}, nil)
	started := make(chan error, 1)
	go func() { started <- scheduler.Start(ctx) }()
	var discovery nativeRoutineDiscovery
	select {
	case discovery = <-discovered:
	case <-ctx.Done():
		cancel()
		t.Fatalf("timed out discovering CORE idle work for %s", actorID)
	}
	if discovery.err != nil || !discovery.found {
		cancel()
		t.Fatalf("CORE idleSource actor=%s found=%v err=%v", actorID, discovery.found, discovery.err)
	}
	select {
	case err := <-completed:
		if err != nil {
			cancel()
			t.Fatalf("CORE Scheduler Work.Run actor=%s id=%s: %v", actorID, discovery.work.id, err)
		}
	case <-ctx.Done():
		cancel()
		t.Fatalf("CORE Scheduler Work.Run timed out for %s", actorID)
	}
	cancel()
	if err := <-started; err != nil {
		t.Fatalf("stop CORE Scheduler: %v", err)
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer waitCancel()
	if err := scheduler.Wait(waitCtx); err != nil {
		t.Fatalf("wait for CORE Scheduler reservations: %v", err)
	}
	return discovery.work
}

func parseNativeRoutineDate(value string, location *time.Location) time.Time {
	date, _ := time.ParseInLocation("2006-01-02", value, location)
	return date
}
