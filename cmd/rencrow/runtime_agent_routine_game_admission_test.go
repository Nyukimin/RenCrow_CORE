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
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type routineAdmissionGameReader struct {
	sessions []gameObserverActiveAgentSession
	err      error
}

func (r *routineAdmissionGameReader) ActiveAgentSessions(context.Context) ([]gameObserverActiveAgentSession, error) {
	return append([]gameObserverActiveAgentSession(nil), r.sessions...), r.err
}

func TestRoutineGameAdmissionMatchesAllLaunchingAndRunningSessions(t *testing.T) {
	reader := &routineAdmissionGameReader{sessions: []gameObserverActiveAgentSession{
		{GameID: "nethack", SessionID: "launch-mio", Personas: []string{"mio"}, Status: "launching"},
		{GameID: "survival_garden", SessionID: "run-shiro", Personas: []string{"shiro", "kuro"}, Status: "running"},
		{GameID: "territory_commander", SessionID: "finished-midori", Personas: []string{"midori"}, Status: "completed"},
	}}
	runtime := &agentRoutineRuntime{gameSessions: reader}
	for _, actorID := range []string{"mio", "shiro", "kuro"} {
		active, err := runtime.hasActiveRoutineGame(context.Background(), actorID)
		if err != nil || !active {
			t.Fatalf("active game membership for %s = %t, %v; want active", actorID, active, err)
		}
	}
	if active, err := runtime.hasActiveRoutineGame(context.Background(), "midori"); err != nil || active {
		t.Fatalf("completed session membership = %t, %v; want inactive", active, err)
	}
	if active, err := (&agentRoutineRuntime{}).hasActiveRoutineGame(context.Background(), "mio"); err != nil || active {
		t.Fatalf("legacy nil-reader membership = %t, %v; want no admission restriction", active, err)
	}
	reader.err = errors.New("observer unavailable")
	if blocked, err := runtime.routineGameAdmissionBlocked(context.Background(), "mio"); !blocked || !errors.Is(err, errRoutineGameAdmission) {
		t.Fatalf("unknown session state admission = blocked:%t err:%v; want fail-closed admission", blocked, err)
	}
}

func TestRoutineGameAdmissionPreservesPendingIntentAndResumesAfterSessionEnds(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 14, 7, 0, 0, 0, time.FixedZone("JST", 9*60*60))
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
	reader := &routineAdmissionGameReader{sessions: []gameObserverActiveAgentSession{{
		GameID: "herzog_zwei", SessionID: "hz-shiro", Personas: []string{"shiro"}, Status: "running",
	}}}
	runtime.gameSessions = reader
	progress := routineProgress{Actors: []routineActorProgress{
		{ActorID: "mio", TaskID: modulecore.NewTaskID()},
		{ActorID: "shiro", TaskID: modulecore.NewTaskID()},
	}}
	state := draftRoutineAdmissionMorningIntent(t, runtime, progress, clock)
	intentID, taskID := state.Pending.ID, progress.Actors[0].TaskID
	planned := agentroutine.Plan{Kind: agentroutine.ActivityMorningChat, Participants: []string{"shiro"}}
	if err := runtime.run(ctx, nil, "mio", planned); err != nil {
		t.Fatalf("run with a participant in a live game: %v", err)
	}
	state, err = runtime.store.Load(ctx, "mio")
	if err != nil || state.Pending == nil || state.Pending.ID != intentID || !state.Pending.RetryAfter.IsZero() {
		t.Fatalf("pending intent after active-game deferral = %+v err=%v; want same intent without cooldown", state.Pending, err)
	}
	if _, err := tasks.Get(ctx, taskID); !errors.Is(err, taskmanager.ErrNotFound) {
		t.Fatalf("blocked discovery created a routine Task: %v", err)
	}
	if work, found, err := runtime.idleSource(nil)(ctx, "mio"); err != nil || found {
		t.Fatalf("idle discovery with participant game active = %+v found=%t err=%v; want no routine work", work, found, err)
	}
	reader.sessions = nil
	work, found, err := runtime.idleSource(nil)(ctx, "mio")
	if err != nil || !found || work.ID == "" {
		t.Fatalf("idle discovery after final game session = %+v found=%t err=%v; want the same pending work immediately", work, found, err)
	}
	state, err = runtime.store.Load(ctx, "mio")
	if err != nil || state.Pending == nil || state.Pending.ID != intentID || !state.Pending.RetryAfter.IsZero() {
		t.Fatalf("pending intent after rediscovery = %+v err=%v; want unchanged and cooldown-free", state.Pending, err)
	}
}

func TestRoutineGameAdmissionDoesNotResumePartialMorningChatForGameParticipant(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 14, 7, 0, 0, 0, time.FixedZone("JST", 9*60*60))
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
	runtime.gameSessions = &routineAdmissionGameReader{sessions: []gameObserverActiveAgentSession{{
		GameID: "survival_garden", SessionID: "sg-shiro", Personas: []string{"shiro"}, Status: "running",
	}}}
	chatTaskID := modulecore.NewTaskID()
	chatRun := startRoutineAdmissionRun(t, tasks, agentRoutineModuleID, "mio", chatTaskID)
	progress := routineProgress{Actors: []routineActorProgress{
		{ActorID: "mio", TaskID: chatTaskID, RunID: chatRun.run.RunID, Output: "existing first turn"},
		{ActorID: "shiro", TaskID: modulecore.NewTaskID()},
	}}
	state := draftRoutineAdmissionMorningIntent(t, runtime, progress, clock)
	intentID, taskID := state.Pending.ID, progress.Actors[0].TaskID
	if err := runtime.run(ctx, nil, "mio", agentroutine.Plan{Kind: agentroutine.ActivityMorningChat, Participants: []string{"shiro"}}); err != nil {
		t.Fatalf("resume partial chat while participant is gaming: %v", err)
	}
	stored, err := runtime.store.Load(ctx, "mio")
	if err != nil || stored.Pending == nil || stored.Pending.ID != intentID || !stored.Pending.RetryAfter.IsZero() {
		t.Fatalf("partial chat after game admission = %+v err=%v; want unchanged checkpoint without cooldown", stored.Pending, err)
	}
	pausedTask, err := tasks.Get(ctx, taskID)
	if err != nil || pausedTask.Status != domaintask.StatusWaiting {
		t.Fatalf("partial chat's exact persisted Task after game admission = %+v err=%v; want paused/waiting", pausedTask, err)
	}
	pausedRuns, err := tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
	if err != nil || len(pausedRuns) != 1 || pausedRuns[0].RunID != chatRun.run.RunID || pausedRuns[0].Status != domaintask.RunStatusWaiting {
		t.Fatalf("partial chat's exact persisted Run after game admission = %+v err=%v; want paused/waiting", pausedRuns, err)
	}
}

func TestRoutineGameAdmissionBlocksFreshRunAndUnknownSessionState(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 14, 7, 0, 0, 0, time.FixedZone("JST", 9*60*60))
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
	if _, err := runtime.store.EnsureSchedule(ctx, "mio", clock.Format("2006-01-02")); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	reader := &routineAdmissionGameReader{sessions: []gameObserverActiveAgentSession{{
		GameID: "survival_garden", SessionID: "sg-mio", Personas: []string{"mio"}, Status: "launching",
	}}}
	runtime.gameSessions = reader
	for _, kind := range []agentroutine.ActivityKind{agentroutine.ActivityDiary, agentroutine.ActivitySleep} {
		if err := runtime.run(ctx, nil, "mio", agentroutine.Plan{Kind: kind}); err != nil {
			t.Fatalf("fresh %s run during game admission: %v", kind, err)
		}
		state, err := runtime.store.Load(ctx, "mio")
		if err != nil || state.Pending != nil {
			t.Fatalf("fresh game-blocked %s run created a pending intent: %+v err=%v", kind, state.Pending, err)
		}
	}
	reader.sessions = nil
	reader.err = errors.New("observer unavailable")
	if work, found, err := runtime.idleSource(nil)(ctx, "mio"); err != nil || found {
		t.Fatalf("unknown GAMES snapshot discovered routine work: %+v found=%t err=%v; want fail closed", work, found, err)
	}
}

func TestRoutineGameAdmissionDiscoversFreshWorldWakeForAuthenticatedValidation(t *testing.T) {
	ctx := context.Background()
	initialClock := time.Date(2026, time.November, 14, 6, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	clock := initialClock
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
	date := initialClock.Format("2006-01-02")
	state, err := runtime.store.EnsureSchedule(ctx, "mio", date)
	if err != nil {
		t.Fatalf("seed wake schedule: %v", err)
	}
	clock = state.PlannedWake
	state.LogicalSleepUntil = state.PlannedWake
	state, err = runtime.store.Save(ctx, state, state.Revision)
	if err != nil {
		t.Fatalf("save completed sleep window: %v", err)
	}
	// A configured life client lets a fresh wake reach the existing authenticated
	// sleep-start validation path; it does not itself authorize a WORLD command.
	runtime.worldLife = &worldread.LifeClient{}
	reader := &routineAdmissionGameReader{sessions: []gameObserverActiveAgentSession{{
		GameID: "herzog_zwei", SessionID: "hz-mio", Personas: []string{"mio"}, Status: "running",
	}}}
	runtime.gameSessions = reader
	for _, test := range []struct {
		name string
		read func()
	}{
		{name: "active session", read: func() {}},
		{name: "unknown session state", read: func() { reader.sessions = nil; reader.err = errors.New("observer unavailable") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.read()
			work, found, err := runtime.idleSource(nil)(ctx, "mio")
			if err != nil || !found || work.ID == "" {
				t.Fatalf("scheduled WORLD wake during %s = %+v found=%t err=%v; want admission to authenticated validation", test.name, work, found, err)
			}
		})
	}
	stored, err := runtime.store.Load(ctx, "mio")
	if err != nil || stored.Pending != nil {
		t.Fatalf("wake discovery modified canonical intent state: %+v err=%v", stored.Pending, err)
	}
}

func TestRoutineGameAdmissionResumesPendingWakeThroughAuthenticatedSleepBinding(t *testing.T) {
	for _, test := range []struct {
		name             string
		reader           *routineAdmissionGameReader
		feedActorID      string
		wantWakeAccepted bool
	}{
		{name: "active game", reader: &routineAdmissionGameReader{sessions: []gameObserverActiveAgentSession{{
			GameID: "survival_garden", SessionID: "sg-mio", Personas: []string{"mio"}, Status: "running",
		}}}, feedActorID: "mio-world", wantWakeAccepted: true},
		{name: "unknown game state", reader: &routineAdmissionGameReader{err: errors.New("observer unavailable")},
			feedActorID: "mio-world", wantWakeAccepted: true},
		{name: "foreign sleep-start event", reader: &routineAdmissionGameReader{sessions: []gameObserverActiveAgentSession{{
			GameID: "territory_commander", SessionID: "tc-mio", Personas: []string{"mio"}, Status: "running",
		}}}, feedActorID: "foreign-world", wantWakeAccepted: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			clock := time.Date(2026, time.November, 14, 6, 0, 0, 0, time.UTC)
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
			runtime.cfg = &config.Config{Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{
				PrivateRooms: map[string]string{"mio": "private-mio"},
			}}}
			const intentID, activityID = "pending-wake-after-restart", "sleep-mio-restart"
			started := clock.Add(-5 * time.Hour)
			taskID := modulecore.NewTaskID()
			progress := routineProgress{Actors: []routineActorProgress{{ActorID: "mio", TaskID: taskID}}}
			encoded, err := json.Marshal(progress)
			if err != nil {
				t.Fatal(err)
			}
			state, err := runtime.store.DraftIntent(ctx, agentroutine.Intent{
				ID: intentID, ActorID: "mio", Date: "2026-11-14", TaskID: string(taskID),
				Kind: agentroutine.ActivityWake, Step: "planned", Output: string(encoded), CreatedAt: clock,
			})
			if err != nil {
				t.Fatalf("draft pending Wake without a sleep binding: %v", err)
			}
			wakeCommands, snapshotReads, startFeedReads, terminalFeedReads := 0, 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				switch {
				case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/snapshot"):
					snapshotReads++
					stateSeq := 1
					actor := map[string]any{
						"room_id": "private-mio", "slot_id": "bed-mio", "home_scene_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					}
					if wakeCommands == 0 {
						actor["activity_id"] = activityID
						actor["activity_kind"] = "sleep"
						actor["activity_started_at"] = started.Format(time.RFC3339Nano)
					} else {
						stateSeq = 2
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"schema_version": 1, "world_id": "world-home", "timeline_id": "timeline-home", "timeline_epoch": "epoch-home",
						"state_seq": stateSeq, "state_hash": strings.Repeat("a", 64),
						"home_scene_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
						"state":             map[string]any{"actors": map[string]any{"mio-world": actor}},
					})
				case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/events"):
					w.Header().Set("Content-Type", "application/json")
					if request.URL.Query().Get("after_seq") == "0" {
						startFeedReads++
						_ = json.NewEncoder(w).Encode(map[string]any{
							"schema_version": 1, "events": []any{map[string]any{
								"seq": 1, "transaction_id": "sleep-start-txn", "event_id": "sleep-start-event",
								"kind": "life.home.sleep.started", "recorded_at": started.Add(time.Second).Format(time.RFC3339Nano),
								"payload": map[string]any{
									"actor_id": test.feedActorID, "activity_id": activityID, "activity_kind": "sleep",
									"room_id": "private-mio", "slot_id": "bed-mio", "started_at": started.Format(time.RFC3339Nano),
									"layout_sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
									"scene_sha256":  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
								},
							}}, "next_seq": 1, "done": true,
						})
					} else {
						terminalFeedReads++
						_ = json.NewEncoder(w).Encode(map[string]any{
							"schema_version": 1, "events": []any{map[string]any{
								"seq": 2, "transaction_id": "wake-txn", "event_id": "wake-event",
								"kind": "life.home.sleep.woke", "recorded_at": clock.Format(time.RFC3339Nano),
								"payload": map[string]any{
									"actor_id": "mio-world", "activity_id": activityID, "activity_kind": "sleep",
									"room_id": "private-mio", "slot_id": "bed-mio", "started_at": started.Format(time.RFC3339Nano),
									"ended_at":      clock.Format(time.RFC3339Nano),
									"layout_sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
									"scene_sha256":  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
								},
							}}, "next_seq": 2, "done": true,
						})
					}
				case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/commands"):
					wakeCommands++
					wantKey := routineWorldOperationKey(intentID, "mio", "wake:"+activityID)
					if request.Header.Get("Idempotency-Key") != wantKey {
						t.Errorf("pending Wake idempotency key = %q; want exact saved intent key %q", request.Header.Get("Idempotency-Key"), wantKey)
					}
					var command struct {
						Action  string                      `json:"action"`
						Payload worldread.HomeActionPayload `json:"payload"`
					}
					if err := json.NewDecoder(request.Body).Decode(&command); err != nil {
						t.Errorf("decode pending Wake command: %v", err)
					}
					if command.Action != worldread.HomeActionWake || command.Payload.ActorID != "mio" || command.Payload.ActivityID != activityID {
						t.Errorf("pending Wake command = %+v; want exact authenticated sleep instance", command)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"schema_version":1,"receipt":{"receipt_id":"wake-receipt","status":"committed","transaction_id":"wake-txn","event_ids":["wake-event"],"first_seq":2,"last_seq":2,"state_seq":2,"recorded_at":"2026-11-14T06:00:00Z"}}`))
				default:
					t.Errorf("unexpected WORLD request %s %s", request.Method, request.URL.Path)
					http.NotFound(w, request)
				}
			}))
			defer server.Close()
			runtime.world, err = worldread.New(worldread.Config{
				BaseURL: server.URL, Credential: "read-token", WorldID: "world-home", TimelineID: "timeline-home",
				TimelineEpoch: "epoch-home", ActorIDs: []string{"mio-world"}, Timeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			runtime.worldLife, err = worldread.NewLife(worldread.LifeConfig{
				BaseURL: server.URL, Credential: "life-token", WorldID: "world-home", TimelineID: "timeline-home",
				TimelineEpoch: "epoch-home", ActorIDs: []string{"mio"}, Timeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			runtime.worldActorIDs = map[string]string{"mio-world": "mio"}
			runtime.gameSessions = test.reader
			work, found, discoveryErr := runtime.idleSource(nil)(ctx, "mio")
			if discoveryErr != nil || !found || work.ID == "" {
				t.Fatalf("pending Wake discovery during %s = %+v found:%t err:%v; want admission to authenticated validation", test.name, work, found, discoveryErr)
			}
			runErr := work.Run(ctx, nil)
			if test.wantWakeAccepted {
				if runErr != nil {
					t.Fatalf("resume pending Wake during %s: %v", test.name, runErr)
				}
				if wakeCommands != 1 || startFeedReads < 1 || terminalFeedReads < 1 || snapshotReads < 2 {
					t.Fatalf("authenticated Wake validation counts = command:%d sleep-feed:%d terminal-feed:%d snapshots:%d",
						wakeCommands, startFeedReads, terminalFeedReads, snapshotReads)
				}
				finished, loadErr := runtime.store.Load(ctx, "mio")
				if loadErr != nil || finished.Pending != nil || finished.LastFinishedIntentID != state.Pending.ID {
					t.Fatalf("pending Wake after authenticated completion = %+v err=%v", finished, loadErr)
				}
				task, taskErr := tasks.Get(ctx, taskID)
				if taskErr != nil || task.Status != domaintask.StatusSucceeded {
					t.Fatalf("validated Wake canonical Task = %+v err=%v; want succeeded", task, taskErr)
				}
			} else {
				if runErr == nil || wakeCommands != 0 || startFeedReads < 1 {
					t.Fatalf("foreign sleep-start data = err:%v wake commands:%d feed reads:%d; want proof rejection before command", runErr, wakeCommands, startFeedReads)
				}
				pending, loadErr := runtime.store.Load(ctx, "mio")
				if loadErr != nil || pending.Pending == nil || pending.Pending.ID != state.Pending.ID {
					t.Fatalf("foreign World data did not preserve the pending Wake: %+v err=%v", pending.Pending, loadErr)
				}
			}
		})
	}
}

func TestRoutineGameAdmissionBlocksNewMoveBeforePreparingWorldOperation(t *testing.T) {
	ctx := context.Background()
	store, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close routine state: %v", err)
		}
	})
	clock := time.Date(2026, time.November, 14, 7, 0, 0, 0, time.UTC)
	progress := routineProgress{Actors: []routineActorProgress{{ActorID: "mio", TaskID: modulecore.NewTaskID()}}}
	encoded, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.DraftIntent(ctx, agentroutine.Intent{
		ID: "routine-game-blocked-move", ActorID: "mio", Date: "2026-11-14", Kind: agentroutine.ActivityHobby,
		Label: "fashion", Step: "planned", Output: string(encoded), CreatedAt: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &agentRoutineRuntime{
		store: store, world: &worldread.Client{}, worldLife: &worldread.LifeClient{},
		gameSessions: &routineAdmissionGameReader{sessions: []gameObserverActiveAgentSession{{
			GameID: "territory_commander", SessionID: "tc-mio", Personas: []string{"mio"}, Status: "running",
		}}},
	}
	for attempt := 0; attempt < 2; attempt++ {
		_, gotProgress, _, actionErr := runtime.performRoutineWorldAction(ctx, state, progress, "hobby-private-room:move",
			worldread.HomeActionMove, "life.home.move.started", worldread.HomeActionPayload{ActorID: "mio", RoomID: "private-mio"})
		if !errors.Is(actionErr, errRoutineGameAdmission) {
			t.Fatalf("new move attempt %d error = %v; want active-game admission block", attempt+1, actionErr)
		}
		if gotProgress.World != nil && len(gotProgress.World.Operations) != 0 {
			t.Fatalf("blocked move attempt %d prepared an operation: %+v", attempt+1, gotProgress.World.Operations)
		}
		stored, loadErr := store.Load(ctx, "mio")
		if loadErr != nil || stored.Pending == nil || stored.Pending.ID != state.Pending.ID || !stored.Pending.RetryAfter.IsZero() {
			t.Fatalf("blocked move attempt %d changed pending state: %+v err=%v", attempt+1, stored.Pending, loadErr)
		}
	}
	reader := runtime.gameSessions.(*routineAdmissionGameReader)
	reader.sessions = nil
	reader.err = errors.New("observer unavailable")
	_, gotProgress, _, actionErr := runtime.performRoutineWorldAction(ctx, state, progress, "hobby-private-room:move",
		worldread.HomeActionMove, "life.home.move.started", worldread.HomeActionPayload{ActorID: "mio", RoomID: "private-mio"})
	if !errors.Is(actionErr, errRoutineGameAdmission) || (gotProgress.World != nil && len(gotProgress.World.Operations) != 0) {
		t.Fatalf("unknown GAMES state new move = err:%v progress:%+v; want no prepared action", actionErr, gotProgress.World)
	}
}

func TestRoutineGameAdmissionReconcilesExactPreparedMoveKey(t *testing.T) {
	ctx := context.Background()
	const (
		intentID   = "routine-game-prepared-move"
		purpose    = "hobby-private-room:move"
		worldID    = "world-home"
		timeline   = "timeline-home"
		actorID    = "mio"
		worldActor = "mio-world"
	)
	key := routineWorldOperationKey(intentID, actorID, purpose)
	var commandCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/worlds/world-home/timelines/timeline-home/commands":
			commandCalls++
			if got := request.Header.Get("Idempotency-Key"); got != key {
				t.Errorf("reconciled command key = %q, want existing key %q", got, key)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schema_version":1,"receipt":{"receipt_id":"receipt-move","status":"committed","transaction_id":"txn-move","event_ids":["event-move"],"first_seq":1,"last_seq":1,"state_seq":1,"recorded_at":"2026-11-14T07:00:00Z"}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/v1/worlds/world-home/timelines/timeline-home/events":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schema_version":1,"events":[{"seq":1,"transaction_id":"txn-move","event_id":"event-move","kind":"life.home.move.started","payload":{"actor_id":"mio-world","activity_id":"move-mio-1","activity_kind":"move","room_id":"hallway","target_room_id":"private-mio","started_at":"2026-11-14T06:59:00Z","due_at":"2026-11-14T07:01:00Z","route_id":"route-mio-1","layout_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","scene_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"recorded_at":"2026-11-14T07:00:00Z"}],"next_seq":1,"done":true}`))
		default:
			t.Errorf("unexpected WORLD request %s %s", request.Method, request.URL.Path)
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	readClient, err := worldread.New(worldread.Config{
		BaseURL: server.URL, Credential: "read-token", WorldID: worldID, TimelineID: timeline,
		TimelineEpoch: "epoch-home", ActorIDs: []string{worldActor}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	lifeClient, err := worldread.NewLife(worldread.LifeConfig{
		BaseURL: server.URL, Credential: "life-token", WorldID: worldID, TimelineID: timeline,
		TimelineEpoch: "epoch-home", ActorIDs: []string{actorID}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close routine state: %v", err)
		}
	})
	progress := routineProgress{Actors: []routineActorProgress{{ActorID: actorID, TaskID: modulecore.NewTaskID()}}, World: &routineWorldProgress{
		Operations: []routineWorldOperation{{
			Purpose: purpose, Key: key, Action: worldread.HomeActionMove, EventKind: "life.home.move.started", ActorID: actorID,
			Payload: worldread.HomeActionPayload{ActorID: actorID, RoomID: "private-mio"},
		}},
	}}
	encoded, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.DraftIntent(ctx, agentroutine.Intent{
		ID: intentID, ActorID: actorID, Date: "2026-11-14", Kind: agentroutine.ActivityHobby,
		Label: "fashion", Step: "world_action_prepared", Output: string(encoded), CreatedAt: time.Date(2026, 11, 14, 7, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &agentRoutineRuntime{
		store: store, world: readClient, worldLife: lifeClient, worldActorIDs: map[string]string{worldActor: actorID},
		gameSessions: &routineAdmissionGameReader{sessions: []gameObserverActiveAgentSession{{
			GameID: "nethack", SessionID: "nh-mio", Personas: []string{actorID}, Status: "running",
		}}},
	}
	state, progress, operation, err := runtime.performRoutineWorldAction(ctx, state, progress, purpose,
		worldread.HomeActionMove, "life.home.move.started", worldread.HomeActionPayload{ActorID: actorID, RoomID: "private-mio"})
	if err != nil {
		t.Fatalf("reconcile same persisted action during a game session: %v", err)
	}
	if commandCalls != 1 || operation.Key != key || !operation.EventVerified || operation.ActivityID != "move-mio-1" {
		t.Fatalf("reconciled operation = %+v calls=%d, want verified receipt on original key", operation, commandCalls)
	}
	if len(progress.World.Operations) != 1 || progress.World.Operations[0].Key != key || !progress.World.Operations[0].EventVerified {
		t.Fatalf("reconciliation rewrote or duplicated the prepared operation: %+v", progress.World.Operations)
	}
	foreignKeyProgress := progress
	foreignKeyProgress.World = &routineWorldProgress{Operations: append([]routineWorldOperation(nil), progress.World.Operations...)}
	foreignKeyProgress.World.Operations[0].Key = "foreign-world-operation-key"
	if _, _, _, scopeErr := runtime.performRoutineWorldAction(ctx, state, foreignKeyProgress, purpose,
		worldread.HomeActionMove, "life.home.move.started", worldread.HomeActionPayload{ActorID: actorID, RoomID: "private-mio"}); scopeErr == nil || commandCalls != 1 {
		t.Fatalf("foreign persisted key was replayed: err=%v command calls=%d", scopeErr, commandCalls)
	}
	stored, err := store.Load(ctx, actorID)
	if err != nil || stored.Pending == nil || stored.Pending.ID != state.Pending.ID || !stored.Pending.RetryAfter.IsZero() {
		t.Fatalf("pending after exact-key recovery = %+v err=%v; want same intent without retry cooldown", stored.Pending, err)
	}
}

func TestRoutineGameAdmissionAllowsOnlyBoundedWorldRecoveryAndAuthenticatedWake(t *testing.T) {
	intent := &agentroutine.Intent{ID: "wake-intent", ActorID: "mio", Kind: agentroutine.ActivityWake}
	binding := &routineWorldSleepBinding{
		ActorID: "mio", WorldActorID: "mio-world", ActivityID: "sleep-mio-1", RoomID: "private-mio", SlotID: "bed-mio",
		StartedAt: time.Date(2026, 11, 14, 1, 0, 0, 0, time.UTC), EventID: "sleep-event", TransactionID: "sleep-txn", Seq: 5,
		LayoutSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SceneSHA256:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	progress := routineProgress{World: &routineWorldProgress{SleepStart: binding}}
	if !routineWorldActionIsRecovery(intent, progress, worldread.HomeActionWake,
		worldread.HomeActionPayload{ActorID: "mio", ActivityID: binding.ActivityID}, false) {
		t.Fatal("authenticated prior-intent sleep binding did not authorize wake recovery")
	}
	wrongWakeActor := *intent
	wrongWakeActor.ActorID = "shiro"
	if routineWorldActionIsRecovery(&wrongWakeActor, progress, worldread.HomeActionWake,
		worldread.HomeActionPayload{ActorID: "mio", ActivityID: binding.ActivityID}, false) {
		t.Fatal("authenticated sleep binding for another actor authorized the current wake intent")
	}
	progress.World.SleepStart = nil
	if routineWorldActionIsRecovery(intent, progress, worldread.HomeActionWake,
		worldread.HomeActionPayload{ActorID: "mio", ActivityID: binding.ActivityID}, false) {
		t.Fatal("wake without an authenticated sleep binding was admitted")
	}
	start := routineWorldOperation{
		Purpose: "move-start", Key: routineWorldOperationKey("hobby-intent", "mio", "move-start"),
		Action: worldread.HomeActionMove, EventKind: "life.home.move.started", ActorID: "mio",
		Payload:   worldread.HomeActionPayload{ActorID: "mio", RoomID: "private-mio"},
		ReceiptID: "move-receipt", ReceiptStatus: "committed", EventVerified: true, ActivityID: "move-mio-2", ActivityKind: "move",
	}
	hobby := &agentroutine.Intent{ID: "hobby-intent", ActorID: "mio", Kind: agentroutine.ActivityHobby}
	progress = routineProgress{World: &routineWorldProgress{Operations: []routineWorldOperation{start}}}
	if !routineWorldActionIsRecovery(hobby, progress, worldread.HomeActionStop,
		worldread.HomeActionPayload{ActorID: "mio", ActivityID: start.ActivityID}, false) {
		t.Fatal("owned stop for an exact verified activity was not admitted")
	}
	if routineWorldActionIsRecovery(hobby, progress, worldread.HomeActionStop,
		worldread.HomeActionPayload{ActorID: "mio", ActivityID: "foreign-activity"}, false) {
		t.Fatal("stop for an unowned activity was admitted")
	}
}

func TestRoutineGameAdmissionPerformsAuthenticatedWakeRecoveryWithoutCurrentSleepOperation(t *testing.T) {
	ctx := context.Background()
	started := time.Date(2026, time.November, 14, 1, 0, 0, 0, time.UTC)
	binding := &routineWorldSleepBinding{
		ActorID: "mio", WorldActorID: "mio-world", ActivityID: "sleep-mio-prior", RoomID: "private-mio", SlotID: "bed-mio",
		StartedAt: started, EventID: "sleep-start-event", TransactionID: "sleep-start-txn", Seq: 5,
		LayoutSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		SceneSHA256:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	for _, test := range []struct {
		name   string
		reader *routineAdmissionGameReader
	}{
		{name: "active session", reader: &routineAdmissionGameReader{sessions: []gameObserverActiveAgentSession{{
			GameID: "herzog_zwei", SessionID: "hz-mio", Personas: []string{"mio"}, Status: "running",
		}}}},
		{name: "unknown session state", reader: &routineAdmissionGameReader{err: errors.New("observer unavailable")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var commandCalls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				switch {
				case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/commands"):
					commandCalls++
					var command struct {
						Action  string                      `json:"action"`
						Payload worldread.HomeActionPayload `json:"payload"`
					}
					if err := json.NewDecoder(request.Body).Decode(&command); err != nil {
						t.Errorf("decode authenticated wake command: %v", err)
					}
					if command.Action != worldread.HomeActionWake || command.Payload.ActorID != binding.ActorID ||
						command.Payload.ActivityID != binding.ActivityID {
						t.Errorf("wake command = %+v; want exact bound sleep activity", command)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"schema_version":1,"receipt":{"receipt_id":"receipt-wake","status":"committed","transaction_id":"wake-txn","event_ids":["wake-event"],"first_seq":6,"last_seq":6,"state_seq":6,"recorded_at":"2026-11-14T06:00:00Z"}}`))
				case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/events"):
					w.Header().Set("Content-Type", "application/json")
					payload := map[string]any{
						"actor_id": binding.WorldActorID, "activity_id": binding.ActivityID, "activity_kind": "sleep",
						"room_id": binding.RoomID, "slot_id": binding.SlotID,
						"started_at":    binding.StartedAt.Format(time.RFC3339Nano),
						"ended_at":      binding.StartedAt.Add(5 * time.Hour).Format(time.RFC3339Nano),
						"layout_sha256": binding.LayoutSHA256, "scene_sha256": binding.SceneSHA256,
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"schema_version": 1, "events": []any{map[string]any{
							"seq": 6, "transaction_id": "wake-txn", "event_id": "wake-event",
							"kind": "life.home.sleep.woke", "payload": payload, "recorded_at": "2026-11-14T06:00:00Z",
						}}, "next_seq": 6, "done": true,
					})
				default:
					t.Errorf("unexpected WORLD request %s %s", request.Method, request.URL.Path)
					http.NotFound(w, request)
				}
			}))
			defer server.Close()
			read, err := worldread.New(worldread.Config{
				BaseURL: server.URL, Credential: "read-token", WorldID: "world-home", TimelineID: "timeline-home",
				TimelineEpoch: "epoch-home", ActorIDs: []string{binding.WorldActorID}, Timeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			life, err := worldread.NewLife(worldread.LifeConfig{
				BaseURL: server.URL, Credential: "life-token", WorldID: "world-home", TimelineID: "timeline-home",
				TimelineEpoch: "epoch-home", ActorIDs: []string{binding.ActorID}, Timeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			store, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Errorf("close routine state: %v", err)
				}
			})
			progress := routineProgress{Actors: []routineActorProgress{{ActorID: binding.ActorID, TaskID: modulecore.NewTaskID()}},
				World: &routineWorldProgress{SleepStart: binding}}
			encoded, err := json.Marshal(progress)
			if err != nil {
				t.Fatal(err)
			}
			state, err := store.DraftIntent(ctx, agentroutine.Intent{
				ID: "fresh-wake-intent", ActorID: binding.ActorID, Date: "2026-11-14", Kind: agentroutine.ActivityWake,
				Step: "wake_prepared", Output: string(encoded), CreatedAt: started.Add(5 * time.Hour),
			})
			if err != nil {
				t.Fatal(err)
			}
			runtime := &agentRoutineRuntime{
				store: store, world: read, worldLife: life, worldActorIDs: map[string]string{binding.WorldActorID: binding.ActorID},
				gameSessions: test.reader,
			}
			state, progress, operation, err := runtime.performRoutineWorldAction(ctx, state, progress, "wake:"+binding.ActivityID,
				worldread.HomeActionWake, "life.home.sleep.woke",
				worldread.HomeActionPayload{ActorID: binding.ActorID, ActivityID: binding.ActivityID})
			if err != nil || commandCalls != 1 || !operation.EventVerified || operation.Key != routineWorldOperationKey(state.Pending.ID, binding.ActorID, "wake:"+binding.ActivityID) {
				t.Fatalf("authenticated wake during %s = operation:%+v calls:%d err:%v; want one exact verified wake", test.name, operation, commandCalls, err)
			}
			if progress.World == nil || len(progress.World.Operations) != 1 || progress.World.Operations[0].Action != worldread.HomeActionWake {
				t.Fatalf("wake recovery did not preserve the prior-intent binding and current wake operation: %+v", progress.World)
			}
		})
	}
}

func TestRoutineGameAdmissionSettlesWorldThenPausesExactPersistedRun(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, time.November, 14, 7, 0, 0, 0, time.UTC)
	const (
		actorID      = "mio"
		worldActorID = "mio-world"
		activityID   = "move-mio-game-1"
		routeID      = "route-mio-game-1"
		sceneSHA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		worldID      = "world-home"
		timelineID   = "timeline-home"
	)
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
	const intentID = "active-game-world-settlement"
	purpose := "relax-start"
	taskID := modulecore.NewTaskID()
	started := clock.Add(-time.Minute)
	due := clock.Add(time.Hour)
	run := startRoutineAdmissionRun(t, tasks, agentRoutineModuleID, actorID, taskID)
	start := routineWorldOperation{
		Purpose: purpose, Key: routineWorldOperationKey(intentID, actorID, purpose),
		Action: worldread.HomeActionMove, EventKind: "life.home.move.started", ActorID: actorID,
		Payload:   worldread.HomeActionPayload{ActorID: actorID, RoomID: "private-mio"},
		ReceiptID: "receipt-move-start", ReceiptStatus: "committed", EventVerified: true,
		ActivityID: activityID, ActivityKind: "move", RoomID: "hallway", TargetRoomID: "private-mio",
		RouteID: routeID, LayoutSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		SceneSHA256: sceneSHA, StartedAt: started, DueAt: due,
	}
	progress := routineProgress{Actors: []routineActorProgress{{ActorID: actorID, TaskID: taskID, RunID: run.run.RunID}},
		World: &routineWorldProgress{Operations: []routineWorldOperation{start}}}
	encoded, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	state, err := runtime.store.DraftIntent(ctx, agentroutine.Intent{
		ID: intentID, ActorID: actorID, Date: clock.Format("2006-01-02"), Kind: agentroutine.ActivityRelax,
		Label: "quiet-time", Step: "world_move_started", Output: string(encoded), CreatedAt: started,
	})
	if err != nil {
		t.Fatalf("draft pending WORLD move: %v", err)
	}
	stopCommandCalls, snapshotCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/snapshot"):
			snapshotCalls++
			w.Header().Set("Content-Type", "application/json")
			actor := map[string]any{"room_id": "hallway"}
			stateSeq := 7
			if stopCommandCalls == 0 {
				actor["activity_id"] = activityID
				actor["activity_kind"] = "move"
				actor["activity_started_at"] = started.Format(time.RFC3339Nano)
				actor["activity_due_at"] = due.Format(time.RFC3339Nano)
				actor["target_room_id"] = "private-mio"
				actor["move_route_id"] = routeID
				actor["home_scene_sha256"] = sceneSHA
			} else {
				stateSeq = 8
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schema_version": 1, "world_id": worldID, "timeline_id": timelineID, "timeline_epoch": "epoch-home",
				"state_seq": stateSeq, "state_hash": strings.Repeat("b", 64),
				"state": map[string]any{"actors": map[string]any{worldActorID: actor}},
			})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/commands"):
			var command struct {
				Action  string                      `json:"action"`
				Payload worldread.HomeActionPayload `json:"payload"`
			}
			if err := json.NewDecoder(request.Body).Decode(&command); err != nil {
				t.Errorf("decode stop command: %v", err)
			}
			if command.Action != worldread.HomeActionStop || command.Payload.ActorID != actorID || command.Payload.ActivityID != activityID {
				t.Errorf("WORLD recovery command = %+v; want exact stop for %s", command, activityID)
			}
			stopCommandCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schema_version":1,"receipt":{"receipt_id":"receipt-stop","status":"committed","transaction_id":"txn-stop","event_ids":["event-stop"],"first_seq":8,"last_seq":8,"state_seq":8,"recorded_at":"2026-11-14T07:00:00Z"}}`))
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/events"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schema_version":1,"events":[{"seq":8,"transaction_id":"txn-stop","event_id":"event-stop","kind":"life.home.activity.stopped","payload":{"actor_id":"mio-world","activity_id":"move-mio-game-1","activity_kind":"move","room_id":"hallway","target_room_id":"private-mio","route_id":"route-mio-game-1","layout_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","scene_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"recorded_at":"2026-11-14T07:00:00Z"}],"next_seq":8,"done":true}`))
		default:
			t.Errorf("unexpected WORLD request %s %s", request.Method, request.URL.Path)
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	runtime.world, err = worldread.New(worldread.Config{
		BaseURL: server.URL, Credential: "read-token", WorldID: worldID, TimelineID: timelineID,
		TimelineEpoch: "epoch-home", ActorIDs: []string{worldActorID}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.worldLife, err = worldread.NewLife(worldread.LifeConfig{
		BaseURL: server.URL, Credential: "life-token", WorldID: worldID, TimelineID: timelineID,
		TimelineEpoch: "epoch-home", ActorIDs: []string{actorID}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.worldActorIDs = map[string]string{worldActorID: actorID}
	runtime.worldNow = func() time.Time { return clock }
	runtime.cfg = &config.Config{}
	reader := &routineAdmissionGameReader{sessions: []gameObserverActiveAgentSession{{
		GameID: "survival_garden", SessionID: "sg-mio", Personas: []string{actorID}, Status: "running",
	}}}
	runtime.gameSessions = reader
	if err := runtime.run(ctx, nil, actorID, agentroutine.Plan{Kind: agentroutine.ActivityRelax}); err != nil {
		t.Fatalf("settle pending WORLD operation during active game: %v", err)
	}
	if stopCommandCalls != 1 || snapshotCalls < 2 {
		t.Fatalf("WORLD settlement calls = stop:%d snapshot:%d; want one exact stop and verified post-stop snapshot", stopCommandCalls, snapshotCalls)
	}
	stored, err := runtime.store.Load(ctx, actorID)
	if err != nil || stored.Pending == nil || stored.Pending.ID != state.Pending.ID || !stored.Pending.RetryAfter.IsZero() {
		t.Fatalf("pending intent after game recovery = %+v err=%v; want same intent without cooldown", stored.Pending, err)
	}
	settled, err := decodeRoutineProgress(stored.Pending.Output)
	if err != nil || settled.EffectCompleted || routineProgressNeedsWorldSettlement(settled) {
		t.Fatalf("recovered routine state = effect-completed:%t settlement-needed:%t err:%v; want settled WORLD and unfinished intent",
			settled.EffectCompleted, routineProgressNeedsWorldSettlement(settled), err)
	}
	task, err := tasks.Get(ctx, taskID)
	if err != nil || task.Status != domaintask.StatusWaiting {
		t.Fatalf("persisted routine Task after settlement = %+v err=%v; want paused/waiting", task, err)
	}
	runs, err := tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
	if err != nil || len(runs) != 1 || runs[0].RunID != run.run.RunID || runs[0].Status != domaintask.RunStatusWaiting {
		t.Fatalf("persisted routine Run after settlement = %+v err=%v; want exact Run paused/waiting", runs, err)
	}
	reader.sessions = nil
	work, found, err := runtime.idleSource(nil)(ctx, actorID)
	if err != nil || !found || work.ID == "" {
		t.Fatalf("pending routine rediscovery after last game = %+v found=%t err=%v; want immediate reoffer", work, found, err)
	}
}

var _ gameObserverActiveSessionsReader = (*routineAdmissionGameReader)(nil)
