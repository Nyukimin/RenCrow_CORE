package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGameBridgeStoreDeduplicatesEventID(t *testing.T) {
	store := NewGameBridgeStore(filepath.Join(t.TempDir(), "game_bridge_events.jsonl"))
	req := GameResultRequest{
		GameID:          "territory_commander",
		SessionID:       "tc_test",
		Turn:            3,
		Persona:         "mio",
		ExecutedActions: []string{"defend"},
		Result:          map[string]any{"success": true, "event": "defended_center"},
	}

	first, err := store.SaveGameBridgeResult(context.Background(), req)
	if err != nil {
		t.Fatalf("first SaveGameBridgeResult returned error: %v", err)
	}
	second, err := store.SaveGameBridgeResult(context.Background(), req)
	if err != nil {
		t.Fatalf("second SaveGameBridgeResult returned error: %v", err)
	}
	if first.EventID != second.EventID || first.CreatedAt != second.CreatedAt {
		t.Fatalf("duplicate save did not return existing event\nfirst=%+v\nsecond=%+v", first, second)
	}

	events, err := store.RecentGameBridgeEvents(context.Background(), "territory_commander", "tc_test", 10)
	if err != nil {
		t.Fatalf("RecentGameBridgeEvents returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%d want 1", len(events))
	}
}

func TestGameBridgeStoreBuildsSessionSummaries(t *testing.T) {
	store := NewGameBridgeStore(filepath.Join(t.TempDir(), "game_bridge_events.jsonl"))
	for _, req := range []GameResultRequest{
		{
			GameID:          "survival_garden",
			SessionID:       "sg_test",
			Turn:            1,
			Persona:         "mio",
			Decision:        GameBrainDecision{Intent: "drink", MemoryRefs: []string{"previous"}},
			ExecutedActions: []string{"drink"},
			Result:          map[string]any{"success": true, "events": []string{"drank_water"}},
		},
		{
			GameID:          "survival_garden",
			SessionID:       "sg_test",
			Turn:            2,
			Persona:         "mio",
			Decision:        GameBrainDecision{Intent: "rest"},
			ExecutedActions: []string{"rest"},
			Result:          map[string]any{"success": true, "event": "rested"},
		},
		{
			GameID:          "territory_commander",
			SessionID:       "tc_test",
			Turn:            1,
			Persona:         "mio",
			Decision:        GameBrainDecision{Intent: "defend"},
			ExecutedActions: []string{"defend"},
			Result:          map[string]any{"success": true, "events": []string{"held_line"}},
		},
	} {
		if _, err := store.SaveGameBridgeResult(context.Background(), req); err != nil {
			t.Fatalf("SaveGameBridgeResult returned error: %v", err)
		}
	}

	sessions, skipped, err := store.RecentGameBridgeSessions(context.Background(), 20)
	if err != nil {
		t.Fatalf("RecentGameBridgeSessions returned error: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("skipped=%d want 0", skipped)
	}
	sg, ok := findGameBridgeSession(sessions, "survival_garden", "sg_test")
	if !ok {
		t.Fatalf("missing survival session: %+v", sessions)
	}
	if sg.CandidateCount != 2 || sg.LatestTurn != 2 || sg.MemoryMode != "candidate_only" {
		t.Fatalf("unexpected survival session: %+v", sg)
	}
}

func TestGameBridgeStoreFiltersEventViewsAndCountsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "game_bridge_events.jsonl")
	valid := GameBridgeEvent{
		EventID:           "game:survival_garden:sg_test:turn_1",
		CandidateMemoryID: "game:survival_garden:sg_test:turn_1:candidate",
		GameID:            "survival_garden",
		SessionID:         "sg_test",
		Turn:              1,
		Persona:           "mio",
		Decision:          GameBrainDecision{Intent: "drink", MemoryRefs: []string{"ref_1"}},
		ExecutedActions:   []string{"drink"},
		Result:            map[string]any{"success": true, "events": []any{"drank_water"}},
		MemoryState:       "candidate",
		Promoted:          false,
		CreatedAt:         "2026-07-02T00:00:00Z",
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if err := os.WriteFile(path, []byte("not-json\n{}\n"+string(encoded)+"\n"), 0644); err != nil {
		t.Fatalf("write temp log: %v", err)
	}
	store := NewGameBridgeStore(path)

	events, skipped, err := store.RecentGameBridgeEventViews(context.Background(), "survival_garden", "sg_test", 50)
	if err != nil {
		t.Fatalf("RecentGameBridgeEventViews returned error: %v", err)
	}
	if skipped != 2 {
		t.Fatalf("skipped=%d want 2", skipped)
	}
	if len(events) != 1 {
		t.Fatalf("events=%d want 1: %+v", len(events), events)
	}
	if events[0].DecisionIntent != "drink" || len(events[0].ResultEvents) != 1 || events[0].ResultEvents[0] != "drank_water" {
		t.Fatalf("unexpected event view: %+v", events[0])
	}
}

func TestGameBridgeStoreMissingLogIsUnavailableForObserver(t *testing.T) {
	store := NewGameBridgeStore(filepath.Join(t.TempDir(), "missing", "game_bridge_events.jsonl"))
	_, _, err := store.RecentGameBridgeSessions(context.Background(), 20)
	if !errors.Is(err, ErrGameBridgeStoreUnavailable) {
		t.Fatalf("err=%v want ErrGameBridgeStoreUnavailable", err)
	}
}

func findGameBridgeSession(sessions []GameBridgeSessionSummary, gameID string, sessionID string) (GameBridgeSessionSummary, bool) {
	for _, session := range sessions {
		if session.GameID == gameID && session.SessionID == sessionID {
			return session, true
		}
	}
	return GameBridgeSessionSummary{}, false
}
