package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
)

type gameAgentDecisionService struct {
	agents agentRuntime
}

const gameAgentDecisionAttempts = 3

func newGameAgentDecisionService(agents agentRuntime) viewer.GameAgentDecisionProvider {
	return &gameAgentDecisionService{agents: agents}
}

func (s *gameAgentDecisionService) DecideGameTurn(ctx context.Context, request viewer.GameAgentDecisionRequest) (viewer.GameBrainDecision, error) {
	if strings.TrimSpace(request.GameID) != "nethack" {
		return viewer.GameBrainDecision{}, fmt.Errorf(
			"Agent game decision is not implemented for game %q",
			request.GameID,
		)
	}
	prompt, err := buildGameAgentTurnPrompt(request)
	if err != nil {
		return viewer.GameBrainDecision{}, err
	}
	persona := strings.ToLower(strings.TrimSpace(request.Persona))
	var generate func() (string, error)
	switch persona {
	case "mio":
		generate = func() (string, error) { return s.agents.Mio.DecideGameTurn(ctx, prompt, "mio") }
	case "shiro":
		generate = func() (string, error) { return s.agents.ShiroChat.DecideGameTurn(ctx, prompt, "shiro") }
	case "kuro":
		generate = func() (string, error) { return s.agents.Heavy.DecideGameTurn(ctx, prompt) }
	case "midori":
		generate = func() (string, error) { return s.agents.Wild.DecideGameTurn(ctx, prompt) }
	default:
		return viewer.GameBrainDecision{}, fmt.Errorf("unknown Agent %q", request.Persona)
	}
	decision, err := generateValidatedGameIntent(generate, request.AvailableActions, gameAgentDecisionAttempts)
	if err != nil {
		return viewer.GameBrainDecision{}, err
	}
	return completeAgentGameDecision(persona, decision), nil
}

func generateValidatedGameIntent(generate func() (string, error), availableActions []string, attempts int) (viewer.GameBrainDecision, error) {
	if generate == nil || attempts < 1 {
		return viewer.GameBrainDecision{}, fmt.Errorf("Agent game intent generator is unavailable")
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		raw, err := generate()
		if err != nil {
			return viewer.GameBrainDecision{}, err
		}
		decision, err := decodeAgentGameIntent(raw, availableActions)
		if err == nil {
			return decision, nil
		}
		lastErr = err
	}
	return viewer.GameBrainDecision{}, fmt.Errorf("Agent game intent remained invalid after %d attempts: %w", attempts, lastErr)
}

func completeAgentGameDecision(persona string, decision viewer.GameBrainDecision) viewer.GameBrainDecision {
	decision.AgentID = persona
	decision.Persona = persona
	decision.Reason = "CORE Agent selected available action: " + decision.Intent
	decision.ActionPlan = []viewer.GameActionStep{{Action: decision.Intent}}
	return decision
}

func buildGameAgentTurnPrompt(request viewer.GameAgentDecisionRequest) (string, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode game observation: %w", err)
	}
	return `Agent自身がプレイしているゲームの1ターンです。
観測と available_actions の範囲だけで、次の行動を一つ決めてください。
出力はavailable_actionsの文字列から選んだaction tokenを一つだけ返してください。
JSON、引用符、Markdown、説明文、改行は禁止です。

Game turn input:
` + string(payload), nil
}

func decodeAgentGameIntent(content string, availableActions []string) (viewer.GameBrainDecision, error) {
	content = strings.TrimSpace(content)
	end := 0
	for end < len(content) && isGameActionTokenByte(content[end]) {
		end++
	}
	intent := content[:end]
	for _, available := range availableActions {
		if intent == available {
			return viewer.GameBrainDecision{Intent: intent}, nil
		}
	}
	return viewer.GameBrainDecision{}, fmt.Errorf("Agent game intent is not an exact available action")
}

func isGameActionTokenByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_' || value == '.' || value == '-'
}

var _ viewer.GameAgentDecisionProvider = (*gameAgentDecisionService)(nil)
