package characterruntime

import (
	"context"
	"fmt"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type Character struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Role    string `json:"role"`
	Alias   string `json:"alias"`
	Enabled bool   `json:"enabled"`
}

type RunRequest struct {
	SessionID   string   `json:"session_id,omitempty"`
	UserMessage string   `json:"user_message"`
	Characters  []string `json:"characters,omitempty"`
	MaxTurns    int      `json:"max_turns,omitempty"`
	RequestedBy string   `json:"requested_by,omitempty"`
}

type Turn struct {
	TurnIndex   int       `json:"turn_index"`
	MessageID   string    `json:"message_id"`
	CharacterID string    `json:"character_id"`
	Name        string    `json:"name"`
	Role        string    `json:"role"`
	Content     string    `json:"content"`
	CreatedAt   time.Time `json:"created_at"`
}

type RunResult struct {
	SessionID     string      `json:"session_id"`
	TraceID       string      `json:"trace_id"`
	UserMessageID string      `json:"user_message_id"`
	Mode          string      `json:"mode"`
	Participants  []Character `json:"participants"`
	Turns         []Turn      `json:"turns"`
	CreatedAt     time.Time   `json:"created_at"`
}

type Service struct {
	now          func() time.Time
	newSessionID func() string
	newTraceID   func() string
	newMessageID func() string
}

func NewService() *Service {
	return &Service{
		now: func() time.Time { return time.Now().UTC() },
		newSessionID: func() string {
			return string(modulecore.NewSessionID())
		},
		newTraceID: func() string {
			return string(modulecore.NewTraceID())
		},
		newMessageID: func() string {
			return string(modulecore.NewMessageID())
		},
	}
}

func (s *Service) RunRound(_ context.Context, req RunRequest) (RunResult, error) {
	if s == nil {
		return RunResult{}, fmt.Errorf("character runtime unavailable")
	}
	userMessage := strings.TrimSpace(req.UserMessage)
	if userMessage == "" {
		return RunResult{}, fmt.Errorf("user_message is required")
	}
	participants, err := selectCharacters(req.Characters)
	if err != nil {
		return RunResult{}, err
	}
	if len(participants) == 0 {
		return RunResult{}, fmt.Errorf("at least one character is required")
	}
	maxTurns := req.MaxTurns
	if maxTurns <= 0 || maxTurns > len(participants) {
		maxTurns = len(participants)
	}
	now := s.now().UTC()
	traceID := s.newTraceID()
	userMessageID := s.newMessageID()
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = s.newSessionID()
	}
	if err := modulecore.SessionID(sessionID).Validate(); err != nil {
		return RunResult{}, fmt.Errorf("session_id is invalid: %w", err)
	}
	turns := make([]Turn, 0, maxTurns)
	for i, character := range participants[:maxTurns] {
		turns = append(turns, Turn{
			TurnIndex:   i + 1,
			MessageID:   s.newMessageID(),
			CharacterID: character.ID,
			Name:        character.Name,
			Role:        character.Role,
			Content:     buildTurnContent(character, userMessage),
			CreatedAt:   now,
		})
	}
	return RunResult{
		SessionID:     sessionID,
		TraceID:       traceID,
		UserMessageID: userMessageID,
		Mode:          "six_character_round",
		Participants:  participants,
		Turns:         turns,
		CreatedAt:     now,
	}, nil
}

func DefaultCharacters() []Character {
	return []Character{
		{ID: "mio", Name: "Mio", Role: "chat_facilitator", Alias: "進行と返答整理", Enabled: true},
		{ID: "shiro", Name: "Shiro", Role: "coder_executor", Alias: "実装と検証", Enabled: true},
		{ID: "aka", Name: "Aka", Role: "coder1_architecture", Alias: "設計と依存関係", Enabled: true},
		{ID: "ao", Name: "Ao", Role: "coder2_implementation", Alias: "実装と検証", Enabled: true},
		{ID: "kin", Name: "Kin", Role: "coder3_risk", Alias: "難実装と安全性", Enabled: true},
		{ID: "gin", Name: "Gin", Role: "coder4_finish", Alias: "比較と仕上げ", Enabled: true},
	}
}

func selectCharacters(ids []string) ([]Character, error) {
	all := DefaultCharacters()
	if len(ids) == 0 {
		return all, nil
	}
	byID := make(map[string]Character, len(all))
	for _, character := range all {
		byID[character.ID] = character
	}
	out := make([]Character, 0, len(ids))
	seen := map[string]struct{}{}
	for _, raw := range ids {
		id := strings.ToLower(strings.TrimSpace(raw))
		if id == "" {
			continue
		}
		character, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("unknown character: %s", raw)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, character)
	}
	return out, nil
}

func buildTurnContent(character Character, userMessage string) string {
	return fmt.Sprintf("%s viewpoint queued for %q: %s", character.Name, userMessage, character.Alias)
}
