package viewer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeGameAgentDecisionProvider struct {
	request  GameAgentDecisionRequest
	decision GameBrainDecision
	err      error
}

func (p *fakeGameAgentDecisionProvider) DecideGameTurn(_ context.Context, request GameAgentDecisionRequest) (GameBrainDecision, error) {
	p.request = request
	return p.decision, p.err
}

func TestHandleGameAgentDecisionUsesAgentOwnedDecision(t *testing.T) {
	provider := &fakeGameAgentDecisionProvider{decision: GameBrainDecision{
		AgentID:    "mio",
		Persona:    "mio",
		Intent:     "search",
		Reason:     "Agent decided to inspect the room",
		ActionPlan: []GameActionStep{{Action: "search"}},
		Confidence: 0.8,
	}}
	handler := HandleGameAgentDecision(provider)
	body := `{"game_id":"nethack","session_id":"nh_agent","turn":1,"persona":"mio","observation":{"depth":1},"available_actions":["search","rest"],"request":"choose_next_action"}`
	request := httptest.NewRequest(http.MethodPost, "/viewer/games/decision", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if provider.request.SessionID != "nh_agent" || !strings.Contains(recorder.Body.String(), `"agent_id":"mio"`) {
		t.Fatalf("Agent decision was not preserved: request=%+v body=%s", provider.request, recorder.Body.String())
	}
}

func TestHandleGameAgentDecisionRejectsNonAgentDecision(t *testing.T) {
	provider := &fakeGameAgentDecisionProvider{decision: GameBrainDecision{
		Persona:    "mio",
		Intent:     "search",
		ActionPlan: []GameActionStep{{Action: "search"}},
		Confidence: 1,
	}}
	handler := HandleGameAgentDecision(provider)
	body := `{"game_id":"nethack","session_id":"nh_rule","turn":0,"persona":"mio","available_actions":["search"],"request":"choose_next_action"}`
	request := httptest.NewRequest(http.MethodPost, "/viewer/games/decision", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "agent_id is required") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
