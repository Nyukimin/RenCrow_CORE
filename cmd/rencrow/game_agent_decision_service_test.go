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

func TestGenerateValidatedStructuredGameActionRejectsNilGenerator(t *testing.T) {
	if _, err := generateValidatedStructuredGameAction(nil, []string{"wait"}, 3); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("nil generator error = %v, want unavailable error", err)
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

func TestGameAgentDecisionServiceRetriesMissingContractArgsWithFeedback(t *testing.T) {
	provider := &gameDecisionLLMProvider{responses: []string{
		`{"action":"attack_move","target":"mio_commander","args":{"to":{"x":2,"y":1}}}`,
		`{"action":"attack_move","args":{"unit_id":"mio_commander","to":{"x":2,"y":1}}}`,
	}}
	service := newTestGameDecisionService(provider)
	request := viewer.GameAgentDecisionRequest{
		GameID:           "herzog_zwei",
		SessionID:        "hz_contract_retry",
		Turn:             126,
		Persona:          "mio",
		Observation:      herzogZweiAttackMoveContractObservation(),
		AvailableActions: []string{"attack_move", "wait"},
		Request:          "issue_orders",
	}
	decision, err := service.DecideGameTurn(t.Context(), request)
	if err != nil {
		t.Fatalf("DecideGameTurn() error = %v", err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("Agent calls = %d, want retry after missing required args", len(provider.requests))
	}
	step := decision.ActionPlan[0]
	if decision.Intent != "attack_move" || step.Args["unit_id"] != "mio_commander" || step.Target != "" {
		t.Fatalf("corrected decision changed or inferred action fields: %+v", decision)
	}
	feedbackPrompt := provider.requests[1].Messages[len(provider.requests[1].Messages)-1].Content
	for _, required := range []string{"action_contracts.attack_move.args.unit_id is required", "action_contracts", "legal_ids", "Do not invent missing values"} {
		if !strings.Contains(feedbackPrompt, required) {
			t.Fatalf("retry prompt missing %q: %s", required, feedbackPrompt)
		}
	}
}

func TestGameAgentDecisionServiceRetriesMalformedStructuredActionsWithFeedback(t *testing.T) {
	provider := &gameDecisionLLMProvider{responses: []string{
		`{"action":"attack_move","to":{"x":2,"y":1}}`,
		`not json`,
		`{"action":"attack_move","args":{"unit_id":"mio_commander","to":{"x":2,"y":1}}}`,
	}}
	service := newTestGameDecisionService(provider)
	decision, err := service.DecideGameTurn(t.Context(), viewer.GameAgentDecisionRequest{
		GameID:           "herzog_zwei",
		SessionID:        "hz_decode_retry_feedback",
		Turn:             126,
		Persona:          "mio",
		Observation:      herzogZweiAttackMoveContractObservation(),
		AvailableActions: []string{"attack_move", "wait"},
		Request:          "issue_orders",
	})
	if err != nil {
		t.Fatalf("DecideGameTurn() error = %v", err)
	}
	if len(provider.requests) != 3 {
		t.Fatalf("Agent calls = %d, want corrective feedback on both retries", len(provider.requests))
	}
	if len(decision.ActionPlan) != 1 {
		t.Fatalf("action plan length = %d, want one corrected action", len(decision.ActionPlan))
	}
	step := decision.ActionPlan[0]
	if decision.Intent != "attack_move" || step.Args["unit_id"] != "mio_commander" || step.Target != "" {
		t.Fatalf("corrected decision changed or inferred action fields: %+v", decision)
	}
	feedbackPrompts := []string{
		provider.requests[1].Messages[len(provider.requests[1].Messages)-1].Content,
		provider.requests[2].Messages[len(provider.requests[2].Messages)-1].Content,
	}
	for i, required := range []string{`unknown Agent action field "to"`, "Agent structured game action must be one JSON object"} {
		if !strings.Contains(feedbackPrompts[i], required) {
			t.Fatalf("attempt %d omitted prior decoder error %q", i+2, required)
		}
		for _, reminder := range []string{"top-level fields action, optional target, and optional args", "put action parameters inside args", "available_actions", "action_contracts", "legal_ids", "Do not invent missing values"} {
			if !strings.Contains(feedbackPrompts[i], reminder) {
				t.Fatalf("attempt %d omitted schema reminder %q", i+2, reminder)
			}
		}
	}
}

func TestGameAgentDecisionServiceStopsAfterThreeContractInvalidAttempts(t *testing.T) {
	provider := &gameDecisionLLMProvider{responses: []string{
		`{"action":"attack_move","args":{"to":{"x":2,"y":1}}}`,
		`{"action":"attack_move","args":{"to":{"x":2,"y":1}}}`,
		`{"action":"attack_move","args":{"to":{"x":2,"y":1}}}`,
	}}
	service := newTestGameDecisionService(provider)
	_, err := service.DecideGameTurn(t.Context(), viewer.GameAgentDecisionRequest{
		GameID:           "herzog_zwei",
		SessionID:        "hz_contract_retry_bound",
		Turn:             126,
		Persona:          "mio",
		Observation:      herzogZweiAttackMoveContractObservation(),
		AvailableActions: []string{"attack_move", "wait"},
		Request:          "issue_orders",
	})
	if err == nil || !strings.Contains(err.Error(), "remained invalid after 3 attempts") || !strings.Contains(err.Error(), "args.unit_id is required") {
		t.Fatalf("error = %v, want bounded missing-argument failure", err)
	}
	if len(provider.requests) != 3 {
		t.Fatalf("Agent calls = %d, want exactly three", len(provider.requests))
	}
	for i := 1; i < len(provider.requests); i++ {
		prompt := provider.requests[i].Messages[len(provider.requests[i].Messages)-1].Content
		if !strings.Contains(prompt, "action_contracts.attack_move.args.unit_id is required") {
			t.Fatalf("attempt %d omitted corrective contract feedback", i+1)
		}
	}
}

func TestGameAgentDecisionServiceRetriesAnActionMissingItsDescriptor(t *testing.T) {
	provider := &gameDecisionLLMProvider{responses: []string{
		`{"action":"attack_move","args":{"unit_id":"mio_commander","to":{"x":2,"y":1}}}`,
		`{"action":"wait"}`,
	}}
	service := newTestGameDecisionService(provider)
	decision, err := service.DecideGameTurn(t.Context(), viewer.GameAgentDecisionRequest{
		GameID: "herzog_zwei", SessionID: "hz_missing_selected_descriptor", Persona: "mio",
		Observation: map[string]any{
			"action_contracts": map[string]any{"wait": map[string]any{}},
			"legal_ids":        map[string]any{},
		},
		AvailableActions: []string{"attack_move", "wait"}, Request: "issue_orders",
	})
	if err != nil || decision.Intent != "wait" || len(provider.requests) != 2 {
		t.Fatalf("decision=%+v calls=%d err=%v, want bounded contract-aware reselection", decision, len(provider.requests), err)
	}
	feedbackPrompt := provider.requests[1].Messages[len(provider.requests[1].Messages)-1].Content
	if !strings.Contains(feedbackPrompt, "action_contracts.attack_move is missing") {
		t.Fatalf("retry prompt omitted missing-descriptor feedback: %s", feedbackPrompt)
	}
}

func TestGameAgentDecisionServicePreservesContractlessLegacyAndRejectsPartialMetadata(t *testing.T) {
	provider := &gameDecisionLLMProvider{responses: []string{`{"action":"wait"}`}}
	service := newTestGameDecisionService(provider)
	decision, err := service.DecideGameTurn(t.Context(), viewer.GameAgentDecisionRequest{
		GameID: "herzog_zwei", SessionID: "hz_legacy_no_metadata", Persona: "mio",
		Observation: map[string]any{}, AvailableActions: []string{"wait"}, Request: "issue_orders",
	})
	if err != nil || decision.Intent != "wait" || len(provider.requests) != 1 {
		t.Fatalf("legacy request without both metadata fields changed: decision=%+v calls=%d err=%v", decision, len(provider.requests), err)
	}

	provider = &gameDecisionLLMProvider{}
	service = newTestGameDecisionService(provider)
	_, err = service.DecideGameTurn(t.Context(), viewer.GameAgentDecisionRequest{
		GameID: "herzog_zwei", SessionID: "hz_partial_metadata", Persona: "mio",
		Observation:      map[string]any{"action_contracts": map[string]any{}},
		AvailableActions: []string{"wait"}, Request: "issue_orders",
	})
	if err == nil || !strings.Contains(err.Error(), "must be provided together") || len(provider.requests) != 0 {
		t.Fatalf("partial contract metadata was not rejected before inference: calls=%d err=%v", len(provider.requests), err)
	}
}

func TestGameAgentActionContractsValidateEnumsBoundsAndIntegerConstraints(t *testing.T) {
	contracts, hasMetadata, err := parseGameActionContracts(map[string]any{
		"action_contracts": map[string]any{
			"attack_move": map[string]any{"args": map[string]any{
				"unit_id": map[string]any{"type": "string", "required": true, "enum_ref": "own_units"},
				"to":      map[string]any{"type": "coordinate", "required": true, "bounds_ref": "coordinates"},
				"radius":  map[string]any{"type": "integer", "required": false, "min": 1, "max": 3},
			}},
			"assault": map[string]any{"args": map[string]any{
				"target_faction": map[string]any{"type": "string", "required": true, "enum": []any{"shiro", "kuro"}},
			}},
		},
		"legal_ids": map[string]any{
			"own_units":   []any{"mio_commander"},
			"coordinates": map[string]any{"x_min": 0, "x_max": 5, "y_min": 0, "y_max": 5},
		},
	})
	if err != nil || !hasMetadata {
		t.Fatalf("parse current DSL subset: metadata=%t err=%v", hasMetadata, err)
	}
	valid := viewer.GameActionStep{Action: "attack_move", Args: map[string]any{
		"unit_id": "mio_commander", "to": map[string]any{"x": json.Number("5"), "y": json.Number("0")}, "radius": json.Number("2"),
		"title_extension": "preserved",
	}}
	if err := contracts.validate(valid); err != nil {
		t.Fatalf("valid enum/bounds/integer action rejected: %v", err)
	}
	invalidCases := []struct {
		name string
		step viewer.GameActionStep
		want string
	}{
		{name: "unit enum", step: viewer.GameActionStep{Action: "attack_move", Args: map[string]any{"unit_id": "shiro_commander", "to": map[string]any{"x": json.Number("1"), "y": json.Number("1")}}}, want: "declared enum"},
		{name: "coordinate bounds", step: viewer.GameActionStep{Action: "attack_move", Args: map[string]any{"unit_id": "mio_commander", "to": map[string]any{"x": json.Number("6"), "y": json.Number("1")}}}, want: "coordinate bounds"},
		{name: "integer maximum", step: viewer.GameActionStep{Action: "attack_move", Args: map[string]any{"unit_id": "mio_commander", "to": map[string]any{"x": json.Number("1"), "y": json.Number("1")}, "radius": json.Number("4")}}, want: "at most 3"},
		{name: "direct enum", step: viewer.GameActionStep{Action: "assault", Args: map[string]any{"target_faction": "midori"}}, want: "declared enum"},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			if err := contracts.validate(tc.step); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validate(%+v) error=%v, want %q", tc.step, err, tc.want)
			}
		})
	}
}

func TestParseGameActionContractsRejectsMalformedReferences(t *testing.T) {
	_, _, err := parseGameActionContracts(map[string]any{
		"action_contracts": map[string]any{"attack_move": map[string]any{"args": map[string]any{
			"unit_id": map[string]any{"type": "string", "required": true, "enum_ref": "missing_units"},
		}}},
		"legal_ids": map[string]any{},
	})
	if err == nil || !strings.Contains(err.Error(), "references missing legal_ids.missing_units") {
		t.Fatalf("malformed enum reference error=%v", err)
	}
}

func TestParseGameActionContractsRejectsNonStringTargetDescriptor(t *testing.T) {
	for _, typeName := range []string{"coordinate", "integer"} {
		t.Run(typeName, func(t *testing.T) {
			legalIDs := map[string]any{}
			descriptor := map[string]any{"type": typeName, "required": true}
			if typeName == "coordinate" {
				descriptor["bounds_ref"] = "coordinates"
				legalIDs["coordinates"] = map[string]any{"x_min": 0, "x_max": 1, "y_min": 0, "y_max": 1}
			}
			_, _, err := parseGameActionContracts(map[string]any{
				"action_contracts": map[string]any{"attack_move": map[string]any{
					"target": descriptor,
				}},
				"legal_ids": legalIDs,
			})
			if err == nil || !strings.Contains(err.Error(), "target must be a string") {
				t.Fatalf("non-string target descriptor error = %v, want target string contract error", err)
			}
		})
	}
}

func herzogZweiAttackMoveContractObservation() map[string]any {
	return map[string]any{
		"action_contracts": map[string]any{
			"attack_move": map[string]any{"args": map[string]any{
				"unit_id": map[string]any{"type": "string", "required": true, "enum_ref": "own_units"},
				"to":      map[string]any{"type": "coordinate", "required": true, "bounds_ref": "coordinates"},
			}},
			"wait": map[string]any{},
		},
		"legal_ids": map[string]any{
			"own_units":   []string{"mio_commander"},
			"coordinates": map[string]any{"x_min": 0, "x_max": 23, "y_min": 0, "y_max": 23},
		},
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
