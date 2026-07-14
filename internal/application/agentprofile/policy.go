package agentprofile

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	domainagentprofile "github.com/Nyukimin/RenCrow_CORE/internal/domain/agentprofile"
)

const (
	DecisionAllowed          = domainagentprofile.PolicyAllowed
	DecisionApprovalRequired = domainagentprofile.PolicyApprovalRequired
	DecisionForbidden        = domainagentprofile.PolicyForbidden
)

type PolicyService struct {
	catalog *Catalog
	now     func() time.Time
	store   PolicyDecisionStore
}

type PolicyDecisionStore interface {
	SaveAgentPolicyDecision(ctx context.Context, item domainagentprofile.PolicyDecision) error
	ListAgentPolicyDecisions(ctx context.Context, limit int) ([]domainagentprofile.PolicyDecision, error)
}

func NewPolicyService(catalog *Catalog) *PolicyService {
	return &PolicyService{catalog: catalog, now: time.Now}
}

func (s *PolicyService) WithStore(store PolicyDecisionStore) *PolicyService {
	s.store = store
	return s
}

func (s *PolicyService) Decide(agentID string, action string) (domainagentprofile.PolicyDecision, error) {
	if s == nil || s.catalog == nil {
		return domainagentprofile.PolicyDecision{}, fmt.Errorf("agent profile catalog is required")
	}
	agentID = strings.ToLower(strings.TrimSpace(agentID))
	action = strings.ToLower(strings.TrimSpace(action))
	profile, ok := s.catalog.Get(agentID)
	if !ok {
		return domainagentprofile.PolicyDecision{}, fmt.Errorf("agent profile %q is not registered", agentID)
	}
	if action == "" {
		return domainagentprofile.PolicyDecision{}, fmt.Errorf("action is required")
	}
	decision := domainagentprofile.PolicyForbidden
	reason := "action is not declared in the autonomy envelope"
	switch {
	case profile.AutonomyEnvelope.IsForbidden(action):
		reason = "action is explicitly forbidden"
	case profile.AutonomyEnvelope.RequiresApproval(action):
		decision = domainagentprofile.PolicyApprovalRequired
		reason = "action requires human approval"
	case profile.AutonomyEnvelope.CanDecide(action) || profile.AutonomyEnvelope.CanAct(action):
		decision = domainagentprofile.PolicyAllowed
		reason = "action is allowed by the autonomy envelope"
	}
	result := domainagentprofile.PolicyDecision{
		DecisionID: uuid.NewString(),
		AgentID:    agentID,
		Action:     action,
		Decision:   decision,
		Reason:     reason,
		CreatedAt:  s.now().UTC(),
	}
	if err := result.Validate(); err != nil {
		return domainagentprofile.PolicyDecision{}, err
	}
	if s.store != nil {
		if err := s.store.SaveAgentPolicyDecision(context.Background(), result); err != nil {
			log.Printf("WARN: failed to record agent policy decision: %v", err)
		}
	}
	return result, nil
}
