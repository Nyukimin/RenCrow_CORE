package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
)

func TestCollectRoutineGameFactsProjectsOnlySafeJapaneseSessionMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "game_bridge_events.jsonl")
	dayStart, dayEnd, err := routineFactDay("2026-11-13")
	if err != nil {
		t.Fatalf("routineFactDay returned error: %v", err)
	}
	firstAt := dayStart.Add(time.Hour)
	lastAt := dayStart.Add(2 * time.Hour)
	events := []viewer.GameBridgeEvent{
		{
			EventID: "legacy-mio-turn-1", GameID: "nethack", SessionID: "nh_session_1", Turn: 1, Persona: "mio",
			Decision:        viewer.GameBrainDecision{Intent: "move", Reason: "private decision reason"},
			ExecutedActions: []string{"move"},
			Result:          map[string]any{"success": true, "events": []string{"moved"}, "private_body": "private result body"},
			CreatedAt:       firstAt.Format(time.RFC3339Nano),
		},
		{
			EventID: "legacy-mio-turn-2", GameID: "nethack", SessionID: "nh_session_1", Turn: 2, Persona: "mio",
			Decision:        viewer.GameBrainDecision{Intent: "wait", Reason: "another private decision reason"},
			ExecutedActions: []string{"wait"},
			Result:          map[string]any{"success": true, "events": []string{"waited"}, "private_body": "another private result body"},
			CreatedAt:       lastAt.Format(time.RFC3339Nano),
		},
	}
	encoded := make([]byte, 0)
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal game result: %v", err)
		}
		encoded = append(encoded, line...)
		encoded = append(encoded, '\n')
	}
	if err := os.WriteFile(path, encoded, 0644); err != nil {
		t.Fatalf("write game result fixture: %v", err)
	}
	runtime := &agentRoutineRuntime{deps: &Dependencies{gameBridgeStore: viewer.NewGameBridgeStore(path)}}

	activities, err := runtime.collectRoutineGameFacts(context.Background(), "mio", "2026-11-13")
	if err != nil {
		t.Fatalf("collectRoutineGameFacts returned error: %v", err)
	}
	if len(activities) != 1 {
		t.Fatalf("activities=%+v want one game session fact", activities)
	}
	activity := activities[0]
	if activity.ActorID != "mio" || activity.Date != "2026-11-13" || activity.Kind != agentroutine.ActivityHobby ||
		activity.Key != "game-session:nethack:nh_session_1" || activity.Source != "canonical_game_session" ||
		activity.SourceID != "nethack:nh_session_1" || !activity.Confirmed || !activity.At.Equal(lastAt.UTC()) {
		t.Fatalf("unexpected routine activity: %+v", activity)
	}
	for _, expected := range []string{"NetHack", "2ターン", "初回 01:00", "最終 02:00 JST"} {
		if !strings.Contains(activity.Summary, expected) {
			t.Errorf("summary %q missing %q", activity.Summary, expected)
		}
	}
	serialized, err := json.Marshal(activity)
	if err != nil {
		t.Fatalf("marshal activity: %v", err)
	}
	for _, secret := range []string{"private decision reason", "private result body", "another private"} {
		if strings.Contains(string(serialized), secret) {
			t.Fatalf("routine activity exposed private game payload %q: %s", secret, serialized)
		}
	}
	if activity.Summary != "GAMESでNetHackを2ターン遊んだ（初回 01:00、最終 02:00 JST）" {
		t.Fatalf("unexpected bounded summary %q", activity.Summary)
	}
	if dayStart.IsZero() || dayEnd.Sub(dayStart) != 24*time.Hour {
		t.Fatalf("routine fact window is not the JST day: %s..%s", dayStart, dayEnd)
	}
}

func TestCollectRoutineGameFactsTreatsDisabledGamesAsEmptyAndConfiguredReadFailureAsError(t *testing.T) {
	activities, err := (&agentRoutineRuntime{}).collectRoutineGameFacts(context.Background(), "mio", "2026-11-13")
	if err != nil || len(activities) != 0 {
		t.Fatalf("disabled GAMES activities=%+v err=%v, want empty success", activities, err)
	}
	missingPath := filepath.Join(t.TempDir(), "missing.jsonl")
	runtime := &agentRoutineRuntime{deps: &Dependencies{gameBridgeStore: viewer.NewGameBridgeStore(missingPath)}}
	_, err = runtime.collectRoutineGameFacts(context.Background(), "mio", "2026-11-13")
	if !errors.Is(err, viewer.ErrGameBridgeStoreUnavailable) {
		t.Fatalf("configured missing store err=%v want ErrGameBridgeStoreUnavailable", err)
	}
	if _, err := runtime.collectRoutineGameFacts(context.Background(), "coder1", "2026-11-13"); err == nil {
		t.Fatal("noncanonical CORE actor must be rejected")
	}
	if _, err := runtime.collectRoutineGameFacts(context.Background(), "mio", "2026-02-30"); err == nil {
		t.Fatal("invalid routine date must be rejected")
	}
}
