package characterruntime

import (
	"context"
	"fmt"
	"strings"
	"time"
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
	CharacterID string    `json:"character_id"`
	Name        string    `json:"name"`
	Role        string    `json:"role"`
	Content     string    `json:"content"`
	CreatedAt   time.Time `json:"created_at"`
}

type RunResult struct {
	SessionID    string      `json:"session_id"`
	Mode         string      `json:"mode"`
	Participants []Character `json:"participants"`
	Turns        []Turn      `json:"turns"`
	CreatedAt    time.Time   `json:"created_at"`
}

type Service struct {
	now func() time.Time
}

func NewService() *Service {
	return &Service{now: func() time.Time { return time.Now().UTC() }}
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
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = "char_runtime_" + now.Format("20060102150405.000000000")
	}
	turns := make([]Turn, 0, maxTurns)
	for i, character := range participants[:maxTurns] {
		turns = append(turns, Turn{
			TurnIndex:   i + 1,
			CharacterID: character.ID,
			Name:        character.Name,
			Role:        character.Role,
			Content:     buildTurnContent(character, userMessage),
			CreatedAt:   now,
		})
	}
	return RunResult{
		SessionID:    sessionID,
		Mode:         "six_character_round",
		Participants: participants,
		Turns:        turns,
		CreatedAt:    now,
	}, nil
}

func DefaultCharacters() []Character {
	return []Character{
		{ID: "mio", Name: "Mio", Role: "chat_facilitator", Alias: "進行と返答整理", Enabled: true},
		{ID: "shiro", Name: "Shiro", Role: "coder_executor", Alias: "実装と検証", Enabled: true},
		{ID: "ao", Name: "AO", Role: "researcher", Alias: "調査と根拠", Enabled: true},
		{ID: "aka", Name: "Aka", Role: "risk_reviewer", Alias: "リスクと反証", Enabled: true},
		{ID: "kin", Name: "Kin", Role: "product_judge", Alias: "価値と優先度", Enabled: true},
		{ID: "gin", Name: "Gin", Role: "operator", Alias: "運用と監視", Enabled: true},
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
