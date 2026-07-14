package agentprofile

import (
	"context"
	"testing"

	domainagentprofile "github.com/Nyukimin/RenCrow_CORE/internal/domain/agentprofile"
)

type policyDecisionStore struct {
	items []domainagentprofile.PolicyDecision
}

func (s *policyDecisionStore) SaveAgentPolicyDecision(_ context.Context, item domainagentprofile.PolicyDecision) error {
	s.items = append(s.items, item)
	return nil
}

func (s *policyDecisionStore) ListAgentPolicyDecisions(context.Context, int) ([]domainagentprofile.PolicyDecision, error) {
	return append([]domainagentprofile.PolicyDecision(nil), s.items...), nil
}

func TestPolicyServiceDecideUsesAutonomyEnvelopePrecedence(t *testing.T) {
	service := NewPolicyService(NewStaticCatalog())

	tests := []struct {
		action string
		want   string
	}{
		{action: "ask_advisor", want: DecisionAllowed},
		{action: "git_push", want: DecisionApprovalRequired},
		{action: "expose_secret", want: DecisionForbidden},
		{action: "unknown_action", want: DecisionForbidden},
	}
	for _, tt := range tests {
		decision, err := service.Decide("shiro", tt.action)
		if err != nil {
			t.Fatalf("Decide(%q) failed: %v", tt.action, err)
		}
		if decision.Decision != tt.want {
			t.Fatalf("Decide(%q) = %q, want %q", tt.action, decision.Decision, tt.want)
		}
	}
}

func TestPolicyServiceDecideRejectsUnknownAgent(t *testing.T) {
	service := NewPolicyService(NewStaticCatalog())
	if _, err := service.Decide("unknown", "ask_advisor"); err == nil {
		t.Fatal("expected unknown agent error")
	}
}

func TestPolicyServiceDecideRecordsTrace(t *testing.T) {
	store := &policyDecisionStore{}
	service := NewPolicyService(NewStaticCatalog()).WithStore(store)
	decision, err := service.Decide("shiro", "ask_advisor")
	if err != nil {
		t.Fatalf("Decide failed: %v", err)
	}
	if len(store.items) != 1 || store.items[0].DecisionID != decision.DecisionID {
		t.Fatalf("policy decision was not recorded: %#v", store.items)
	}
}
