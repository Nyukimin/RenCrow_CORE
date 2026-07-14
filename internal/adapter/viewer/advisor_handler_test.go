package viewer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	advisorapp "github.com/Nyukimin/RenCrow_CORE/internal/application/advisor"
	agentprofileapp "github.com/Nyukimin/RenCrow_CORE/internal/application/agentprofile"
	domainadvisor "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	domainagentprofile "github.com/Nyukimin/RenCrow_CORE/internal/domain/agentprofile"
)

type advisorHandlerStore struct {
	runs      []domainadvisor.AdviceRunRecord
	scores    []domainadvisor.AdvisorScoreSnapshot
	decisions []domainagentprofile.PolicyDecision
}

func (s *advisorHandlerStore) ListAdviceRuns(context.Context, int) ([]domainadvisor.AdviceRunRecord, error) {
	return s.runs, nil
}

func (s *advisorHandlerStore) ListAdvisorScoreSnapshots(context.Context, int) ([]domainadvisor.AdvisorScoreSnapshot, error) {
	return s.scores, nil
}

func (s *advisorHandlerStore) ListAgentPolicyDecisions(context.Context, int) ([]domainagentprofile.PolicyDecision, error) {
	return s.decisions, nil
}

func TestHandleAdvisorsStatusReturnsSafeSummary(t *testing.T) {
	now := time.Now().UTC()
	store := &advisorHandlerStore{
		runs: []domainadvisor.AdviceRunRecord{{
			RunID: "run-1", AdvisorID: domainadvisor.AdvisorCodex,
			RequestedByAgent: "shiro", ApprovalMode: "advice_only",
			Status:  domainadvisor.AdviceStatus(domainadvisor.StatusCompleted),
			Summary: "safe summary", PromptHash: "prompt-hash", OutputHash: "output-hash",
		}},
		scores: []domainadvisor.AdvisorScoreSnapshot{{
			SnapshotID: "score-1", AdvisorID: domainadvisor.AdvisorCodex, Score: 0.8, CreatedAt: now,
		}},
	}
	registry := advisorapp.NewService(advisorapp.NewCodexToolAdvisor(nil))
	req := httptest.NewRequest(http.MethodGet, "/viewer/advisors?limit=10", nil)
	rec := httptest.NewRecorder()
	HandleAdvisorsStatus(AdvisorStatusOptions{
		Store: store, AdvisorProfiles: registry.Profiles(), AgentProfiles: agentprofileapp.NewStaticCatalog().List(),
	}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, required := range []string{"\"advisor_count\":1", "\"recent_run_count\":1", "\"profile_count\":8"} {
		if !strings.Contains(body, required) {
			t.Fatalf("response missing %s: %s", required, body)
		}
	}
	for _, forbidden := range []string{"raw_output", "secret prompt body"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked forbidden value %q: %s", forbidden, body)
		}
	}
}

func TestHandleAdvisorsStatusUnavailableIsWarningNot500(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/advisors", nil)
	rec := httptest.NewRecorder()
	HandleAdvisorsStatus(AdvisorStatusOptions{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "unavailable") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAgentProfilesAndPolicyDecisions(t *testing.T) {
	profiles := agentprofileapp.NewStaticCatalog().List()
	store := &advisorHandlerStore{decisions: []domainagentprofile.PolicyDecision{{
		DecisionID: "decision-1", AgentID: "shiro", Action: "ask_advisor",
		Decision: domainagentprofile.PolicyAllowed, Reason: "allowed", CreatedAt: time.Now().UTC(),
	}}}

	profileRec := httptest.NewRecorder()
	HandleAgentProfiles(profiles).ServeHTTP(profileRec, httptest.NewRequest(http.MethodGet, "/viewer/agents/profiles", nil))
	if profileRec.Code != http.StatusOK || !strings.Contains(profileRec.Body.String(), "\"profile_count\":8") {
		t.Fatalf("profile status=%d body=%s", profileRec.Code, profileRec.Body.String())
	}

	decisionRec := httptest.NewRecorder()
	HandleAgentPolicyDecisions(store).ServeHTTP(decisionRec, httptest.NewRequest(http.MethodGet, "/viewer/agents/policy-decisions", nil))
	if decisionRec.Code != http.StatusOK || !strings.Contains(decisionRec.Body.String(), "decision-1") {
		t.Fatalf("decision status=%d body=%s", decisionRec.Code, decisionRec.Body.String())
	}
}
