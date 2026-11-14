package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
)

func TestInitializeActorsBackfillsOnlyConfirmedPriorDayGameHistory(t *testing.T) {
	ctx := context.Background()
	location := time.FixedZone("JST", 9*60*60)
	now := time.Date(2026, time.November, 14, 10, 0, 0, 0, location)
	priorDate := "2026-11-13"
	priorStart, _, err := routineFactDay(priorDate)
	if err != nil {
		t.Fatal(err)
	}
	gamePath := filepath.Join(t.TempDir(), "game_bridge_events.jsonl")
	event := viewer.GameBridgeEvent{
		EventID: "confirmed-mio-turn", GameID: "nethack", SessionID: "prior_day_session", Turn: 1, Persona: "mio",
		ExecutedActions: []string{"wait"}, Result: map[string]any{"success": true},
		CreatedAt: priorStart.Add(15 * time.Hour).Format(time.RFC3339Nano),
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gamePath, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	routineStore, err := agentroutine.Open(filepath.Join(t.TempDir(), "routine.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = routineStore.Close() })
	runtime := &agentRoutineRuntime{
		cfg:   &config.Config{Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{Enabled: true}}},
		deps:  &Dependencies{gameBridgeStore: viewer.NewGameBridgeStore(gamePath)},
		store: routineStore,
		now:   func() time.Time { return now },
	}
	if err := runtime.initializeActors(ctx); err != nil {
		t.Fatalf("initializeActors: %v", err)
	}
	for _, actorID := range agentHeartbeatActorIDs {
		state, err := routineStore.Load(ctx, actorID)
		if err != nil {
			t.Fatal(err)
		}
		wantDate := now.In(location).Format("2006-01-02")
		if actorID == "mio" {
			wantDate = priorDate
		}
		if state.ActivatedOn != wantDate || state.NextDiaryDate != wantDate {
			t.Errorf("actor %s activation=%q next diary=%q, want only confirmed-history Mio to start at %q", actorID, state.ActivatedOn, state.NextDiaryDate, wantDate)
		}
	}
	if err := runtime.initializeActors(ctx); err != nil {
		t.Fatalf("second initializeActors: %v", err)
	}
	mio, err := routineStore.Load(ctx, "mio")
	if err != nil {
		t.Fatal(err)
	}
	if mio.ActivatedOn != priorDate || mio.NextDiaryDate != priorDate {
		t.Fatalf("reinitialization moved the durable activation checkpoint: %+v", mio)
	}
}
