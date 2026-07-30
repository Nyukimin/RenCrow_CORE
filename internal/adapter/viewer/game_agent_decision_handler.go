package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const gameAgentDecisionMaxBodyBytes int64 = 2 * 1024 * 1024

// GameAgentDecisionRequest is the observation sent by RenCrow_GAMES for one
// Agent-owned turn. The Agent decides; GAMES remains responsible for validating
// and executing the returned action.
type GameAgentDecisionRequest struct {
	GameID           string         `json:"game_id"`
	SessionID        string         `json:"session_id"`
	Turn             int            `json:"turn"`
	Persona          string         `json:"persona"`
	Observation      map[string]any `json:"observation"`
	AvailableActions []string       `json:"available_actions"`
	Request          string         `json:"request"`
}

type GameAgentDecisionProvider interface {
	DecideGameTurn(context.Context, GameAgentDecisionRequest) (GameBrainDecision, error)
}

func HandleGameAgentDecision(provider GameAgentDecisionProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if provider == nil {
			http.Error(w, "game Agent decision provider is unavailable", http.StatusServiceUnavailable)
			return
		}
		var request GameAgentDecisionRequest
		reader := http.MaxBytesReader(w, r.Body, gameAgentDecisionMaxBodyBytes)
		decoder := json.NewDecoder(reader)
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid Agent decision request", http.StatusBadRequest)
			return
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			http.Error(w, "Agent decision request must contain one JSON object", http.StatusBadRequest)
			return
		}
		if err := validateGameAgentDecisionRequest(request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		decision, err := provider.DecideGameTurn(r.Context(), request)
		if err != nil {
			http.Error(w, fmt.Sprintf("Agent decision failed: %v", err), http.StatusBadGateway)
			return
		}
		if err := validateGameAgentDecision(request, decision); err != nil {
			http.Error(w, fmt.Sprintf("Agent decision invalid: %v", err), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"decision": decision,
		})
	}
}

func validateGameAgentDecisionRequest(request GameAgentDecisionRequest) error {
	if strings.TrimSpace(request.GameID) == "" {
		return fmt.Errorf("game_id is required")
	}
	if strings.TrimSpace(request.SessionID) == "" {
		return fmt.Errorf("session_id is required")
	}
	if request.Turn < 0 {
		return fmt.Errorf("turn must be non-negative")
	}
	if strings.TrimSpace(request.Persona) == "" {
		return fmt.Errorf("persona is required")
	}
	if len(request.AvailableActions) == 0 {
		return fmt.Errorf("available_actions must not be empty")
	}
	if strings.TrimSpace(request.Request) == "" {
		return fmt.Errorf("request is required")
	}
	return nil
}

func validateGameAgentDecision(request GameAgentDecisionRequest, decision GameBrainDecision) error {
	if strings.TrimSpace(decision.AgentID) == "" {
		return fmt.Errorf("agent_id is required")
	}
	if decision.AgentID != request.Persona {
		return fmt.Errorf("agent_id %q does not own persona %q", decision.AgentID, request.Persona)
	}
	if decision.Persona != request.Persona {
		return fmt.Errorf("decision persona %q does not match request persona %q", decision.Persona, request.Persona)
	}
	if strings.TrimSpace(decision.Intent) == "" {
		return fmt.Errorf("intent is required")
	}
	available := make(map[string]struct{}, len(request.AvailableActions))
	for _, action := range request.AvailableActions {
		available[action] = struct{}{}
	}
	if _, ok := available[decision.Intent]; !ok {
		return fmt.Errorf("intent %q is not available", decision.Intent)
	}
	if decision.Confidence < 0 || decision.Confidence > 1 {
		return fmt.Errorf("confidence must be between 0 and 1")
	}
	for i, step := range decision.ActionPlan {
		if _, ok := available[step.Action]; !ok {
			return fmt.Errorf("action_plan[%d].action %q is not available", i, step.Action)
		}
	}
	return nil
}
