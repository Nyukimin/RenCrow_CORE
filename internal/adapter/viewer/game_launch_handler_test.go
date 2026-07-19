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
	// B-3: 動機は参加ペルソナ全員に記録される（言い出しっぺ + 誘われた側）。
	if len(store.saved) != 2 {
		t.Fatalf("motive must be recorded for all personas: %+v", store.saved)
	}
	if store.saved[0].Turn != -1 || store.saved[0].Persona != "mio" || store.saved[0].Decision.Intent != "play_game" {
		t.Fatalf("initiator motive must be turn -1 play_game: %+v", store.saved[0])
	}
	if store.saved[1].Turn != -2 || store.saved[1].Persona != "kuro" || store.saved[1].Decision.Intent != "invited_to_play" {
		t.Fatalf("invitee motive must be turn -2 invited_to_play: %+v", store.saved[1])
	}
	if store.saved[1].Result["invited_by"] != "mio" {
		t.Fatalf("invitee must record invited_by: %+v", store.saved[1].Result)
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

func TestHandleGameLaunchRequiresGameID(t *testing.T) {
	handler := HandleGameLaunch(GameLaunchOptions{ObserverBaseURL: "http://127.0.0.1:1"})
	if response := postGameLaunch(t, handler, map[string]any{"personas": []string{"mio"}}); response.Code != http.StatusBadRequest {
		t.Fatalf("missing game_id must be 400: %d", response.Code)
	}
}

// B-2: タイトル・人数の capability 検証は observer が正本。CORE は
// 二重管理せず、observer の 400 をそのまま透過する。
func TestHandleGameLaunchCapabilityValidationPassesThroughObserver(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `game "nethack" supports 1-1 personas, got 2`, http.StatusBadRequest)
	}))
	defer upstream.Close()
	handler := HandleGameLaunch(GameLaunchOptions{ObserverBaseURL: upstream.URL})
	response := postGameLaunch(t, handler, map[string]any{"game_id": "nethack", "personas": []string{"mio", "kuro"}})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("observer 400 must pass through: %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "supports 1-1 personas") {
		t.Fatalf("observer error message must pass through: %s", response.Body.String())
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
