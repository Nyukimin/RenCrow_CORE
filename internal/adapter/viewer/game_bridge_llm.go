package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

const defaultGameDecisionTimeout = 25 * time.Second

// LLMGameDecisionGenerator builds the Phase 2 game decision prompt and parses
// the model response as strict BrainDecision JSON.
type LLMGameDecisionGenerator struct {
	Provider llm.LLMProvider
	Timeout  time.Duration
}

func NewLLMGameDecisionGenerator(provider llm.LLMProvider) *LLMGameDecisionGenerator {
	if provider == nil {
		return nil
	}
	return &LLMGameDecisionGenerator{
		Provider: provider,
		Timeout:  defaultGameDecisionTimeout,
	}
}

func (g *LLMGameDecisionGenerator) GenerateGameDecision(r *http.Request, req GameObservationRequest, recentEvents []GameBridgeEvent) (GameBrainDecision, error) {
	if g == nil || g.Provider == nil {
		return GameBrainDecision{}, GameDecisionGenerationError{StatusCode: http.StatusServiceUnavailable, Err: fmt.Errorf("game decision provider is unavailable")}
	}
	ctx := r.Context()
	if g.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.Timeout)
		defer cancel()
	}
	userPrompt, err := buildGameDecisionUserPrompt(req, recentEvents)
	if err != nil {
		return GameBrainDecision{}, GameDecisionGenerationError{StatusCode: http.StatusInternalServerError, Err: err}
	}
	resp, err := g.Provider.Generate(ctx, llm.GenerateRequest{
		SystemPrompt: gameDecisionSystemPrompt(req.Persona),
		Messages: []llm.Message{
			{Role: "user", Content: userPrompt},
		},
		MaxTokens:   700,
		Temperature: 0.2,
		ProviderOptions: map[string]any{
			"surface": "game_bridge",
			"persona": strings.TrimSpace(req.Persona),
			"game_id": strings.TrimSpace(req.GameID),
		},
	})
	if err != nil {
		return GameBrainDecision{}, GameDecisionGenerationError{StatusCode: http.StatusServiceUnavailable, Err: fmt.Errorf("game decision provider failed: %w", err)}
	}
	decision, err := decodeStrictGameBrainDecision(resp.Content)
	if err != nil {
		return GameBrainDecision{}, GameDecisionGenerationError{StatusCode: http.StatusBadGateway, Err: err}
	}
	return decision, nil
}

func gameDecisionSystemPrompt(persona string) string {
	persona = strings.TrimSpace(persona)
	if persona == "" {
		persona = "mio"
	}
	return fmt.Sprintf(`You are %s, a RenCrow game persona making a high-level game decision through RenCrow.
Use the recall section as candidate context only; do not claim it is confirmed memory.
RenCrow_GAMES owns game physics and action validity. Choose only from the exact available_actions list.
Return only one strict JSON object matching BrainDecision:
{"persona":"%s","intent":"<available action>","reason":"<brief reason>","action_plan":[{"action":"<available action>","target":"optional","args":{}}],"memory_refs":[],"confidence":0.0}
Do not wrap the JSON in markdown. Do not include extra keys.`, persona, persona)
}

func buildGameDecisionUserPrompt(req GameObservationRequest, recentEvents []GameBridgeEvent) (string, error) {
	payload := map[string]any{
		"game_id":           strings.TrimSpace(req.GameID),
		"session_id":        strings.TrimSpace(req.SessionID),
		"turn":              req.Turn,
		"persona":           strings.TrimSpace(req.Persona),
		"observation":       req.Observation,
		"available_actions": req.AvailableActions,
		"request":           strings.TrimSpace(req.Request),
		"recall": map[string]any{
			"recent_candidate_events": summarizeGameBridgeEvents(recentEvents),
		},
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal game decision prompt: %w", err)
	}
	return "Game decision input:\n" + string(encoded), nil
}

func summarizeGameBridgeEvents(events []GameBridgeEvent) []map[string]any {
	if len(events) == 0 {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(events))
	for _, event := range events {
		out = append(out, map[string]any{
			"event_id":            event.EventID,
			"candidate_memory_id": event.CandidateMemoryID,
			"turn":                event.Turn,
			"persona":             event.Persona,
			"decision_intent":     event.Decision.Intent,
			"executed_actions":    event.ExecutedActions,
			"result":              event.Result,
			"memory_state":        event.MemoryState,
			"promoted":            event.Promoted,
		})
	}
	return out
}

func decodeStrictGameBrainDecision(content string) (GameBrainDecision, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return GameBrainDecision{}, fmt.Errorf("empty game decision response")
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var decision GameBrainDecision
	if err := dec.Decode(&decision); err != nil {
		return GameBrainDecision{}, fmt.Errorf("decode game decision json: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return GameBrainDecision{}, fmt.Errorf("game decision response contains trailing JSON")
		}
		return GameBrainDecision{}, fmt.Errorf("decode game decision trailing content: %w", err)
	}
	if decision.MemoryRefs == nil {
		decision.MemoryRefs = []string{}
	}
	return decision, nil
}
