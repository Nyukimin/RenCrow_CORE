package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
)

type gameAgentDecisionService struct {
	agents agentRuntime
}

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
	var raw string
	switch persona {
	case "mio":
		raw, err = s.agents.Mio.DecideGameTurn(ctx, prompt, "mio")
	case "shiro":
		raw, err = s.agents.ShiroChat.DecideGameTurn(ctx, prompt, "shiro")
	case "kuro":
		raw, err = s.agents.Heavy.DecideGameTurn(ctx, prompt)
	case "midori":
		raw, err = s.agents.Wild.DecideGameTurn(ctx, prompt)
	default:
		return viewer.GameBrainDecision{}, fmt.Errorf("unknown Agent %q", request.Persona)
	}
	if err != nil {
		return viewer.GameBrainDecision{}, err
	}
	decision, err := decodeAgentGameDecision(raw)
	if err != nil {
		return viewer.GameBrainDecision{}, err
	}
	decision.AgentID = persona
	decision.Persona = persona
	return decision, nil
}

func buildGameAgentTurnPrompt(request viewer.GameAgentDecisionRequest) (string, error) {
	payload, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode game observation: %w", err)
	}
	return `Agent自身がプレイしているゲームの1ターンです。
観測と available_actions の範囲だけで、次の行動を一つ決めてください。
intent と action_plan[].action は available_actions の文字列と完全一致させてください。
対象が必要な行動では、観測内のIDを action_plan[].target または args に指定してください。
出力は次の形のJSONオブジェクトだけにしてください。Markdownや説明文は禁止です。
{"intent":"<action>","reason":"<Agent自身の短い判断理由>","action_plan":[{"action":"<action>","target":"<optional id>","args":{}}],"memory_refs":[],"confidence":0.0}

Game turn input:
` + string(payload), nil
}

func decodeAgentGameDecision(content string) (viewer.GameBrainDecision, error) {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(content)))
	decoder.DisallowUnknownFields()
	var payload struct {
		Intent     string                  `json:"intent"`
		Reason     string                  `json:"reason"`
		ActionPlan []viewer.GameActionStep `json:"action_plan"`
		MemoryRefs []string                `json:"memory_refs"`
		Confidence float64                 `json:"confidence"`
	}
	if err := decoder.Decode(&payload); err != nil {
		return viewer.GameBrainDecision{}, fmt.Errorf("decode Agent game decision JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return viewer.GameBrainDecision{}, fmt.Errorf("Agent game decision contains trailing content")
	}
	return viewer.GameBrainDecision{
		Intent:     strings.TrimSpace(payload.Intent),
		Reason:     strings.TrimSpace(payload.Reason),
		ActionPlan: payload.ActionPlan,
		MemoryRefs: append([]string(nil), payload.MemoryRefs...),
		Confidence: payload.Confidence,
	}, nil
}

var _ viewer.GameAgentDecisionProvider = (*gameAgentDecisionService)(nil)
