package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type gameObserverSessionsReaderFunc func(context.Context) ([]gameObserverActiveAgentSession, error)

func (f gameObserverSessionsReaderFunc) ActiveAgentSessions(ctx context.Context) ([]gameObserverActiveAgentSession, error) {
	return f(ctx)
}

func TestLivingConversationUsesDistinctSeatsAndTracksCurrentTurn(t *testing.T) {
	for _, room := range []string{"living", "dining"} {
		t.Run(room, func(t *testing.T) {
			runtime := &agentRoutineRuntime{}
			end := runtime.showLivingConversation("mio", "shiro", room)
			host := runtime.livingConversation["mio"]
			guest := runtime.livingConversation["shiro"]
			if host.slot == "" || guest.slot == "" || host.slot == guest.slot ||
				livingSeatRoom(host.slot) != room || livingSeatRoom(guest.slot) != room ||
				host.pose != "" || guest.pose != "" {
				t.Fatalf("unexpected conversation seats: host=%+v guest=%+v", host, guest)
			}
			runtime.livingConversationSpeaking("mio", true)
			if runtime.livingConversation["mio"].pose != "talk" || runtime.livingConversation["shiro"].pose != "" {
				t.Fatal("only the active participant should use the talk pose")
			}
			runtime.livingConversationSpeaking("mio", false)
			runtime.livingConversationSpeaking("shiro", true)
			if runtime.livingConversation["mio"].pose != "" || runtime.livingConversation["shiro"].pose != "talk" {
				t.Fatal("the second turn should transfer the talk pose")
			}
			end()
			if len(runtime.livingConversation) != 0 {
				t.Fatal("ending the conversation did not release both seat targets")
			}
		})
	}
}

func TestRunningGameLeaseTargetsOwnDeskAndProjectsTyping(t *testing.T) {
	_, deps, server := newRoutineHouseViewTestRuntime(t, true)
	defer server.Close()
	gamesResponse := []byte(`{"ok":true,"source":"local_observer","sessions":[],"active_agent_sessions":[{"game_id":"nethack","session_id":"nethack-mio","personas":["mio"],"status":"running"}]}`)
	gamesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(gamesResponse)
	}))
	defer gamesServer.Close()
	gameSessions, err := newGameObserverSessionsHTTPClient(gamesServer.URL)
	if err != nil {
		t.Fatalf("create GAMES sessions client: %v", err)
	}
	deps.agentRoutine.gameSessions = gameSessions
	scheduler := startLivingSeatTestScheduler(t)
	deps.agentHeartbeat = scheduler
	runtime := deps.agentRoutine
	runtime.deps = deps

	release, err := scheduler.AcquireGameTurn(context.Background(), "mio", domaintask.PriorityHigh, "nethack")
	if err != nil {
		t.Fatalf("acquire real game-turn lease: %v", err)
	}
	defer release()

	seat, ready, err := runtime.livingSeats(context.Background())
	if err != nil {
		t.Fatalf("read desired living seats: %v", err)
	}
	if !ready {
		t.Fatal("running scheduler snapshot is unavailable")
	}
	if got, want := seat["mio"], (livingSeatActivity{room: "living", slot: "desk-mio", pose: "type"}); got != want {
		t.Fatalf("running mio game seat = %+v, want %+v", got, want)
	}

	actors := make([]worldread.Actor, 0, len(runtime.worldActorIDs))
	for worldID, coreID := range runtime.worldActorIDs {
		actor := worldread.Actor{
			ActorID: worldID, Present: true, RoomID: "room-" + coreID,
			Position: [3]float64{1, 2, 0}, HasPosition: true,
			HomeSceneSHA256: routineHouseViewTestSceneSHA256,
		}
		if coreID == "mio" {
			actor.RoomID = "living"
			actor.SlotID = "desk-mio"
		}
		actors = append(actors, actor)
	}
	read := routineSnapshotRead{
		snapshot: worldread.Snapshot{
			WorldID: "home-world", StateSeq: 45, Actors: actors,
			HomeSceneSHA256: routineHouseViewTestSceneSHA256,
		},
		readAt: time.Date(2026, time.November, 16, 0, 0, 0, 0, time.UTC),
	}
	projected, err := runtimeRoutineHouseViewProject(context.Background(), runtime, read)
	if err != nil {
		t.Fatalf("project canonical house view: %v", err)
	}
	foundMio := false
	for _, actor := range projected.Actors {
		if actor.ID != "mio" {
			continue
		}
		foundMio = true
		if actor.SeatID != "desk-mio" || actor.SeatAction != "type" {
			t.Fatalf("projected mio seat = %q/%q, want desk-mio/type", actor.SeatID, actor.SeatAction)
		}
	}
	if !foundMio {
		t.Fatal("canonical house projection omitted mio")
	}

	release()
	seat, ready, err = runtime.livingSeats(context.Background())
	if err != nil {
		t.Fatalf("read desired living seats after release: %v", err)
	}
	if !ready {
		t.Fatal("running scheduler snapshot is unavailable after release")
	}
	if got, want := seat["mio"], (livingSeatActivity{room: "living", slot: "desk-mio", pose: "type"}); got != want {
		t.Fatalf("active game session lost its desk after turn release: got %+v, want %+v", got, want)
	}
	projected, err = runtimeRoutineHouseViewProject(context.Background(), runtime, read)
	if err != nil {
		t.Fatalf("project house view after game turn release: %v", err)
	}
	for _, actor := range projected.Actors {
		if actor.ID == "mio" && (actor.SeatID != "desk-mio" || actor.SeatAction != "type") {
			t.Fatalf("active session projection after turn release = %q/%q, want desk-mio/type", actor.SeatID, actor.SeatAction)
		}
	}
	gamesResponse = []byte(`{"ok":true,"source":"local_observer","sessions":[],"active_agent_sessions":[{"game_id":"nethack","session_id":"nethack-mio-1","personas":["mio"],"status":"running"},{"game_id":"nethack","session_id":"nethack-mio-2","personas":["mio"],"status":"running"}]}`)
	seat, ready, err = runtime.livingSeats(context.Background())
	if err != nil || !ready || seat["mio"] != (livingSeatActivity{room: "living", slot: "desk-mio", pose: "type"}) {
		t.Fatalf("two active sessions for mio = (%+v, %t, %v), want union retaining desk-mio/type", seat, ready, err)
	}
	gamesResponse = []byte(`{"ok":true,"source":"local_observer","sessions":[],"active_agent_sessions":[{"game_id":"nethack","session_id":"nethack-mio-2","personas":["mio"],"status":"running"}]}`)
	seat, ready, err = runtime.livingSeats(context.Background())
	if err != nil || !ready || seat["mio"] != (livingSeatActivity{room: "living", slot: "desk-mio", pose: "type"}) {
		t.Fatalf("one remaining session for mio = (%+v, %t, %v), want desk-mio/type", seat, ready, err)
	}
	gamesResponse = []byte(`{"ok":true,"source":"local_observer","sessions":[{"game_id":"nethack","session_id":"nethack-mio","persona":"mio","status":"completed"}],"active_agent_sessions":[]}`)
	seat, ready, err = runtime.livingSeats(context.Background())
	if err != nil {
		t.Fatalf("read desired living seats after session completion: %v", err)
	}
	if !ready {
		t.Fatal("successful empty active-session snapshot was unavailable")
	}
	if _, exists := seat["mio"]; exists {
		t.Fatalf("terminal session left a stale desired seat: %+v", seat["mio"])
	}
	projected, err = runtimeRoutineHouseViewProject(context.Background(), runtime, read)
	if err != nil {
		t.Fatalf("project house view after game session completion: %v", err)
	}
	for _, actor := range projected.Actors {
		if actor.ID == "mio" && actor.SeatAction != "" {
			t.Fatalf("completed game session retained typing pose %q", actor.SeatAction)
		}
	}
}

func TestActiveGameSessionTargetsDeskBeforeFirstTurnLease(t *testing.T) {
	_, deps, server := newRoutineHouseViewTestRuntime(t, true)
	defer server.Close()
	gamesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"source":"local_observer","sessions":[],"active_agent_sessions":[{"game_id":"nethack","session_id":"nethack-mio","personas":["mio"],"status":"launching"}]}`))
	}))
	defer gamesServer.Close()
	gameSessions, err := newGameObserverSessionsHTTPClient(gamesServer.URL)
	if err != nil {
		t.Fatalf("create GAMES sessions client: %v", err)
	}
	deps.agentRoutine.gameSessions = gameSessions
	scheduler := startLivingSeatTestScheduler(t)
	deps.agentHeartbeat = scheduler
	runtime := deps.agentRoutine
	runtime.deps = deps
	seat, ready, err := runtime.livingSeats(context.Background())
	if err != nil {
		t.Fatalf("read desired living seats before first turn: %v", err)
	}
	if !ready {
		t.Fatal("active launching session was unavailable before its first turn lease")
	}
	if got, want := seat["mio"], (livingSeatActivity{room: "living", slot: "desk-mio", pose: "type"}); got != want {
		t.Fatalf("pre-turn mio game seat = %+v, want %+v", got, want)
	}
}

func TestGameSessionSnapshotFailureDoesNotBecomeAnEmptySeatSet(t *testing.T) {
	_, deps, server := newRoutineHouseViewTestRuntime(t, true)
	defer server.Close()
	scheduler := startLivingSeatTestScheduler(t)
	deps.agentHeartbeat = scheduler
	runtime := deps.agentRoutine
	runtime.deps = deps
	runtime.gameSessions = gameObserverSessionsReaderFunc(func(context.Context) ([]gameObserverActiveAgentSession, error) {
		return nil, errGameObserverSessionsUnavailable
	})
	seats, ready, err := runtime.livingSeats(context.Background())
	if ready || !errors.Is(err, errGameObserverSessionsUnavailable) || seats != nil {
		t.Fatalf("livingSeats() = (%+v, %t, %v), want unavailable snapshot", seats, ready, err)
	}
	read := routineSnapshotRead{snapshot: worldread.Snapshot{
		WorldID: "home-world", HomeSceneSHA256: routineHouseViewTestSceneSHA256,
	}}
	if _, err := runtimeRoutineHouseViewProject(context.Background(), runtime, read); !errors.Is(err, errGameObserverSessionsUnavailable) {
		t.Fatalf("house view projection error = %v, want unavailable GAMES snapshot", err)
	}
}

func TestGameSessionSeatsYieldToWorkAndConversationSeats(t *testing.T) {
	sessions := []gameObserverActiveAgentSession{{
		GameID: "herzog_zwei", SessionID: "multi-agent-game", Personas: []string{"mio", "midori", "shiro", "aka"}, Status: "running",
	}}
	activities := []agentheartbeat.LivingActivity{
		{ActorID: "mio", State: "running", ActivityKind: "board"},
		{ActorID: "midori", State: "running", ActivityKind: "private"},
		{ActorID: "shiro", State: "running", ActivityKind: "routine"},
	}
	conversations := map[string]livingSeatActivity{"shiro": {room: "dining", slot: "dining-guest", pose: "talk"}}
	seats := buildLivingSeatTargets(sessions, activities, conversations)
	want := map[string]livingSeatActivity{
		"mio":    {room: "living", slot: "desk-mio", pose: "review"},
		"midori": {room: "living", slot: "desk-midori", pose: "notes"},
		"shiro":  {room: "dining", slot: "dining-guest", pose: "talk"},
		"aka":    {room: "living", slot: "desk-aka", pose: "type"},
	}
	for actorID, expected := range want {
		if got := seats[actorID]; got != expected {
			t.Errorf("seat for %s = %+v, want %+v", actorID, got, expected)
		}
	}
}

func startLivingSeatTestScheduler(t *testing.T) *agentheartbeat.Scheduler {
	t.Helper()
	scheduler := agentheartbeat.New(agentHeartbeatActorIDs, agentheartbeat.Sources{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = scheduler.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer waitCancel()
		if err := scheduler.Wait(waitCtx); err != nil {
			t.Errorf("wait for scheduler shutdown: %v", err)
		}
	})
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		if _, ready := scheduler.LivingSnapshot(context.Background(), nil); ready {
			return scheduler
		}
		select {
		case <-deadline.C:
			t.Fatal("scheduler did not become available")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}
