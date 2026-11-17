package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
)

func TestGameObserverSessionsHTTPClientReadsActiveAgentSessions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/games/sessions" || r.URL.RawQuery != "" {
			t.Errorf("GAMES request = %s %s, want GET /games/sessions", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"ok":true,"source":"local_observer","sessions":[],"active_agent_sessions":[{"game_id":"nethack","session_id":"session-mio","personas":["mio"],"status":"running"}]}`)
	}))
	defer server.Close()

	client, err := newGameObserverSessionsHTTPClient(server.URL)
	if err != nil {
		t.Fatalf("create GAMES sessions client: %v", err)
	}
	sessions, err := client.ActiveAgentSessions(context.Background())
	if err != nil {
		t.Fatalf("read GAMES sessions: %v", err)
	}
	want := []gameObserverActiveAgentSession{{GameID: "nethack", SessionID: "session-mio", Personas: []string{"mio"}, Status: "running"}}
	if len(sessions) != len(want) || sessions[0].GameID != want[0].GameID || sessions[0].SessionID != want[0].SessionID ||
		len(sessions[0].Personas) != 1 || sessions[0].Personas[0] != want[0].Personas[0] || sessions[0].Status != want[0].Status {
		t.Fatalf("active sessions = %+v, want %+v", sessions, want)
	}
}

func TestAgentRoutineRuntimeConnectsConfiguredGameSessionsReader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	cfg := &config.Config{
		WorkspaceDir: t.TempDir(),
		Heartbeat:    config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{Enabled: true}},
		Games:        config.GamesConfig{ObserverURL: server.URL},
	}
	deps := &Dependencies{taskManager: taskmanager.New(nil, taskmanager.DefaultParallelLimits())}
	runtime, err := newAgentRoutineRuntime(cfg, deps, nil, nil)
	if err != nil {
		t.Fatalf("create agent routine runtime: %v", err)
	}
	if runtime == nil || runtime.gameSessions == nil {
		t.Fatal("configured GAMES observer was not connected to agent routine seats")
	}
	if err := runtime.store.Close(); err != nil {
		t.Errorf("close routine fixture: %v", err)
	}
}

func TestGameObserverSessionsHTTPClientDoesNotPromoteLegacyOrSimulationSessions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok":true,"source":"local_observer","sessions":[{"game_id":"nethack","session_id":"rule-sim","persona":"mio","status":"running"}],"active_agent_sessions":[]}`)
	}))
	defer server.Close()
	client, err := newGameObserverSessionsHTTPClient(server.URL)
	if err != nil {
		t.Fatalf("create GAMES sessions client: %v", err)
	}
	sessions, err := client.ActiveAgentSessions(context.Background())
	if err != nil {
		t.Fatalf("read empty active-agent sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("legacy sessions were promoted to active Agent sessions: %+v", sessions)
	}
}

func TestGameObserverSessionsHTTPClientRejectsUnavailableOrInvalidSnapshots(t *testing.T) {
	valid := `{"ok":true,"source":"local_observer","sessions":[],"active_agent_sessions":[]}`
	cases := []struct {
		name       string
		statusCode int
		body       string
	}{
		{name: "http error", statusCode: http.StatusBadGateway, body: valid},
		{name: "missing active field", statusCode: http.StatusOK, body: `{"ok":true,"source":"local_observer","sessions":[]}`},
		{name: "invalid json", statusCode: http.StatusOK, body: `{"ok":`},
		{name: "wrong source", statusCode: http.StatusOK, body: `{"ok":true,"source":"candidate_log","sessions":[],"active_agent_sessions":[]}`},
		{name: "terminal in active list", statusCode: http.StatusOK, body: `{"ok":true,"source":"local_observer","sessions":[],"active_agent_sessions":[{"game_id":"nethack","session_id":"s1","personas":["mio"],"status":"completed"}]}`},
		{name: "unknown game", statusCode: http.StatusOK, body: `{"ok":true,"source":"local_observer","sessions":[],"active_agent_sessions":[{"game_id":"unknown","session_id":"s1","personas":["mio"],"status":"running"}]}`},
		{name: "unknown persona", statusCode: http.StatusOK, body: `{"ok":true,"source":"local_observer","sessions":[],"active_agent_sessions":[{"game_id":"nethack","session_id":"s1","personas":["local"],"status":"running"}]}`},
		{name: "empty personas", statusCode: http.StatusOK, body: `{"ok":true,"source":"local_observer","sessions":[],"active_agent_sessions":[{"game_id":"nethack","session_id":"s1","personas":[],"status":"running"}]}`},
		{name: "duplicate session", statusCode: http.StatusOK, body: `{"ok":true,"source":"local_observer","sessions":[],"active_agent_sessions":[{"game_id":"nethack","session_id":"s1","personas":["mio"],"status":"running"},{"game_id":"nethack","session_id":"s1","personas":["mio"],"status":"running"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.statusCode)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client, err := newGameObserverSessionsHTTPClient(server.URL)
			if err != nil {
				t.Fatalf("create GAMES sessions client: %v", err)
			}
			if sessions, err := client.ActiveAgentSessions(context.Background()); !errors.Is(err, errGameObserverSessionsUnavailable) || sessions != nil {
				t.Fatalf("ActiveAgentSessions() = (%+v, %v), want unavailable", sessions, err)
			}
		})
	}
}

func TestGameObserverSessionsHTTPClientAllowsPersonaAcrossActiveSessions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"ok":true,"source":"local_observer","sessions":[],"active_agent_sessions":[{"game_id":"nethack","session_id":"game-one","personas":["mio"],"status":"running"},{"game_id":"nethack","session_id":"game-two","personas":["mio"],"status":"running"}]}`)
	}))
	defer server.Close()
	client, err := newGameObserverSessionsHTTPClient(server.URL)
	if err != nil {
		t.Fatalf("create GAMES sessions client: %v", err)
	}
	sessions, err := client.ActiveAgentSessions(context.Background())
	if err != nil {
		t.Fatalf("read valid concurrent sessions for mio: %v", err)
	}
	if len(sessions) != 2 || sessions[0].Personas[0] != "mio" || sessions[1].Personas[0] != "mio" {
		t.Fatalf("active sessions = %+v, want both sessions retained for mio", sessions)
	}
}
