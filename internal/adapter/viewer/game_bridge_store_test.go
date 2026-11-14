package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

func TestGameBridgeStoreKeepsEachPersonaPerTurnAndDeduplicatesPersonaRetry(t *testing.T) {
	store := NewGameBridgeStore(filepath.Join(t.TempDir(), "game_bridge_events.jsonl"))
	base := GameResultRequest{
		GameID:          "survival_garden",
		SessionID:       "sg_shared_turn",
		Turn:            7,
		ExecutedActions: []string{"rest"},
		Result:          map[string]any{"success": true, "event": "rested"},
	}
	mioRequest := base
	mioRequest.Persona = "mio"
	shiroRequest := base
	shiroRequest.Persona = "shiro"
	kuroRequest := base
	kuroRequest.Persona = "kuro"
	midoriRequest := base
	midoriRequest.Persona = "midori"

	mio, err := store.SaveGameBridgeResult(context.Background(), mioRequest)
	if err != nil {
		t.Fatalf("save mio result: %v", err)
	}
	shiro, err := store.SaveGameBridgeResult(context.Background(), shiroRequest)
	if err != nil {
		t.Fatalf("save shiro result: %v", err)
	}
	if _, err := store.SaveGameBridgeResult(context.Background(), kuroRequest); err != nil {
		t.Fatalf("save kuro result: %v", err)
	}
	if _, err := store.SaveGameBridgeResult(context.Background(), midoriRequest); err != nil {
		t.Fatalf("save midori result: %v", err)
	}
	mioRetry := mioRequest
	mioRetry.Result = map[string]any{"success": true, "event": "retry_must_not_append"}
	retried, err := store.SaveGameBridgeResult(context.Background(), mioRetry)
	if err != nil {
		t.Fatalf("retry mio result: %v", err)
	}

	if mio.EventID != "game:survival_garden:sg_shared_turn:turn_7:persona_mio" {
		t.Fatalf("mio event_id=%q", mio.EventID)
	}
	if shiro.EventID != "game:survival_garden:sg_shared_turn:turn_7:persona_shiro" {
		t.Fatalf("shiro event_id=%q", shiro.EventID)
	}
	if retried.EventID != mio.EventID || retried.CreatedAt != mio.CreatedAt {
		t.Fatalf("same-persona retry must return the existing event\nfirst=%+v\nretry=%+v", mio, retried)
	}

	events, err := store.RecentGameBridgeEvents(context.Background(), "survival_garden", "sg_shared_turn", 10)
	if err != nil {
		t.Fatalf("RecentGameBridgeEvents returned error: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("events=%d want 4: %+v", len(events), events)
	}
	seen := map[string]bool{}
	for _, event := range events {
		seen[event.Persona] = true
	}
	if !seen["mio"] || !seen["shiro"] || !seen["kuro"] || !seen["midori"] {
		t.Fatalf("same-turn persona events missing: %+v", events)
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
		EventID:           "game:survival_garden:sg_test:turn_1:persona_mio",
		CandidateMemoryID: "game:survival_garden:sg_test:turn_1:persona_mio:candidate",
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

func TestGameBridgeStorePlayedGameSessionFactsFiltersAndAggregates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "game_bridge_events.jsonl")
	from := time.Date(2026, time.October, 4, 15, 0, 0, 0, time.UTC)
	until := time.Date(2026, time.October, 5, 15, 0, 0, 0, time.UTC)
	base := GameBridgeEvent{
		GameID:    "nethack",
		SessionID: "session-1",
		Persona:   "mio",
		Turn:      1,
		Result:    map[string]any{"success": true, "events": []any{"moved"}},
	}
	event := func(id string, gameID, sessionID, persona string, turn int, createdAt string, actions []string, result map[string]any) GameBridgeEvent {
		item := base
		item.EventID = id
		item.GameID = gameID
		item.SessionID = sessionID
		item.Persona = persona
		item.Turn = turn
		item.CreatedAt = createdAt
		item.ExecutedActions = actions
		item.Result = result
		return item
	}
	playedAtStart := event("old-turn-1", "nethack", "session-1", "mio", 1, "2026-10-05T01:00:00+09:00", []string{"move"}, map[string]any{"success": true})
	playedLater := event("old-turn-2", "nethack", "session-1", "mio", 2, "2026-10-05T02:00:00Z", []string{"wait"}, map[string]any{"success": true})
	validGameOver := event("old-game-over", "nethack", "session-1", "mio", 3, "2026-10-05T14:30:00Z", nil, map[string]any{"success": true, "game_over": true, "events": []any{"victory"}})
	otherGameSession := event("other-game", "survival_garden", "session-1", "mio", 1, "2026-10-05T03:00:00Z", []string{"rest"}, map[string]any{"success": true})
	otherPersona := event("other-persona", "nethack", "session-1", "shiro", 4, "2026-10-05T04:00:00Z", []string{"move"}, map[string]any{"success": true})
	otherActor := event("other-actor", "nethack", "session-1", "coder1", 4, "2026-10-05T04:00:00Z", []string{"move"}, map[string]any{"success": true})
	launchMotive := event("launch-motive", "nethack", "session-1", "mio", -1, "2026-10-05T05:00:00Z", []string{"play_game"}, map[string]any{"success": true})
	failed := event("failed-turn", "nethack", "session-1", "mio", 5, "2026-10-05T06:00:00Z", []string{"move"}, map[string]any{"success": false})
	noAction := event("no-action", "nethack", "session-1", "mio", 6, "2026-10-05T07:00:00Z", nil, map[string]any{"success": true})
	invalidGameOver := event("invalid-game-over", "nethack", "session-1", "mio", 7, "2026-10-05T08:00:00Z", nil, map[string]any{"success": true, "game_over": true})
	blankAction := event("blank-action", "nethack", "session-1", "mio", 8, "2026-10-05T09:00:00Z", []string{"  "}, map[string]any{"success": true})
	invalidGameID := event("unknown-game", "not_a_game", "session-1", "mio", 9, "2026-10-05T10:00:00Z", []string{"move"}, map[string]any{"success": true})
	unsafeSessionID := event("unsafe-session", "nethack", "session:<private>", "mio", 10, "2026-10-05T10:30:00Z", []string{"move"}, map[string]any{"success": true})
	beforeWindow := event("before-window", "nethack", "session-1", "mio", 10, "2026-10-04T14:59:59Z", []string{"move"}, map[string]any{"success": true})
	atEnd := event("at-end", "nethack", "session-1", "mio", 11, "2026-10-05T15:00:00Z", []string{"move"}, map[string]any{"success": true})
	otherSession := event("other-session", "nethack", "session-2", "mio", 1, "2026-10-05T11:00:00Z", []string{"move"}, map[string]any{"success": true})
	crossUTCDay := event("tc-before-midnight", "territory_commander", "tc-session", "mio", 1, "2026-10-04T16:00:00Z", []string{"defend"}, map[string]any{"success": true})
	crossUTCDayLater := event("tc-after-midnight", "territory_commander", "tc-session", "mio", 2, "2026-10-05T14:59:59Z", []string{"attack"}, map[string]any{"success": true})
	akaPlayed := event("aka-event", "nethack", "aka-session", "aka", 1, "2026-10-05T12:30:00Z", []string{"move"}, map[string]any{"success": true})
	all := []GameBridgeEvent{
		playedAtStart, playedAtStart, playedLater, validGameOver, otherGameSession,
		otherPersona, otherActor, launchMotive, failed, noAction, invalidGameOver,
		blankAction, invalidGameID, unsafeSessionID, beforeWindow, atEnd, otherSession, crossUTCDay,
		crossUTCDayLater, akaPlayed,
	}
	for i := 0; i < 6; i++ {
		suffix := strconv.Itoa(i)
		all = append(all, event(
			"many-"+suffix,
			"nethack",
			"session-many-"+suffix,
			"mio",
			1,
			"2026-10-05T12:00:00Z",
			[]string{"move"},
			map[string]any{"success": true},
		))
	}
	encoded := make([]byte, 0)
	for _, item := range all {
		// Historical bridge lines predate the candidate decision and memory
		// metadata but still contain the executed result contract.
		line, err := json.Marshal(map[string]any{
			"event_id": item.EventID, "game_id": item.GameID, "session_id": item.SessionID,
			"turn": item.Turn, "persona": item.Persona, "executed_actions": item.ExecutedActions,
			"result": item.Result, "created_at": item.CreatedAt,
		})
		if err != nil {
			t.Fatalf("marshal fixture: %v", err)
		}
		encoded = append(encoded, line...)
		encoded = append(encoded, '\n')
	}
	if err := os.WriteFile(path, encoded, 0644); err != nil {
		t.Fatalf("write legacy game bridge log: %v", err)
	}

	// Read through a fresh store so facts come from durable JSONL records.
	facts, err := NewGameBridgeStore(path).PlayedGameSessionFacts(context.Background(), "mio", from, until)
	if err != nil {
		t.Fatalf("PlayedGameSessionFacts returned error: %v", err)
	}
	if len(facts) != 10 {
		t.Fatalf("facts=%d want 10: %+v", len(facts), facts)
	}
	if facts[0].GameID != "nethack" || facts[0].SessionID != "session-1" || facts[0].TurnCount != 3 {
		t.Fatalf("unexpected first fact: %+v", facts[0])
	}
	if !facts[0].FirstAt.Equal(from.Add(time.Hour)) || !facts[0].LastAt.Equal(until.Add(-30*time.Minute)) {
		t.Fatalf("first/last=%s/%s want %s/%s", facts[0].FirstAt, facts[0].LastAt, from.Add(time.Hour), until.Add(-30*time.Minute))
	}
	if facts[0].FirstAt.Location() != time.UTC || facts[0].LastAt.Location() != time.UTC {
		t.Fatalf("fact timestamps must be UTC: %+v", facts[0])
	}
	if facts[1].GameID != "nethack" || facts[1].SessionID != "session-2" || facts[1].TurnCount != 1 {
		t.Fatalf("unexpected second fact: %+v", facts[1])
	}
	if facts[2].GameID != "nethack" || facts[2].SessionID != "session-many-0" {
		t.Fatalf("facts must be deterministically sorted: %+v", facts)
	}
	if facts[8].GameID != "survival_garden" || facts[8].SessionID != "session-1" {
		t.Fatalf("same session ID in a different game must remain distinct: %+v", facts[8])
	}
	last := facts[len(facts)-1]
	if last.GameID != "territory_commander" || last.SessionID != "tc-session" || last.TurnCount != 2 {
		t.Fatalf("cross-UTC-day turns must remain one session fact: %+v", last)
	}
	jsonFacts, err := json.Marshal(facts[0])
	if err != nil {
		t.Fatalf("marshal fact: %v", err)
	}
	for _, forbidden := range []string{"decision", "result", "executed_actions", "persona", "memory_refs", "reason"} {
		if strings.Contains(string(jsonFacts), forbidden) {
			t.Fatalf("public fact leaked %q: %s", forbidden, jsonFacts)
		}
	}
	akaFacts, err := NewGameBridgeStore(path).PlayedGameSessionFacts(context.Background(), "aka", from, until)
	if err != nil {
		t.Fatalf("canonical Aka query returned error: %v", err)
	}
	if len(akaFacts) != 1 || akaFacts[0].SessionID != "aka-session" {
		t.Fatalf("canonical Aka must receive only Aka's exact events: %+v", akaFacts)
	}
}

func TestGameBridgeStorePlayedGameSessionFactsHandlesEmptyMissingAndInvalidQueries(t *testing.T) {
	from := time.Date(2026, time.October, 4, 15, 0, 0, 0, time.UTC)
	until := from.Add(24 * time.Hour)
	emptyPath := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(emptyPath, nil, 0644); err != nil {
		t.Fatalf("write empty store: %v", err)
	}
	facts, err := NewGameBridgeStore(emptyPath).PlayedGameSessionFacts(context.Background(), "mio", from, until)
	if err != nil || len(facts) != 0 {
		t.Fatalf("empty store facts=%+v err=%v, want empty success", facts, err)
	}
	missing := NewGameBridgeStore(filepath.Join(t.TempDir(), "missing.jsonl"))
	_, err = missing.PlayedGameSessionFacts(context.Background(), "mio", from, until)
	if !errors.Is(err, ErrGameBridgeStoreUnavailable) {
		t.Fatalf("missing store err=%v want ErrGameBridgeStoreUnavailable", err)
	}
	for _, query := range []struct {
		persona string
		from    time.Time
		until   time.Time
	}{
		{persona: "Mio", from: from, until: until},
		{persona: "coder1", from: from, until: until},
		{persona: "mio ", from: from, until: until},
		{persona: "mio", from: until, until: from},
		{persona: "mio", from: from, until: from},
	} {
		if _, err := NewGameBridgeStore(emptyPath).PlayedGameSessionFacts(context.Background(), query.persona, query.from, query.until); err == nil {
			t.Errorf("query persona=%q from=%s until=%s should be rejected", query.persona, query.from, query.until)
		}
	}
}
