package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type gameAgentDecisionService struct {
	agents      agentRuntime
	actorAgents map[string]*domainagent.MioAgent
}

const gameAgentDecisionAttempts = 3
const structuredGameDecisionMaxBytes = 16 * 1024

var agentPlayableGameTitles = [...]string{"nethack", "survival_garden", "territory_commander", "herzog_zwei"}
var gameTurnActorIDs = [...]string{"aka", "ao", "kin", "gin"}

func supportedAgentGameIDs() []string {
	return append([]string(nil), agentPlayableGameTitles[:]...)
}

func isSupportedAgentGameID(gameID string) bool {
	for _, supported := range agentPlayableGameTitles {
		if gameID == supported {
			return true
		}
	}
	return false
}

func newGameAgentDecisionService(agents agentRuntime, actorProviders map[string]llm.LLMProvider, characterPrompts map[string]string) viewer.GameAgentDecisionProvider {
	actorAgents := make(map[string]*domainagent.MioAgent, len(gameTurnActorIDs))
	for _, actorID := range gameTurnActorIDs {
		provider := actorProviders[actorID]
		characterPrompt := strings.TrimSpace(characterPrompts[actorID])
		if provider == nil || characterPrompt == "" {
			continue
		}
		actorAgents[actorID] = domainagent.NewMioAgent(provider, nil, nil, nil, nil, nil).
			WithViewerRecipientPrompts(map[string]string{actorID: characterPrompt})
	}
	return &gameAgentDecisionService{agents: agents, actorAgents: actorAgents}
}

func (s *gameAgentDecisionService) DecideGameTurn(ctx context.Context, request viewer.GameAgentDecisionRequest) (viewer.GameBrainDecision, error) {
	if !isSupportedAgentGameID(request.GameID) {
		return viewer.GameBrainDecision{}, fmt.Errorf(
			"Agent game decision is not implemented for game %q",
			request.GameID,
		)
	}
	prompt, err := buildGameAgentTurnPrompt(request)
	if err != nil {
		return viewer.GameBrainDecision{}, err
	}
	var structuredPrompt string
	var actionContracts *gameActionContractSet
	if request.GameID != "nethack" {
		structuredPrompt, err = buildStructuredGameAgentTurnPrompt(request)
		if err != nil {
			return viewer.GameBrainDecision{}, err
		}
		actionContracts, _, err = parseGameActionContracts(request.Observation)
		if err != nil {
			return viewer.GameBrainDecision{}, fmt.Errorf("invalid structured game action contract: %w", err)
		}
	}
	persona := strings.ToLower(strings.TrimSpace(request.Persona))
	var generateToken func() (string, error)
	var generateStructured func(string) (string, error)
	switch persona {
	case "mio":
		generateToken = func() (string, error) { return s.agents.Mio.DecideGameTurn(ctx, prompt, "mio") }
		generateStructured = func(feedback string) (string, error) {
			return s.agents.Mio.DecideStructuredGameTurn(ctx, structuredPromptWithFeedback(structuredPrompt, feedback), "mio")
		}
	case "shiro":
		generateToken = func() (string, error) { return s.agents.ShiroChat.DecideGameTurn(ctx, prompt, "shiro") }
		generateStructured = func(feedback string) (string, error) {
			return s.agents.ShiroChat.DecideStructuredGameTurn(ctx, structuredPromptWithFeedback(structuredPrompt, feedback), "shiro")
		}
	case "kuro":
		generateToken = func() (string, error) { return s.agents.Heavy.DecideGameTurn(ctx, prompt) }
		generateStructured = func(feedback string) (string, error) {
			return s.agents.Heavy.DecideStructuredGameTurn(ctx, structuredPromptWithFeedback(structuredPrompt, feedback))
		}
	case "midori":
		generateToken = func() (string, error) { return s.agents.Wild.DecideGameTurn(ctx, prompt) }
		generateStructured = func(feedback string) (string, error) {
			return s.agents.Wild.DecideStructuredGameTurn(ctx, structuredPromptWithFeedback(structuredPrompt, feedback))
		}
	case "aka", "ao", "kin", "gin":
		actor := s.actorAgents[persona]
		if actor == nil {
			return viewer.GameBrainDecision{}, fmt.Errorf("Agent %q game cognition is unavailable", persona)
		}
		generateToken = func() (string, error) {
			return actor.DecideGameTurn(ctx, prompt, persona)
		}
		generateStructured = func(feedback string) (string, error) {
			return actor.DecideStructuredGameTurn(ctx, structuredPromptWithFeedback(structuredPrompt, feedback), persona)
		}
	default:
		return viewer.GameBrainDecision{}, fmt.Errorf("unknown Agent %q", request.Persona)
	}
	if request.GameID == "nethack" {
		decision, err := generateValidatedGameIntent(generateToken, request.AvailableActions, gameAgentDecisionAttempts)
		if err != nil {
			return viewer.GameBrainDecision{}, err
		}
		return completeAgentGameDecision(persona, decision), nil
	}
	var validateContract func(viewer.GameActionStep) error
	if actionContracts != nil {
		validateContract = actionContracts.validate
	}
	step, err := generateValidatedStructuredGameActionWithContract(generateStructured, request.AvailableActions, validateContract, gameAgentDecisionAttempts)
	if err != nil {
		return viewer.GameBrainDecision{}, err
	}
	return completeAgentStructuredGameDecision(persona, step), nil
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

func completeAgentStructuredGameDecision(persona string, step viewer.GameActionStep) viewer.GameBrainDecision {
	return viewer.GameBrainDecision{
		AgentID:    persona,
		Persona:    persona,
		Intent:     step.Action,
		Reason:     "CORE Agent selected available action: " + step.Action,
		ActionPlan: []viewer.GameActionStep{step},
	}
}

func generateValidatedStructuredGameAction(generate func() (string, error), availableActions []string, attempts int) (viewer.GameActionStep, error) {
	if generate == nil {
		return viewer.GameActionStep{}, fmt.Errorf("Agent structured game action generator is unavailable")
	}
	return generateValidatedStructuredGameActionWithContract(func(string) (string, error) {
		return generate()
	}, availableActions, nil, attempts)
}

func generateValidatedStructuredGameActionWithContract(generate func(string) (string, error), availableActions []string, validateContract func(viewer.GameActionStep) error, attempts int) (viewer.GameActionStep, error) {
	if generate == nil || attempts < 1 {
		return viewer.GameActionStep{}, fmt.Errorf("Agent structured game action generator is unavailable")
	}
	var lastErr error
	feedback := ""
	for attempt := 0; attempt < attempts; attempt++ {
		raw, err := generate(feedback)
		if err != nil {
			return viewer.GameActionStep{}, err
		}
		step, err := decodeStructuredGameAction(raw, availableActions)
		if err != nil {
			lastErr = err
			feedback = structuredGameActionFeedback(err)
			continue
		}
		if validateContract != nil {
			if err := validateContract(step); err != nil {
				lastErr = err
				feedback = structuredGameActionFeedback(err)
				continue
			}
		}
		return step, nil
	}
	return viewer.GameActionStep{}, fmt.Errorf("Agent structured game action remained invalid after %d attempts: %w", attempts, lastErr)
}

func structuredGameActionFeedback(err error) string {
	return "The previous action was invalid: " + err.Error() + ". Return exactly one JSON object with only the top-level fields action, optional target, and optional args; put action parameters inside args. Follow available_actions and any supplied action_contracts and legal_ids in the original Game turn input. Do not invent missing values."
}

func structuredPromptWithFeedback(prompt, feedback string) string {
	if feedback == "" {
		return prompt
	}
	return prompt + "\n\n" + feedback
}

func decodeStructuredGameAction(content string, availableActions []string) (viewer.GameActionStep, error) {
	if len(content) == 0 || len(content) > structuredGameDecisionMaxBytes {
		return viewer.GameActionStep{}, fmt.Errorf("Agent structured game action exceeds the output size limit")
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return viewer.GameActionStep{}, fmt.Errorf("Agent structured game action must be one JSON object")
	}
	fields := make(map[string]json.RawMessage, 3)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return viewer.GameActionStep{}, fmt.Errorf("read Agent action field: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return viewer.GameActionStep{}, fmt.Errorf("Agent action field name must be a string")
		}
		if key != "action" && key != "target" && key != "args" {
			return viewer.GameActionStep{}, fmt.Errorf("unknown Agent action field %q", key)
		}
		if _, exists := fields[key]; exists {
			return viewer.GameActionStep{}, fmt.Errorf("duplicate Agent action field %q", key)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return viewer.GameActionStep{}, fmt.Errorf("decode Agent action field %q: %w", key, err)
		}
		fields[key] = raw
	}
	if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
		return viewer.GameActionStep{}, fmt.Errorf("close Agent action object")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return viewer.GameActionStep{}, fmt.Errorf("Agent structured game action has trailing JSON")
		}
		return viewer.GameActionStep{}, fmt.Errorf("Agent structured game action has trailing content: %w", err)
	}

	action, err := decodeRequiredJSONString(fields["action"], "action")
	if err != nil {
		return viewer.GameActionStep{}, err
	}
	if !isAvailableGameAction(action, availableActions) {
		return viewer.GameActionStep{}, fmt.Errorf("Agent action %q is not available", action)
	}
	step := viewer.GameActionStep{Action: action}
	if raw, exists := fields["target"]; exists && !isJSONNull(raw) {
		step.Target, err = decodeRequiredJSONString(raw, "target")
		if err != nil {
			return viewer.GameActionStep{}, err
		}
	}
	if raw, exists := fields["args"]; exists && !isJSONNull(raw) {
		trimmed := strings.TrimSpace(string(raw))
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return viewer.GameActionStep{}, fmt.Errorf("Agent action args must be an object")
		}
		argsDecoder := json.NewDecoder(strings.NewReader(trimmed))
		argsDecoder.UseNumber()
		if err := argsDecoder.Decode(&step.Args); err != nil {
			return viewer.GameActionStep{}, fmt.Errorf("decode Agent action args: %w", err)
		}
		if step.Args == nil {
			return viewer.GameActionStep{}, fmt.Errorf("Agent action args must be an object")
		}
	}
	return step, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

func decodeRequiredJSONString(raw json.RawMessage, field string) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", fmt.Errorf("Agent action %s must be a string", field)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("decode Agent action %s: %w", field, err)
	}
	if field == "action" && strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("Agent action is required")
	}
	return value, nil
}

func isAvailableGameAction(action string, availableActions []string) bool {
	for _, available := range availableActions {
		if action == available {
			return true
		}
	}
	return false
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

func buildStructuredGameAgentTurnPrompt(request viewer.GameAgentDecisionRequest) (string, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode structured game observation: %w", err)
	}
	return "Agent自身がプレイしているゲームの1ターンです。\n" +
		"observation内のaction_contractsとlegal_ids、およびavailable_actionsの範囲だけを使って、実行可能な行動を一つ決めてください。\n" +
		"出力はaction、任意のtarget、任意のargsだけを持つJSON objectを一つ返してください。不要なtarget/argsは省略してください。\n" +
		"必須のtargetやargsを省略せず、観測にない対象や値を作らないでください。説明文や追加fieldを含めないでください。\n\n" +
		"Game turn input:\n" + string(payload), nil
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
