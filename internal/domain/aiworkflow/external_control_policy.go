package aiworkflow

import (
	"strings"
)

const (
	ExternalControlStatusAllowed       = "allowed"
	ExternalControlStatusNeedsApproval = "needs_approval"
	ExternalControlStatusBlocked       = "blocked"
)

type ExternalControlPolicy struct {
	AllowedActors    []string
	AllowedChannels  []string
	AllowedActions   []string
	ApprovalRequired []string
}

type ExternalControlRequest struct {
	Actor         string `json:"actor"`
	ChannelID     string `json:"channel_id"`
	Action        string `json:"action"`
	HumanApproved bool   `json:"human_approved"`
}

type ExternalControlDecision struct {
	Status           string   `json:"status"`
	RequiresApproval bool     `json:"requires_approval"`
	Reasons          []string `json:"reasons,omitempty"`
}

func EvaluateExternalControl(policy ExternalControlPolicy, req ExternalControlRequest) ExternalControlDecision {
	actor := strings.TrimSpace(req.Actor)
	channelID := strings.TrimSpace(req.ChannelID)
	action := strings.TrimSpace(req.Action)
	var reasons []string
	if actor == "" {
		reasons = append(reasons, "actor is required")
	}
	if channelID == "" {
		reasons = append(reasons, "channel_id is required")
	}
	if action == "" {
		reasons = append(reasons, "action is required")
	}
	if actor != "" && len(policy.AllowedActors) > 0 && !containsFold(policy.AllowedActors, actor) {
		reasons = append(reasons, "actor is not allowed")
	}
	if channelID != "" && len(policy.AllowedChannels) > 0 && !containsFold(policy.AllowedChannels, channelID) {
		reasons = append(reasons, "channel is not allowed")
	}
	if action != "" && len(policy.AllowedActions) > 0 && !containsFold(policy.AllowedActions, action) {
		reasons = append(reasons, "action is not allowed")
	}
	if len(reasons) > 0 {
		return ExternalControlDecision{Status: ExternalControlStatusBlocked, Reasons: reasons}
	}
	requiresApproval := containsFold(policy.ApprovalRequired, action)
	if requiresApproval && !req.HumanApproved {
		return ExternalControlDecision{
			Status:           ExternalControlStatusNeedsApproval,
			RequiresApproval: true,
			Reasons:          []string{"human approval is required for action"},
		}
	}
	return ExternalControlDecision{Status: ExternalControlStatusAllowed, RequiresApproval: requiresApproval}
}

func containsFold(values []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}
