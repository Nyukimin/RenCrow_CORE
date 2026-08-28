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
	return completeAgentGameDecision(persona, decision), nil
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
intentはavailable_actionsの文字列と完全一致させてください。
出力は次の単一field JSONオブジェクトだけにしてください。別field、Markdown、説明文は禁止です。
{"intent":"<action>"}

Game turn input:
` + string(payload), nil
}

func decodeAgentGameDecision(content string) (viewer.GameBrainDecision, error) {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(content)))
	decoder.DisallowUnknownFields()
	var payload struct {
		Intent string `json:"intent"`
	}
	if err := decoder.Decode(&payload); err != nil {
		return viewer.GameBrainDecision{}, fmt.Errorf("decode Agent game decision JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return viewer.GameBrainDecision{}, fmt.Errorf("Agent game decision contains trailing content")
	}
	return viewer.GameBrainDecision{
		Intent: strings.TrimSpace(payload.Intent),
	}, nil
}

var _ viewer.GameAgentDecisionProvider = (*gameAgentDecisionService)(nil)
