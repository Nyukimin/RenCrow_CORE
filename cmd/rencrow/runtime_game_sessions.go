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
