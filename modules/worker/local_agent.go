package worker

import "strings"

type LocalAgentAvailability struct {
	Coder1 bool
	Coder2 bool
	Coder3 bool
	Coder4 bool
}

func LocalAgentEnabled(agentName string, availability LocalAgentAvailability) bool {
	switch strings.ToLower(strings.TrimSpace(agentName)) {
	case "mio", "shiro":
		return true
	case "coder1":
		return availability.Coder1
	case "coder2":
		return availability.Coder2
	case "coder3":
		return availability.Coder3
	case "coder4":
		return availability.Coder4
	default:
		return true
	}
}

func DistributedAgentAvailable(agentName string, hasLocalTransport, hasSSHTransport bool) bool {
	return strings.TrimSpace(agentName) != "" && (hasLocalTransport || hasSSHTransport)
}

func FormatAgentUnavailableReason(prefix string, err error) string {
	msg := strings.TrimSpace(prefix)
	if err == nil {
		return msg
	}
	detail := strings.TrimSpace(err.Error())
	if detail == "" {
		return msg
	}
	if msg == "" {
		return detail
	}
	return msg + ": " + detail
}

func LocalCoderReplyTarget(from string) string {
	if strings.EqualFold(strings.TrimSpace(from), "shiro") {
		return "mio"
	}
	return strings.TrimSpace(from)
}
