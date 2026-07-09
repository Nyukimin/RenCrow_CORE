package viewer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrGameBridgeStoreUnavailable = errors.New("game bridge candidate log unavailable")

type GameBridgeEvent struct {
	EventID           string            `json:"event_id"`
	CandidateMemoryID string            `json:"candidate_memory_id"`
	GameID            string            `json:"game_id"`
	SessionID         string            `json:"session_id"`
	Turn              int               `json:"turn"`
	Persona           string            `json:"persona"`
	Decision          GameBrainDecision `json:"decision"`
	ExecutedActions   []string          `json:"executed_actions"`
	Result            map[string]any    `json:"result"`
	MemoryState       string            `json:"memory_state"`
	Promoted          bool              `json:"promoted"`
	CreatedAt         string            `json:"created_at"`
}

type GameBridgeSessionSummary struct {
	GameID         string `json:"game_id"`
	SessionID      string `json:"session_id"`
	Persona        string `json:"persona"`
	Status         string `json:"status"`
	LatestTurn     int    `json:"latest_turn"`
	LatestEventID  string `json:"latest_event_id"`
	CandidateCount int    `json:"candidate_count"`
	UpdatedAt      string `json:"updated_at"`
	DecisionMode   string `json:"decision_mode,omitempty"`
	ResultMode     string `json:"result_mode,omitempty"`
	MemoryMode     string `json:"memory_mode,omitempty"`
}

type GameBridgeEventView struct {
	EventID           string   `json:"event_id"`
	CandidateMemoryID string   `json:"candidate_memory_id"`
	GameID            string   `json:"game_id"`
	SessionID         string   `json:"session_id"`
	Turn              int      `json:"turn"`
	Persona           string   `json:"persona"`
	DecisionIntent    string   `json:"decision_intent"`
	MemoryRefs        []string `json:"memory_refs,omitempty"`
	ExecutedActions   []string `json:"executed_actions,omitempty"`
	ResultEvents      []string `json:"result_events,omitempty"`
	MemoryState       string   `json:"memory_state"`
	Promoted          bool     `json:"promoted"`
	CreatedAt         string   `json:"created_at"`
}

type GameBridgeResultWriter interface {
	SaveGameBridgeResult(context.Context, GameResultRequest) (GameBridgeEvent, error)
}

type GameBridgeRecallReader interface {
	RecentGameBridgeEvents(context.Context, string, string, int) ([]GameBridgeEvent, error)
}

type GameBridgeStore struct {
	path string
	mu   sync.Mutex
}

func NewGameBridgeStore(path string) *GameBridgeStore {
	return &GameBridgeStore{path: strings.TrimSpace(path)}
}

func (s *GameBridgeStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *GameBridgeStore) SaveGameBridgeResult(_ context.Context, req GameResultRequest) (GameBridgeEvent, error) {
	if s == nil || s.path == "" {
		return GameBridgeEvent{}, fmt.Errorf("game bridge store path is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	eventID := gameBridgeEventID(req.GameID, req.SessionID, req.Turn)
	if existing, ok, err := s.findByEventIDLocked(eventID); err != nil {
		return GameBridgeEvent{}, err
	} else if ok {
		return existing, nil
	}

	event := GameBridgeEvent{
		EventID:           eventID,
		CandidateMemoryID: eventID + ":candidate",
		GameID:            strings.TrimSpace(req.GameID),
		SessionID:         strings.TrimSpace(req.SessionID),
		Turn:              req.Turn,
		Persona:           strings.TrimSpace(req.Persona),
		Decision:          req.Decision,
		ExecutedActions:   append([]string(nil), req.ExecutedActions...),
		Result:            cloneGameBridgeResult(req.Result),
		MemoryState:       "candidate",
		Promoted:          false,
		CreatedAt:         time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := s.appendLocked(event); err != nil {
		return GameBridgeEvent{}, err
	}
	return event, nil
}

func (s *GameBridgeStore) RecentGameBridgeEvents(_ context.Context, gameID string, sessionID string, limit int) ([]GameBridgeEvent, error) {
	if s == nil || s.path == "" {
		return []GameBridgeEvent{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	events, err := s.readAllLocked()
	if err != nil {
		return nil, err
	}
	gameID = strings.TrimSpace(gameID)
	sessionID = strings.TrimSpace(sessionID)
	filtered := make([]GameBridgeEvent, 0)
	for _, event := range events {
		if strings.EqualFold(event.GameID, gameID) && event.SessionID == sessionID {
			filtered = append(filtered, event)
		}
	}
	for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
		filtered[i], filtered[j] = filtered[j], filtered[i]
	}
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func (s *GameBridgeStore) RecentGameBridgeSessions(_ context.Context, limit int) ([]GameBridgeSessionSummary, int, error) {
	if s == nil || s.path == "" {
		return nil, 0, ErrGameBridgeStoreUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	events, skipped, err := s.readAllWithSkippedLocked(false)
	if err != nil {
		return nil, skipped, err
	}
	type aggregate struct {
		summary GameBridgeSessionSummary
		latest  GameBridgeEvent
	}
	aggregates := make(map[string]aggregate)
	for _, event := range events {
		key := event.GameID + "\x00" + event.SessionID
		current, ok := aggregates[key]
		if !ok || gameBridgeEventNewer(event, current.latest) {
			current.latest = event
			current.summary.GameID = event.GameID
			current.summary.SessionID = event.SessionID
			current.summary.Persona = event.Persona
			current.summary.Status = "recent"
			current.summary.LatestTurn = event.Turn
			current.summary.LatestEventID = event.EventID
			current.summary.UpdatedAt = event.CreatedAt
			current.summary.MemoryMode = "candidate_only"
			current.summary.ResultMode = "persisted_candidate"
		}
		current.summary.CandidateCount++
		aggregates[key] = current
	}
	summaries := make([]GameBridgeSessionSummary, 0, len(aggregates))
	for _, item := range aggregates {
		summaries = append(summaries, item.summary)
	}
	sort.SliceStable(summaries, func(i, j int) bool {
		if summaries[i].UpdatedAt == summaries[j].UpdatedAt {
			return summaries[i].SessionID < summaries[j].SessionID
		}
		return summaries[i].UpdatedAt > summaries[j].UpdatedAt
	})
	if limit > 0 && len(summaries) > limit {
		summaries = summaries[:limit]
	}
	return summaries, skipped, nil
}

func (s *GameBridgeStore) RecentGameBridgeEventViews(_ context.Context, gameID string, sessionID string, limit int) ([]GameBridgeEventView, int, error) {
	if s == nil || s.path == "" {
		return nil, 0, ErrGameBridgeStoreUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	events, skipped, err := s.readAllWithSkippedLocked(false)
	if err != nil {
		return nil, skipped, err
	}
	gameID = strings.TrimSpace(gameID)
	sessionID = strings.TrimSpace(sessionID)
	filtered := make([]GameBridgeEvent, 0, len(events))
	for _, event := range events {
		if gameID != "" && !strings.EqualFold(event.GameID, gameID) {
			continue
		}
		if sessionID != "" && event.SessionID != sessionID {
			continue
		}
		filtered = append(filtered, event)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].CreatedAt == filtered[j].CreatedAt {
			return filtered[i].Turn > filtered[j].Turn
		}
		return filtered[i].CreatedAt > filtered[j].CreatedAt
	})
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	views := make([]GameBridgeEventView, 0, len(filtered))
	for _, event := range filtered {
		views = append(views, gameBridgeEventView(event))
	}
	return views, skipped, nil
}

func (s *GameBridgeStore) findByEventIDLocked(eventID string) (GameBridgeEvent, bool, error) {
	events, err := s.readAllLocked()
	if err != nil {
		return GameBridgeEvent{}, false, err
	}
	for _, event := range events {
		if event.EventID == eventID {
			return event, true, nil
		}
	}
	return GameBridgeEvent{}, false, nil
}

func (s *GameBridgeStore) readAllLocked() ([]GameBridgeEvent, error) {
	events, _, err := s.readAllWithSkippedLocked(true)
	return events, err
}

func (s *GameBridgeStore) readAllWithSkippedLocked(missingAsEmpty bool) ([]GameBridgeEvent, int, error) {
	file, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			if missingAsEmpty {
				return []GameBridgeEvent{}, 0, nil
			}
			return nil, 0, ErrGameBridgeStoreUnavailable
		}
		return nil, 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	events := make([]GameBridgeEvent, 0)
	skipped := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event GameBridgeEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			skipped++
			continue
		}
		if event.EventID == "" {
			skipped++
			continue
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, skipped, err
	}
	return events, skipped, nil
}

func (s *GameBridgeStore) appendLocked(event GameBridgeEvent) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return nil
}

func cloneGameBridgeResult(result map[string]any) map[string]any {
	if result == nil {
		return nil
	}
	cloned := make(map[string]any, len(result))
	for key, value := range result {
		cloned[key] = value
	}
	return cloned
}

func gameBridgeEventNewer(candidate GameBridgeEvent, current GameBridgeEvent) bool {
	if candidate.CreatedAt != current.CreatedAt {
		return candidate.CreatedAt > current.CreatedAt
	}
	return candidate.Turn > current.Turn
}

func gameBridgeEventView(event GameBridgeEvent) GameBridgeEventView {
	return GameBridgeEventView{
		EventID:           event.EventID,
		CandidateMemoryID: event.CandidateMemoryID,
		GameID:            event.GameID,
		SessionID:         event.SessionID,
		Turn:              event.Turn,
		Persona:           event.Persona,
		DecisionIntent:    event.Decision.Intent,
		MemoryRefs:        append([]string(nil), event.Decision.MemoryRefs...),
		ExecutedActions:   append([]string(nil), event.ExecutedActions...),
		ResultEvents:      gameBridgeResultEvents(event.Result),
		MemoryState:       event.MemoryState,
		Promoted:          event.Promoted,
		CreatedAt:         event.CreatedAt,
	}
}

func gameBridgeResultEvents(result map[string]any) []string {
	if result == nil {
		return nil
	}
	if events, ok := result["events"].([]string); ok {
		return append([]string(nil), events...)
	}
	if values, ok := result["events"].([]any); ok {
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
		}
		return out
	}
	if event, ok := result["event"].(string); ok && strings.TrimSpace(event) != "" {
		return []string{event}
	}
	return nil
}
