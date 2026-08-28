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
	for _, required := range []string{`"session_id":"nh_agent_e2e"`, `"search"`, `{"intent":"<action>"}`, "単一field", "Markdown、説明文は禁止"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q: %s", required, prompt)
		}
	}
	if strings.Contains(prompt, "\n  ") {
		t.Fatalf("game observation must use compact JSON to preserve context budget: %s", prompt)
	}
}

func TestDecodeAgentGameDecisionRejectsJSONFence(t *testing.T) {
	if _, err := decodeAgentGameDecision("```json\n{\"intent\":\"search\"}\n```"); err == nil {
		t.Fatal("CORE must reject decoder-specific JSON fences")
	}
}

func TestDecodeAgentGameDecisionRejectsNonJSONMarkdown(t *testing.T) {
	if _, err := decodeAgentGameDecision("result:\n```json\n{\"intent\":\"search\"}\n```"); err == nil {
		t.Fatal("expected prose outside the JSON fence to be rejected")
	}
}

func TestDecodeAgentGameDecisionAcceptsStrictJSON(t *testing.T) {
	decision, err := decodeAgentGameDecision(`{"intent":"search"}`)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decision.Intent != "search" {
		t.Fatalf("unexpected decision: %+v", decision)
	}
}

func TestDecodeAgentGameDecisionRejectsFieldsOutsideLLMResidual(t *testing.T) {
	if _, err := decodeAgentGameDecision(`{"intent":"search","reason":"model supplied"}`); err == nil {
		t.Fatal("game decision LLM residual must contain only intent")
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
