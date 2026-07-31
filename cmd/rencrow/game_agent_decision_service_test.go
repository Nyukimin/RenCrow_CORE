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
	for _, required := range []string{`"session_id": "nh_agent_e2e"`, `"search"`, `"intent"`, "Markdownや説明文は禁止"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q: %s", required, prompt)
		}
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
	decision, err := decodeAgentGameDecision(`{"intent":"search","reason":"周囲を確認する","action_plan":[{"action":"search"}],"memory_refs":[],"confidence":0.8}`)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decision.Intent != "search" || decision.Confidence != 0.8 {
		t.Fatalf("unexpected decision: %+v", decision)
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
