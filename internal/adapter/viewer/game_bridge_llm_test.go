package viewer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
)

func TestLLMGameDecisionGeneratorBuildsPromptAndParsesDecision(t *testing.T) {
	provider := &fakeGameDecisionLLMProvider{
		content: `{"persona":"mio","intent":"drink","reason":"thirst is visible","action_plan":[{"action":"drink"}],"memory_refs":[],"confidence":0.81}`,
	}
	generator := NewLLMGameDecisionGenerator(provider)
	generator.Timeout = 50 * time.Millisecond
	request := httptest.NewRequest(http.MethodPost, "/viewer/games/decision", nil)

	decision, err := generator.GenerateGameDecision(request, GameObservationRequest{
		GameID:           "survival_garden",
		SessionID:        "sg_test",
		Turn:             2,
		Persona:          "mio",
		Observation:      map[string]any{"status": map[string]any{"thirst": 72}},
		AvailableActions: []string{"drink", "rest"},
		Request:          "choose_next_action",
	}, []GameBridgeEvent{{
		EventID:           "game:survival_garden:sg_test:turn_1",
		CandidateMemoryID: "game:survival_garden:sg_test:turn_1:candidate",
		Turn:              1,
		MemoryState:       "candidate",
		Result:            map[string]any{"success": true},
	}})
	if err != nil {
		t.Fatalf("GenerateGameDecision returned error: %v", err)
	}
	if decision.Intent != "drink" || decision.Confidence != 0.81 {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	if !provider.deadlineSet {
		t.Fatal("expected generator to set a timeout on provider context")
	}
	if !strings.Contains(provider.request.SystemPrompt, "mio") {
		t.Fatalf("system prompt does not include persona: %q", provider.request.SystemPrompt)
	}
	userContent := provider.request.Messages[0].Content
	for _, want := range []string{`"available_actions"`, `"drink"`, `"candidate_memory_id"`} {
		if !strings.Contains(userContent, want) {
			t.Fatalf("prompt missing %q: %s", want, userContent)
		}
	}
	if provider.request.ProviderOptions["surface"] != "game_bridge" {
		t.Fatalf("provider options not set: %+v", provider.request.ProviderOptions)
	}
}

func TestLLMGameDecisionGeneratorRejectsNonStrictJSON(t *testing.T) {
	provider := &fakeGameDecisionLLMProvider{
		content: `{"persona":"mio","intent":"drink","reason":"ok","action_plan":[{"action":"drink"}],"confidence":0.7} trailing`,
	}
	generator := NewLLMGameDecisionGenerator(provider)
	request := httptest.NewRequest(http.MethodPost, "/viewer/games/decision", nil)

	_, err := generator.GenerateGameDecision(request, GameObservationRequest{
		GameID:           "survival_garden",
		SessionID:        "sg_test",
		Turn:             2,
		Persona:          "mio",
		Observation:      map[string]any{"status": map[string]any{"thirst": 72}},
		AvailableActions: []string{"drink", "rest"},
		Request:          "choose_next_action",
	}, nil)

	var generationErr GameDecisionGenerationError
	if !errors.As(err, &generationErr) {
		t.Fatalf("error=%v want GameDecisionGenerationError", err)
	}
	if generationErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("StatusCode=%d want %d", generationErr.StatusCode, http.StatusBadGateway)
	}
}

func TestValidateGameDecisionRejectsInvalidConfidence(t *testing.T) {
	err := validateGameDecision(GameBrainDecision{
		Persona:    "mio",
		Intent:     "drink",
		ActionPlan: []GameActionStep{{Action: "drink"}},
		Confidence: 1.2,
	}, []string{"drink"})
	if err == nil {
		t.Fatal("expected invalid confidence to fail")
	}
}

type fakeGameDecisionLLMProvider struct {
	content     string
	request     llm.GenerateRequest
	deadlineSet bool
}

func (p *fakeGameDecisionLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.request = req
	_, p.deadlineSet = ctx.Deadline()
	return llm.GenerateResponse{Content: p.content}, nil
}

func (p *fakeGameDecisionLLMProvider) Name() string {
	return "fake-game-decision"
}
