package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func TestBuildGameAgentTurnPromptCarriesObservationAndStrictContract(t *testing.T) {
	prompt, err := buildGameAgentTurnPrompt(viewer.GameAgentDecisionRequest{
		GameID:           "nethack",
		SessionID:        "nh_agent_e2e",
		Turn:             2,
		Persona:          "mio",
		Observation:      map[string]any{"depth": 1},
		AvailableActions: []string{"search", "rest"},
		Request:          "choose_next_action",
	})
	if err != nil {
		t.Fatalf("build prompt: %v", err)
	}
	for _, required := range []string{`"session_id":"nh_agent_e2e"`, `"search"`, "action tokenを一つ", "JSON、引用符、Markdown、説明文、改行は禁止"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q: %s", required, prompt)
		}
	}
	if strings.Contains(prompt, "\n  ") {
		t.Fatalf("game observation must use compact JSON to preserve context budget: %s", prompt)
	}
}

func TestDecodeAgentGameIntentRejectsJSONFence(t *testing.T) {
	if _, err := decodeAgentGameIntent("```json\n{\"intent\":\"search\"}\n```", []string{"search"}); err == nil {
		t.Fatal("CORE must reject decoder-specific JSON fences")
	}
}

func TestDecodeAgentGameIntentRejectsProse(t *testing.T) {
	if _, err := decodeAgentGameIntent("I choose search", []string{"search"}); err == nil {
		t.Fatal("expected prose to be rejected")
	}
}

func TestDecodeAgentGameIntentAcceptsExactAvailableAction(t *testing.T) {
	decision, err := decodeAgentGameIntent("search", []string{"search", "rest"})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decision.Intent != "search" {
		t.Fatalf("unexpected decision: %+v", decision)
	}
}

func TestDecodeAgentGameIntentAcceptsValidatedLeadingActionToken(t *testing.T) {
	for _, raw := range []string{"search,garbage", "search/repeated", "search because"} {
		decision, err := decodeAgentGameIntent(raw, []string{"search", "rest"})
		if err != nil || decision.Intent != "search" {
			t.Fatalf("raw=%q decision=%+v err=%v", raw, decision, err)
		}
	}
	for _, raw := range []string{"unknown/search", `"search"`, "_search"} {
		if _, err := decodeAgentGameIntent(raw, []string{"search", "rest"}); err == nil {
			t.Fatalf("invalid leading token accepted: %q", raw)
		}
	}
}

func TestDecodeAgentGameIntentRejectsUnavailableAction(t *testing.T) {
	if _, err := decodeAgentGameIntent("attack", []string{"search", "rest"}); err == nil {
		t.Fatal("game decision LLM residual must match available actions")
	}
}

func TestGenerateValidatedGameIntentRetriesOnlyBoundedInvalidOutputs(t *testing.T) {
	outputs := []string{"200000", "unknown/action", "search/trailing"}
	calls := 0
	decision, err := generateValidatedGameIntent(func() (string, error) {
		value := outputs[calls]
		calls++
		return value, nil
	}, []string{"search", "rest"}, 3)
	if err != nil || decision.Intent != "search" || calls != 3 {
		t.Fatalf("decision=%+v calls=%d err=%v", decision, calls, err)
	}
	calls = 0
	if _, err := generateValidatedGameIntent(func() (string, error) {
		calls++
		return "invalid", nil
	}, []string{"search"}, 3); err == nil || calls != 3 {
		t.Fatalf("bounded retry was not enforced: calls=%d err=%v", calls, err)
	}
}

func TestGameAgentDecisionServiceRejectsUnknownTitle(t *testing.T) {
	provider := &gameDecisionLLMProvider{}
	service := newTestGameDecisionService(provider)
	_, err := service.DecideGameTurn(t.Context(), viewer.GameAgentDecisionRequest{
		GameID:           "unknown_game",
		SessionID:        "sg_not_agent_e2e",
		Persona:          "mio",
		AvailableActions: []string{"rest"},
		Request:          "choose_next_action",
	})
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("expected unmigrated title rejection, got %v", err)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("unknown title reached Agent inference: %d calls", len(provider.requests))
	}
}

func TestGameAgentDecisionServiceDerivesExecutionFieldsFromValidatedIntent(t *testing.T) {
	decision := completeAgentGameDecision("mio", viewer.GameBrainDecision{Intent: "search"})
	if decision.Reason == "" || len(decision.ActionPlan) != 1 || decision.ActionPlan[0].Action != "search" {
		t.Fatalf("derived decision fields are incomplete: %+v", decision)
	}
}

func TestGameAgentDecisionServiceUsesStructuredActionForThreeTitles(t *testing.T) {
	cases := []struct {
		name        string
		persona     string
		output      string
		available   []string
		action      string
		target      string
		args        map[string]any
		observation map[string]any
	}{
		{
			name: "survival_garden", persona: "mio",
			output:    "{\"action\":\"move\",\"target\":\"hex-1\",\"args\":{\"pace\":\"careful\"}}",
			available: []string{"move", "rest"}, action: "move", target: "hex-1",
			args: map[string]any{"pace": "careful"},
			observation: map[string]any{
				"action_contracts": map[string]any{"move": map[string]any{
					"target": map[string]any{"type": "string", "required": true, "enum_ref": "move_targets"},
				}},
				"legal_ids": map[string]any{"move_targets": []string{"hex-1"}},
			},
		},
		{
			name: "territory_commander", persona: "shiro",
			output:    "{\"action\":\"produce\",\"args\":{\"unit\":\"scout\"}}",
			available: []string{"produce", "capture"}, action: "produce",
			args: map[string]any{"unit": "scout"},
			observation: map[string]any{
				"action_contracts": map[string]any{"produce": map[string]any{
					"args": map[string]any{"unit": map[string]any{"type": "string", "required": true, "enum_ref": "unit_types"}},
				}},
				"legal_ids": map[string]any{"unit_types": []string{"scout"}},
			},
		},
		{
			name: "herzog_zwei", persona: "kuro",
			output:    "{\"action\":\"attack_move\",\"args\":{\"unit_id\":\"unit-1\",\"to\":{\"x\":3,\"y\":4}}}",
			available: []string{"attack_move", "wait"}, action: "attack_move",
			args: map[string]any{"unit_id": "unit-1", "to": map[string]any{"x": json.Number("3"), "y": json.Number("4")}},
			observation: map[string]any{
				"action_contracts": map[string]any{"attack_move": map[string]any{
					"args": map[string]any{
						"unit_id": map[string]any{"type": "string", "required": true, "enum_ref": "own_unit_ids"},
						"to":      map[string]any{"type": "coordinate", "required": true, "x_min": 0, "x_max": 5, "y_min": 0, "y_max": 5},
					},
				}},
				"legal_ids": map[string]any{"own_unit_ids": []string{"unit-1"}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &gameDecisionLLMProvider{responses: []string{tc.output}}
			service := newTestGameDecisionService(provider)
			request := viewer.GameAgentDecisionRequest{
				GameID:           tc.name,
				SessionID:        "session-" + tc.name,
				Turn:             4,
				Persona:          tc.persona,
				Observation:      tc.observation,
				AvailableActions: tc.available,
				Request:          "choose_next_action",
			}
			decision, err := service.DecideGameTurn(t.Context(), request)
			if err != nil {
				t.Fatalf("DecideGameTurn() error = %v", err)
			}
			if len(provider.requests) != 1 {
				t.Fatalf("Agent calls = %d, want one", len(provider.requests))
			}
			gotRequest := provider.requests[0]
			if gotRequest.ResponseFormat != llm.ResponseFormatJSONObject || gotRequest.MaxTokens != 256 || gotRequest.OnToken != nil {
				t.Fatalf("structured generation request format/budget/stream = %q/%d/%t", gotRequest.ResponseFormat, gotRequest.MaxTokens, gotRequest.OnToken != nil)
			}
			if _, ok := gotRequest.ProviderOptions["stop"]; ok {
				t.Fatalf("structured generation included token stops: %#v", gotRequest.ProviderOptions)
			}
			prompt := gotRequest.Messages[len(gotRequest.Messages)-1].Content
			if !strings.Contains(prompt, "\"action_contracts\"") || !strings.Contains(prompt, "\"legal_ids\"") {
				t.Fatalf("Agent prompt omitted action contracts or legal identifiers: %s", prompt)
			}
			if decision.AgentID != tc.persona || decision.Persona != tc.persona || decision.Intent != tc.action {
				t.Fatalf("decision identity/intent = %+v", decision)
			}
			if len(decision.ActionPlan) != 1 {
				t.Fatalf("action plan length = %d, want one", len(decision.ActionPlan))
			}
			step := decision.ActionPlan[0]
			if step.Action != tc.action || step.Target != tc.target || !jsonValuesEqual(step.Args, tc.args) {
				t.Fatalf("action step = %+v, want action=%q target=%q args=%v", step, tc.action, tc.target, tc.args)
			}
		})
	}
}

func TestGameAgentDecisionServiceKeepsNetHackTokenContract(t *testing.T) {
	provider := &gameDecisionLLMProvider{responses: []string{"search"}}
	service := newTestGameDecisionService(provider)
	decision, err := service.DecideGameTurn(t.Context(), viewer.GameAgentDecisionRequest{
		GameID:           "nethack",
		SessionID:        "nh_agent",
		Turn:             1,
		Persona:          "mio",
		Observation:      map[string]any{"depth": 1},
		AvailableActions: []string{"search", "rest"},
		Request:          "choose_next_action",
	})
	if err != nil {
		t.Fatalf("DecideGameTurn() error = %v", err)
	}
	got := provider.requests[0]
	if got.ResponseFormat != llm.ResponseFormatText || got.MaxTokens != 16 || got.OnToken != nil {
		t.Fatalf("NetHack request format/budget/stream = %q/%d/%t", got.ResponseFormat, got.MaxTokens, got.OnToken != nil)
	}
	stop, ok := got.ProviderOptions["stop"].([]string)
	if !ok || len(stop) != 4 || stop[0] != "," || stop[1] != "\n" || stop[2] != " " || stop[3] != "\"" {
		t.Fatalf("NetHack token stop contract changed: %#v", got.ProviderOptions)
	}
	if decision.AgentID != "mio" || decision.Persona != "mio" || decision.Intent != "search" ||
		len(decision.ActionPlan) != 1 || decision.ActionPlan[0].Action != "search" {
		t.Fatalf("NetHack Agent decision changed: %+v", decision)
	}
}

func TestGameAgentDecisionServiceUsesMidoriForStructuredGameTurn(t *testing.T) {
	provider := &gameDecisionLLMProvider{responses: []string{"{\"action\":\"wait\"}"}}
	service := newTestGameDecisionService(provider)
	decision, err := service.DecideGameTurn(t.Context(), viewer.GameAgentDecisionRequest{
		GameID:           "herzog_zwei",
		SessionID:        "hz_midori",
		Turn:             2,
		Persona:          "midori",
		Observation:      map[string]any{"action_contracts": map[string]any{"wait": map[string]any{}}, "legal_ids": map[string]any{}},
		AvailableActions: []string{"wait"},
		Request:          "choose_next_action",
	})
	if err != nil {
		t.Fatalf("DecideGameTurn() error = %v", err)
	}
	if decision.AgentID != "midori" || decision.Persona != "midori" || decision.Intent != "wait" || len(decision.ActionPlan) != 1 {
		t.Fatalf("Midori identity was not preserved: %+v", decision)
	}
}

func TestDecodeStructuredGameActionRejectsMalformedUnknownAndTrailingOutput(t *testing.T) {
	for _, raw := range []string{
		"{\"target\":\"hex-1\"}",
		"{\"action\":\"move\",\"extra\":\"no\"}",
		"{\"action\":\"move\",\"action\":\"rest\"}",
		"{\"action\":\"move\"} {}",
		"{\"action\":\"move\",\"target\":4}",
		"{\"action\":\"move\",\"args\":[]}",
		"{\"action\":\"unknown\"}",
		"not json",
	} {
		if _, err := decodeStructuredGameAction(raw, []string{"move", "rest"}); err == nil {
			t.Errorf("decodeStructuredGameAction(%q) succeeded, want rejection", raw)
		}
	}
	if _, err := decodeStructuredGameAction(strings.Repeat(" ", structuredGameDecisionMaxBytes+1), []string{"move"}); err == nil {
		t.Fatal("oversized structured action was accepted")
	}
}

func TestDecodeStructuredGameActionAcceptsOmittedAndNullOptionalFields(t *testing.T) {
	for _, raw := range []string{
		"{\"action\":\"rest\"}",
		"{\"action\":\"rest\",\"target\":null,\"args\":null}",
		"{\"action\":\"rest\",\"args\":{}}",
	} {
		step, err := decodeStructuredGameAction(raw, []string{"rest"})
		if err != nil || step.Action != "rest" {
			t.Errorf("decodeStructuredGameAction(%q) = %+v, %v; want valid rest action", raw, step, err)
		}
	}
}

func TestGenerateValidatedStructuredGameActionRetriesInvalidJSONAndStopsAtLimit(t *testing.T) {
	outputs := []string{"{\"action\":\"unknown\"}", "{\"action\":\"move\",\"target\":\"hex-1\"}"}
	calls := 0
	step, err := generateValidatedStructuredGameAction(func() (string, error) {
		raw := outputs[calls]
		calls++
		return raw, nil
	}, []string{"move", "rest"}, 3)
	if err != nil || calls != 2 || step.Action != "move" || step.Target != "hex-1" {
		t.Fatalf("step=%+v calls=%d err=%v", step, calls, err)
	}
	calls = 0
	if _, err := generateValidatedStructuredGameAction(func() (string, error) {
		calls++
		return "{\"action\":\"unknown\"}", nil
	}, []string{"move"}, 3); err == nil || calls != 3 {
		t.Fatalf("retry limit not enforced: calls=%d err=%v", calls, err)
	}
	transportErr := errors.New("gateway unavailable")
	calls = 0
	if _, err := generateValidatedStructuredGameAction(func() (string, error) {
		calls++
		return "", transportErr
	}, []string{"move"}, 3); !errors.Is(err, transportErr) || calls != 1 {
		t.Fatalf("transport failure was retried or lost: calls=%d err=%v", calls, err)
	}
}

func TestSupportedAgentGameIDsAreSharedAndDefensive(t *testing.T) {
	want := []string{"nethack", "survival_garden", "territory_commander", "herzog_zwei"}
	got := supportedAgentGameIDs()
	if len(got) != len(want) {
		t.Fatalf("supported Agent games = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("supported Agent game[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	got[0] = "unknown"
	if supportedAgentGameIDs()[0] != "nethack" {
		t.Fatal("supported Agent game list was not defensively copied")
	}
	if isSupportedAgentGameID("Unknown") || isSupportedAgentGameID("nethack ") {
		t.Fatal("unknown or noncanonical game ID was accepted")
	}
}

type gameDecisionLLMProvider struct {
	responses []string
	requests  []llm.GenerateRequest
	err       error
}

func (p *gameDecisionLLMProvider) Generate(_ context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.requests = append(p.requests, request)
	if p.err != nil {
		return llm.GenerateResponse{}, p.err
	}
	index := len(p.requests) - 1
	if index >= len(p.responses) {
		return llm.GenerateResponse{}, errors.New("missing test response")
	}
	return llm.GenerateResponse{Content: p.responses[index]}, nil
}

func (p *gameDecisionLLMProvider) Name() string {
	return "game-decision-test-provider"
}

func newTestGameDecisionService(provider llm.LLMProvider) *gameAgentDecisionService {
	mio := domainagent.NewMioAgent(provider, nil, nil, nil, nil, nil)
	return &gameAgentDecisionService{agents: agentRuntime{
		Mio:       mio,
		ShiroChat: mio,
		Heavy:     domainagent.NewHeavyAgent(provider, "Kuro"),
		Wild:      domainagent.NewWildAgent(provider, "Midori"),
	}}
}

func jsonValuesEqual(left, right map[string]any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}
