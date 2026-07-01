package security

import "strings"

const (
	ChannelSourceDM    = "dm"
	ChannelSourceGroup = "group"

	ChannelDenyDMDisabled    = "dm_disabled"
	ChannelDenyGroupDisabled = "group_disabled"
	ChannelDenyUnknownSender = "unknown_sender"
	ChannelDenyUnpairedGroup = "unpaired_group"
)

// ChannelPolicy defines coarse access control for external chat channels.
type ChannelPolicy struct {
	AllowDM        bool
	AllowGroups    bool
	AllowedSenders []string
	PairedGroups   []string
}

type ChannelRequest struct {
	Channel    string
	SourceType string
	SenderID   string
	ChatID     string
}

type ChannelDecision struct {
	Allowed bool
	Reason  string
}

func (p ChannelPolicy) Evaluate(req ChannelRequest) ChannelDecision {
	sourceType := normalizeChannelSource(req.SourceType)
	if sourceType == ChannelSourceDM && !p.AllowDM {
		return ChannelDecision{Reason: ChannelDenyDMDisabled}
	}
	if sourceType == ChannelSourceGroup && !p.AllowGroups {
		return ChannelDecision{Reason: ChannelDenyGroupDisabled}
	}
	if len(p.AllowedSenders) > 0 && !containsString(p.AllowedSenders, req.SenderID) {
		return ChannelDecision{Reason: ChannelDenyUnknownSender}
	}
	if sourceType == ChannelSourceGroup && len(p.PairedGroups) > 0 && !containsString(p.PairedGroups, req.ChatID) {
		return ChannelDecision{Reason: ChannelDenyUnpairedGroup}
	}
	return ChannelDecision{Allowed: true}
}

func normalizeChannelSource(sourceType string) string {
	switch strings.TrimSpace(strings.ToLower(sourceType)) {
	case "group", "room":
		return ChannelSourceGroup
	default:
		return ChannelSourceDM
	}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == needle {
			return true
		}
	}
	return false
}
