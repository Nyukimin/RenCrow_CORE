package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type recordingResultWriter struct {
	saved []GameResultRequest
	fail  bool
}

func (w *recordingResultWriter) SaveGameBridgeResult(_ context.Context, req GameResultRequest) (GameBridgeEvent, error) {
	if w.fail {
		return GameBridgeEvent{}, context.Canceled
	}
	w.saved = append(w.saved, req)
	return GameBridgeEvent{EventID: "test"}, nil
}

func postGameLaunch(t *testing.T, handler http.HandlerFunc, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/viewer/games/launch", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestHandleGameLaunchForwardsAndRecordsMotive(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/games/launch" || r.Method != http.MethodPost {
			t.Errorf("unexpected upstream call: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&upstreamBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "game_id": "herzog_zwei", "session_id": "hz_test_1", "status": "launching",
		})
	}))
	defer upstream.Close()
	store := &recordingResultWriter{}
	handler := HandleGameLaunch(GameLaunchOptions{ObserverBaseURL: upstream.URL, Store: store})

	response := postGameLaunch(t, handler, map[string]any{
		"game_id":  "herzog_zwei",
		"personas": []string{"mio", "kuro"},
		"reason":   "雪辱戦",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if decoded["session_id"] != "hz_test_1" || decoded["motive_recorded"] != true {
		t.Fatalf("unexpected response: %v", decoded)
	}
	personas, _ := upstreamBody["personas"].([]any)
	if len(personas) != 2 {
		t.Fatalf("personas must be forwarded: %v", upstreamBody)
	}
	if len(store.saved) != 1 || store.saved[0].Turn != -1 || store.saved[0].Decision.Intent != "play_game" {
		t.Fatalf("motive must be recorded as turn -1 play_game: %+v", store.saved)
	}
	if store.saved[0].SessionID != "hz_test_1" {
		t.Fatalf("motive must reference launched session: %+v", store.saved[0])
	}
}

func TestHandleGameLaunchWithoutReasonSkipsMotive(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "game_id": "nethack", "session_id": "nh_1", "status": "launching"})
	}))
	defer upstream.Close()
	store := &recordingResultWriter{}
	handler := HandleGameLaunch(GameLaunchOptions{ObserverBaseURL: upstream.URL, Store: store})

	response := postGameLaunch(t, handler, map[string]any{"game_id": "nethack", "personas": []string{"mio"}})
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	if len(store.saved) != 0 {
		t.Fatalf("no reason must record nothing: %+v", store.saved)
	}
	if !strings.Contains(response.Body.String(), `"motive_recorded":false`) {
		t.Fatalf("motive_recorded must be false: %s", response.Body.String())
	}
}

func TestHandleGameLaunchValidation(t *testing.T) {
	handler := HandleGameLaunch(GameLaunchOptions{ObserverBaseURL: "http://127.0.0.1:1"})
	cases := []map[string]any{
		{"game_id": "unknown_game"},
		{"game_id": "nethack", "personas": []string{"mio", "kuro"}},
		{"game_id": "territory_commander", "personas": []string{"a", "b", "c"}},
		{"game_id": "herzog_zwei", "personas": []string{"mio", "mio"}},
		{"game_id": "herzog_zwei", "personas": []string{" "}},
	}
	for i, body := range cases {
		if response := postGameLaunch(t, handler, body); response.Code != http.StatusBadRequest {
			t.Fatalf("case %d: status=%d want 400 (%v)", i, response.Code, body)
		}
	}
}

func TestHandleGameLaunchUpstreamFailurePassesThrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "observer store session capacity reached", http.StatusTooManyRequests)
	}))
	defer upstream.Close()
	handler := HandleGameLaunch(GameLaunchOptions{ObserverBaseURL: upstream.URL})
	response := postGameLaunch(t, handler, map[string]any{"game_id": "herzog_zwei", "personas": []string{"mio"}})
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d want 429", response.Code)
	}
}

func TestHandleGameLaunchUpstreamUnavailable(t *testing.T) {
	handler := HandleGameLaunch(GameLaunchOptions{ObserverBaseURL: "http://127.0.0.1:1"})
	response := postGameLaunch(t, handler, map[string]any{"game_id": "herzog_zwei", "personas": []string{"mio"}})
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", response.Code)
	}
}

func TestHandleGameLaunchRejectsGet(t *testing.T) {
	handler := HandleGameLaunch(GameLaunchOptions{})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/viewer/games/launch", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", recorder.Code)
	}
}
