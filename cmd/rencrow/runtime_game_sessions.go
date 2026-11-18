package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	gameObserverSessionsTimeout = 1500 * time.Millisecond
	gameObserverSessionsMaxBody = 1 << 20
)

var errGameObserverSessionsUnavailable = errors.New("GAMES active sessions are unavailable")

type gameObserverActiveAgentSession struct {
	GameID    string   `json:"game_id"`
	SessionID string   `json:"session_id"`
	Personas  []string `json:"personas"`
	Status    string   `json:"status"`
}

type gameObserverSessionSummary struct {
	GameID    string   `json:"game_id"`
	SessionID string   `json:"session_id"`
	Persona   string   `json:"persona"`
	Personas  []string `json:"personas"`
	Status    string   `json:"status"`
}

type gameObserverActiveSessionsReader interface {
	ActiveAgentSessions(context.Context) ([]gameObserverActiveAgentSession, error)
}

type gameObserverSessionsHTTPClient struct {
	baseURL    string
	httpClient *http.Client
}

func newGameObserverSessionsHTTPClient(observerBaseURL string) (*gameObserverSessionsHTTPClient, error) {
	baseURL, err := parseGameObserverSessionsBaseURL(observerBaseURL)
	if err != nil {
		return nil, err
	}
	return &gameObserverSessionsHTTPClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: gameObserverSessionsTimeout},
	}, nil
}

func parseGameObserverSessionsBaseURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("GAMES observer URL is invalid")
	}
	return raw, nil
}

func (c *gameObserverSessionsHTTPClient) ActiveAgentSessions(ctx context.Context) ([]gameObserverActiveAgentSession, error) {
	if c == nil || c.httpClient == nil || c.baseURL == "" {
		return nil, errGameObserverSessionsUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/games/sessions", nil)
	if err != nil {
		return nil, errGameObserverSessionsUnavailable
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: request failed", errGameObserverSessionsUnavailable)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP status %d", errGameObserverSessionsUnavailable, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, gameObserverSessionsMaxBody+1))
	if err != nil || len(body) > gameObserverSessionsMaxBody {
		return nil, fmt.Errorf("%w: response body unavailable", errGameObserverSessionsUnavailable)
	}
	var response struct {
		OK                  bool                             `json:"ok"`
		Source              string                           `json:"source"`
		Sessions            []json.RawMessage                `json:"sessions"`
		ActiveAgentSessions []gameObserverActiveAgentSession `json:"active_agent_sessions"`
	}
	if err := json.Unmarshal(body, &response); err != nil || !response.OK || response.Source != "local_observer" ||
		response.Sessions == nil || response.ActiveAgentSessions == nil {
		return nil, fmt.Errorf("%w: response is malformed or incomplete", errGameObserverSessionsUnavailable)
	}
	if err := validateGameObserverActiveAgentSessions(response.ActiveAgentSessions); err != nil {
		return nil, fmt.Errorf("%w: %v", errGameObserverSessionsUnavailable, err)
	}
	return response.ActiveAgentSessions, nil
}

func (c *gameObserverSessionsHTTPClient) SessionSummary(ctx context.Context, sessionID string) (gameObserverSessionSummary, error) {
	if c == nil || c.httpClient == nil || c.baseURL == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(sessionID) != sessionID {
		return gameObserverSessionSummary{}, errGameObserverSessionsUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/games/sessions/"+url.PathEscape(sessionID), nil)
	if err != nil {
		return gameObserverSessionSummary{}, errGameObserverSessionsUnavailable
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return gameObserverSessionSummary{}, fmt.Errorf("%w: source session request failed", errGameObserverSessionsUnavailable)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return gameObserverSessionSummary{}, fmt.Errorf("%w: source session HTTP status %d", errGameObserverSessionsUnavailable, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, gameObserverSessionsMaxBody+1))
	if err != nil || len(body) > gameObserverSessionsMaxBody {
		return gameObserverSessionSummary{}, fmt.Errorf("%w: source session body unavailable", errGameObserverSessionsUnavailable)
	}
	var response struct {
		OK      bool                       `json:"ok"`
		Session gameObserverSessionSummary `json:"session"`
	}
	if err := json.Unmarshal(body, &response); err != nil || !response.OK || response.Session.SessionID != sessionID {
		return gameObserverSessionSummary{}, fmt.Errorf("%w: source session response is malformed", errGameObserverSessionsUnavailable)
	}
	if err := validateGameObserverSessionSummary(response.Session); err != nil {
		return gameObserverSessionSummary{}, fmt.Errorf("%w: %v", errGameObserverSessionsUnavailable, err)
	}
	return response.Session, nil
}

func validateGameObserverSessionSummary(session gameObserverSessionSummary) error {
	knownGames := map[string]struct{}{
		"nethack": {}, "herzog_zwei": {}, "territory_commander": {}, "survival_garden": {},
	}
	if _, ok := knownGames[session.GameID]; !ok || session.SessionID == "" || strings.TrimSpace(session.SessionID) != session.SessionID {
		return errors.New("session identity is invalid")
	}
	if session.Status != "launching" && session.Status != "running" && session.Status != "completed" && session.Status != "failed" {
		return errors.New("session status is invalid")
	}
	personas := session.Personas
	if len(personas) == 0 && session.Persona != "" {
		personas = []string{session.Persona}
	}
	if len(personas) == 0 || len(personas) > len(agentHeartbeatActorIDs) {
		return errors.New("source session roster is unavailable")
	}
	seen := make(map[string]struct{}, len(personas))
	for _, actor := range personas {
		if actor == "" || strings.TrimSpace(actor) != actor || !isAgentHeartbeatActor(actor) {
			return errors.New("source session roster contains an unknown actor")
		}
		if _, duplicate := seen[actor]; duplicate {
			return errors.New("source session roster contains a duplicate actor")
		}
		seen[actor] = struct{}{}
	}
	if session.Persona != "" && len(session.Personas) > 0 && session.Persona != session.Personas[0] {
		return errors.New("source session persona does not match its ordered roster")
	}
	return nil
}

func validateGameObserverActiveAgentSessions(sessions []gameObserverActiveAgentSession) error {
	knownGames := map[string]struct{}{
		"nethack": {}, "herzog_zwei": {}, "territory_commander": {}, "survival_garden": {},
	}
	seenSessions := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		if session.SessionID == "" || strings.TrimSpace(session.SessionID) != session.SessionID {
			return errors.New("session id is invalid")
		}
		if _, duplicate := seenSessions[session.SessionID]; duplicate {
			return errors.New("session id is duplicated")
		}
		seenSessions[session.SessionID] = struct{}{}
		if _, known := knownGames[session.GameID]; !known {
			return errors.New("game id is unknown")
		}
		if session.Status != "launching" && session.Status != "running" {
			return errors.New("session status is not active")
		}
		if len(session.Personas) == 0 || len(session.Personas) > len(agentHeartbeatActorIDs) {
			return errors.New("session personas are incomplete")
		}
		seenPersonas := make(map[string]struct{}, len(session.Personas))
		for _, persona := range session.Personas {
			if persona == "" || strings.TrimSpace(persona) != persona || !isAgentHeartbeatActor(persona) {
				return errors.New("session persona is unknown")
			}
			if _, duplicate := seenPersonas[persona]; duplicate {
				return errors.New("session persona is duplicated")
			}
			seenPersonas[persona] = struct{}{}
		}
	}
	return nil
}
