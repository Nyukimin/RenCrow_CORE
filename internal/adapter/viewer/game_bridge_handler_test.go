package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestHandleGameBridgeStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/games/status", nil)

	HandleGameBridgeStatus(GameBridgeStatusOptions{
		ConversationEngineEnabled: true,
		L1StoreEnabled:            true,
	}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["conversation_engine_enabled"] != true || got["l1_store_enabled"] != true {
		t.Fatalf("runtime flags not reflected: %+v", got)
	}
}

func TestGameActionStepUsesCommonArgsKey(t *testing.T) {
	payload, err := json.Marshal(GameActionStep{
		Action: "move",
		Target: "river",
		Args:   map[string]any{"pace": "safe"},
	})
	if err != nil {
		t.Fatalf("marshal action step: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("decode action step: %v", err)
	}
	if _, ok := got["args"]; !ok {
		t.Fatalf("missing args key: %s", string(payload))
	}
	if _, ok := got["parameters"]; ok {
		t.Fatalf("unexpected parameters key: %s", string(payload))
	}
}

func TestHandleGameBridgeResultAcceptsCandidateResult(t *testing.T) {
	store := NewGameBridgeStore(filepath.Join(t.TempDir(), "game_bridge_events.jsonl"))
	body := map[string]any{
		"game_id":          "survival_garden",
		"session_id":       "sg_test",
		"turn":             2,
		"persona":          "mio",
		"executed_actions": []string{"drink", "return_to_camp"},
		"result": map[string]any{
			"success": true,
			"event":   "returned_before_rain",
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/viewer/games/result", bytes.NewReader(payload))

	HandleGameBridgeResult(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["memory_state"] != "candidate" {
		t.Fatalf("memory_state=%v", got["memory_state"])
	}
	if got["event_id"] == "" {
		t.Fatalf("event_id is empty")
	}
	events, err := store.RecentGameBridgeEvents(context.Background(), "survival_garden", "sg_test", 10)
	if err != nil {
		t.Fatalf("RecentGameBridgeEvents returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%d want 1", len(events))
	}
	if events[0].MemoryState != "candidate" || events[0].Promoted {
		t.Fatalf("unexpected persisted event: %+v", events[0])
	}
}

func TestHandleGameBridgeSessionsReturnsCandidateLogSummaries(t *testing.T) {
	store := NewGameBridgeStore(filepath.Join(t.TempDir(), "game_bridge_events.jsonl"))
	_, err := store.SaveGameBridgeResult(context.Background(), GameResultRequest{
		GameID:          "survival_garden",
		SessionID:       "sg_test",
		Turn:            4,
		Persona:         "mio",
		Decision:        GameBrainDecision{Intent: "rest"},
		ExecutedActions: []string{"rest"},
		Result:          map[string]any{"success": true, "event": "rested"},
	})
	if err != nil {
		t.Fatalf("SaveGameBridgeResult returned error: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/games/sessions?limit=5", nil)
	HandleGameBridgeSessions(store, GameBridgeStatusOptions{
		ResultMode: "persisted_candidate",
		MemoryMode: "candidate_only",
	}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got struct {
		OK       bool                       `json:"ok"`
		Sessions []GameBridgeSessionSummary `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].LatestTurn != 4 {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestHandleGameBridgeEventsFiltersCandidateEvents(t *testing.T) {
	store := NewGameBridgeStore(filepath.Join(t.TempDir(), "game_bridge_events.jsonl"))
	for _, req := range []GameResultRequest{
		{GameID: "survival_garden", SessionID: "sg_test", Turn: 1, Persona: "mio", Decision: GameBrainDecision{Intent: "drink"}, ExecutedActions: []string{"drink"}, Result: map[string]any{"success": true, "events": []string{"drank_water"}}},
		{GameID: "territory_commander", SessionID: "tc_test", Turn: 1, Persona: "mio", Decision: GameBrainDecision{Intent: "defend"}, ExecutedActions: []string{"defend"}, Result: map[string]any{"success": true, "events": []string{"held_line"}}},
	} {
		if _, err := store.SaveGameBridgeResult(context.Background(), req); err != nil {
			t.Fatalf("SaveGameBridgeResult returned error: %v", err)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/games/events?game_id=survival_garden&session_id=sg_test&limit=10", nil)
	HandleGameBridgeEvents(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got struct {
		Events []GameBridgeEventView `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Events) != 1 || got.Events[0].GameID != "survival_garden" || got.Events[0].DecisionIntent != "drink" {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestHandleGameBridgeObserverMissingLogAndInvalidLimit(t *testing.T) {
	store := NewGameBridgeStore(filepath.Join(t.TempDir(), "missing.jsonl"))

	missingRec := httptest.NewRecorder()
	missingReq := httptest.NewRequest(http.MethodGet, "/viewer/games/sessions", nil)
	HandleGameBridgeSessions(store, GameBridgeStatusOptions{}).ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing status=%d want=%d body=%s", missingRec.Code, http.StatusServiceUnavailable, missingRec.Body.String())
	}

	limitRec := httptest.NewRecorder()
	limitReq := httptest.NewRequest(http.MethodGet, "/viewer/games/events?limit=bad", nil)
	HandleGameBridgeEvents(store).ServeHTTP(limitRec, limitReq)
	if limitRec.Code != http.StatusBadRequest {
		t.Fatalf("limit status=%d want=%d body=%s", limitRec.Code, http.StatusBadRequest, limitRec.Body.String())
	}
}
