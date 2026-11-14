package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

func TestRoutineWorldStoppedActivityRequiresVerifiedReceiptBeforeRetry(t *testing.T) {
	progress := routineProgress{World: &routineWorldProgress{Operations: []routineWorldOperation{
		{Purpose: "relax-room:move", ActorID: "mio", ActivityID: "move-1", Action: "move", EventVerified: true, ReceiptStatus: "committed"},
		{Purpose: "interrupt-stop:move-1", ActorID: "mio", Payload: worldread.HomeActionPayload{ActorID: "mio", ActivityID: "move-1"},
			Action: worldread.HomeActionStop, EventKind: "life.home.activity.stopped", EventVerified: true, ReceiptStatus: "committed"},
	}}}
	if !routineWorldActivityWasConfirmedStopped(progress, "mio", "move-1") {
		t.Fatal("verified stop receipt did not authorize a new move attempt")
	}
	if got := routineWorldRetryPurpose(progress, "relax-room:move"); got != "relax-room:move:retry:1" {
		t.Fatalf("retry purpose = %q, want unique retry suffix", got)
	}
	progress.World.Operations[1].EventVerified = false
	if routineWorldActivityWasConfirmedStopped(progress, "mio", "move-1") {
		t.Fatal("unverified stop receipt authorized a retry")
	}
}

func TestRoutineWorldRelaxRoomUsesPolicyScope(t *testing.T) {
	runtime := &agentRoutineRuntime{cfg: &config.Config{Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{
		PrivateRooms: map[string]string{"mio": "mio-room"}, SharedRooms: []string{"dining", "living"},
	}}}}
	if got := runtime.routineWorldRelaxRoom("mio", agentroutine.RelaxRoomPrivate); got != "mio-room" {
		t.Fatalf("private relaxation room = %q, want configured actor room", got)
	}
	if got := runtime.routineWorldRelaxRoom("mio", agentroutine.RelaxRoomShared); got != "living" {
		t.Fatalf("shared relaxation room = %q, want preferred shared room", got)
	}
	if got := runtime.routineWorldRelaxRoom("mio", agentroutine.RelaxRoomUnknown); got != "" {
		t.Fatalf("unknown relaxation room scope = %q, want empty", got)
	}
}

func TestExecuteHobbyRequiresConfiguredPrivateRoomWhenWORLDLifeIsEnabled(t *testing.T) {
	runtime := &agentRoutineRuntime{
		cfg:       &config.Config{Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{PrivateRooms: map[string]string{}}}},
		worldLife: &worldread.LifeClient{}, now: time.Now,
	}
	state := agentroutine.State{Pending: &agentroutine.Intent{
		ID: "hobby-1", ActorID: "shiro", Kind: agentroutine.ActivityHobby, Label: "fitness_research",
	}}
	_, _, err := runtime.executeHobby(context.Background(), nil, state, routineProgress{}, routinePreparedRun{})
	if err == nil || !strings.Contains(err.Error(), "configured private room") {
		t.Fatalf("executeHobby() error = %v, want a private-room configuration error before WORLD reads or WebGather", err)
	}
}

func TestRecordRoutineRelaxOnlyAfterConfirmedRoomObservationAndDiaryIncludesIt(t *testing.T) {
	store, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = taskStore.Close() })
	runtime := &agentRoutineRuntime{
		store: store, tasks: taskmanager.New(taskStore, taskmanager.DefaultParallelLimits()), ownerID: "ren",
		cfg: &config.Config{Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{
			PrivateRooms: map[string]string{"mio": "upper-room-2-1"}, SharedRooms: []string{"living"},
		}}},
	}
	intent := agentroutine.Intent{ID: "relax-1", ActorID: "mio", Date: "2026-11-14", Kind: agentroutine.ActivityRelax}
	now := time.Date(2026, time.November, 14, 7, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	if err := runtime.recordRoutineFacts(context.Background(), intent, routineProgress{}, now); err != nil {
		t.Fatal(err)
	}
	facts, err := store.ListFacts(context.Background(), "mio", intent.Date)
	if err != nil || len(facts) != 0 {
		t.Fatalf("unconfirmed relaxation facts = %v, err=%v; want none", facts, err)
	}

	progress := routineProgress{World: &routineWorldProgress{RelaxRoomID: "living", RelaxObservedAt: now.UTC()}}
	if err := runtime.recordRoutineFacts(context.Background(), intent, progress, now); err != nil {
		t.Fatal(err)
	}
	facts, err = store.ListFacts(context.Background(), "mio", intent.Date)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || facts[0].Kind != agentroutine.ActivityRelax || !facts[0].Confirmed || !strings.Contains(facts[0].Summary, "リビング") {
		t.Fatalf("confirmed relaxation facts = %+v, want one confirmed living-room record", facts)
	}
	body, err := runtime.buildDiaryBody(context.Background(), "mio", intent.Date)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, facts[0].Summary) {
		t.Fatalf("diary body omitted confirmed relaxation: %s", body)
	}
}
