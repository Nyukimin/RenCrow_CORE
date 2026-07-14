package agentprofile

import (
	"fmt"
	"strings"
	"time"
)

const (
	PolicyAllowed          = "allowed"
	PolicyApprovalRequired = "approval_required"
	PolicyForbidden        = "forbidden"
)

type PolicyDecision struct {
	DecisionID string    `json:"decision_id"`
	AgentID    string    `json:"agent_id"`
	Action     string    `json:"action"`
	Decision   string    `json:"decision"`
	Reason     string    `json:"reason"`
	CreatedAt  time.Time `json:"created_at"`
}

type AgentPolicyDecision = PolicyDecision

func (d PolicyDecision) Validate() error {
	if strings.TrimSpace(d.DecisionID) == "" || strings.TrimSpace(d.AgentID) == "" || strings.TrimSpace(d.Action) == "" {
		return fmt.Errorf("decision_id, agent_id, and action are required")
	}
	switch d.Decision {
	case PolicyAllowed, PolicyApprovalRequired, PolicyForbidden:
	default:
		return fmt.Errorf("unsupported policy decision %q", d.Decision)
	}
	if strings.TrimSpace(d.Reason) == "" || d.CreatedAt.IsZero() {
		return fmt.Errorf("reason and created_at are required")
	}
	return nil
}
