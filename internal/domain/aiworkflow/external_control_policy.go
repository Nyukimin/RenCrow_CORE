package aiworkflow

import (
	"strings"
)

const (
	ExternalControlStatusAllowed = "allowed"
	ExternalControlStatusBlocked = "blocked"
)

type ExternalControlPolicy struct {
	AllowedActors   []string
	AllowedChannels []string
	AllowedActions  []string
}

type ExternalControlRequest struct {
	Actor     string `json:"actor"`
	ChannelID string `json:"channel_id"`
	Action    string `json:"action"`
}

type ExternalControlDecision struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons,omitempty"`
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
	return ExternalControlDecision{Status: ExternalControlStatusAllowed}
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
