package main

import (
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
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

func TestGameAgentDecisionServiceRejectsUnmigratedTitle(t *testing.T) {
	service := &gameAgentDecisionService{}
	_, err := service.DecideGameTurn(t.Context(), viewer.GameAgentDecisionRequest{
		GameID:           "survival_garden",
		SessionID:        "sg_not_agent_e2e",
		Persona:          "mio",
		AvailableActions: []string{"rest"},
		Request:          "choose_next_action",
	})
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("expected unmigrated title rejection, got %v", err)
	}
}

func TestGameAgentDecisionServiceDerivesExecutionFieldsFromValidatedIntent(t *testing.T) {
	decision := completeAgentGameDecision("mio", viewer.GameBrainDecision{Intent: "search"})
	if decision.Reason == "" || len(decision.ActionPlan) != 1 || decision.ActionPlan[0].Action != "search" {
		t.Fatalf("derived decision fields are incomplete: %+v", decision)
	}
}
