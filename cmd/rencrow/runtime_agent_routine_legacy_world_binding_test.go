package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
)

func TestAgentRoutineRuntimeExcludesLegacyLuminaWorldBinding(t *testing.T) {
	workspace := t.TempDir()
	readTokenPath := filepath.Join(workspace, "world-read.token")
	if err := os.WriteFile(readTokenPath, []byte("world-read-token-with-enough-length-1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	lifeTokenPath := filepath.Join(workspace, "world-life.token")
	if err := os.WriteFile(lifeTokenPath, []byte("world-life-token-with-enough-length-5678"), 0o600); err != nil {
		t.Fatal(err)
	}

	worldActors := map[string]string{
		"mio": "world-mio", "shiro": "world-shiro", "midori": "world-midori", "kuro": "world-kuro",
		"aka": "world-aka", "ao": "world-ao", "kin": "world-kin", "gin": "world-gin",
		"lumina": "world-lumina",
	}
	cfg := &config.Config{
		WorkspaceDir:  filepath.Join(workspace, "core"),
		LocalAgentOps: config.LocalAgentOpsConfig{UserID: "ren"},
		Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{
			Enabled: true, WorldURL: "http://127.0.0.1:9000", WorldID: "living-world",
			TimelineID: "main", TimelineEpoch: "epoch-1", WorldReadTokenFile: readTokenPath,
			WorldLifeTokenFile: lifeTokenPath, WorldActors: worldActors,
		}},
	}
	routine, err := newAgentRoutineRuntime(cfg, &Dependencies{taskManager: taskmanager.New(nil, taskmanager.DefaultParallelLimits())}, nil, nil)
	if err != nil {
		t.Fatalf("newAgentRoutineRuntime: %v", err)
	}
	t.Cleanup(func() {
		if err := routine.Close(); err != nil {
			t.Errorf("close routine state: %v", err)
		}
	})

	if routine.world == nil || routine.worldLife == nil {
		t.Fatal("authenticated WORLD reader and life client were not constructed")
	}
	if _, err := routine.worldLife.Command(context.Background(), "legacy-lumina-attempt", worldread.HomeActionMove, worldread.HomeActionPayload{
		ActorID: "lumina", RoomID: "living",
	}); err == nil || !strings.Contains(err.Error(), "outside the configured life scope") {
		t.Fatalf("WORLD life client accepted or misreported Lumina scope: %v", err)
	}
	if len(routine.worldActorIDs) != len(agentHeartbeatActorIDs) {
		t.Fatalf("effective WORLD actor map has %d actors, want %d: %#v", len(routine.worldActorIDs), len(agentHeartbeatActorIDs), routine.worldActorIDs)
	}
	for _, coreActorID := range agentHeartbeatActorIDs {
		worldActorID, ok := routine.routineWorldActorID(coreActorID)
		if !ok || routine.worldActorIDs[worldActorID] != coreActorID {
			t.Errorf("effective WORLD binding for %q = %q, want exact round trip", coreActorID, worldActorID)
		}
	}
	if _, ok := routine.routineWorldActorID("lumina"); ok {
		t.Fatal("legacy Lumina binding entered the effective heartbeat WORLD scope")
	}
	if !runtimeRoutineHouseViewHasCompleteActorScope(routine) {
		t.Fatal("legacy Lumina binding made the canonical HOUSE actor scope incomplete")
	}
	luminaState, err := routine.store.Load(context.Background(), "lumina")
	if err != nil {
		t.Fatalf("load Lumina routine state: %v", err)
	}
	if luminaState.ActivatedOn != "" || luminaState.NextDiaryDate != "" {
		t.Fatalf("Lumina received heartbeat routine state: %+v", luminaState)
	}
	if len(cfg.Heartbeat.Routine.WorldActors) != 9 || cfg.Heartbeat.Routine.WorldActors["lumina"] != "world-lumina" {
		t.Fatalf("runtime normalized the retained legacy config binding: %#v", cfg.Heartbeat.Routine.WorldActors)
	}
	for _, actorID := range agentHeartbeatActorIDs {
		state, err := routine.store.Load(context.Background(), actorID)
		if err != nil {
			t.Errorf("load canonical routine state for %q: %v", actorID, err)
		} else if state.ActivatedOn == "" {
			t.Errorf("canonical actor %q was not initialized", actorID)
		}
	}
}
