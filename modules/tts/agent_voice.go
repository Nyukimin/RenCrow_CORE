package tts

import "strings"

// CORE-managed Agent identities. Each name is also the public voice_id of the
// RenCrow_TTS Gateway, so the Agent that responds is the voice that speaks.
const (
	AgentMio    = "mio"
	AgentShiro  = "shiro"
	AgentMidori = "midori"
	AgentKuro   = "kuro"
)

const (
	agentVoiceProfileFemale = "lumina_female"
	agentVoiceProfileMale   = "lumina_male"
)

// VoiceForAgent is the single owner of the Agent -> Gateway voice identity
// mapping. voiceID is the Gateway's public voice_id (never a legacy alias such
// as male_01 / female_01); voiceProfile only steers emotion planning.
// Anything that is not one of the four CORE-managed Agents is rejected rather
// than defaulted, so a wrong voice never replaces a missing one.
func VoiceForAgent(agent string) (voiceID, voiceProfile string, ok bool) {
	switch normalized := strings.ToLower(strings.TrimSpace(agent)); normalized {
	case AgentMio, AgentMidori:
		return normalized, agentVoiceProfileFemale, true
	case AgentShiro, AgentKuro:
		return normalized, agentVoiceProfileMale, true
	default:
		return "", "", false
	}
}

// RespondingAgentForRoute returns the Agent whose output is spoken for a turn
// on route. It must equal the `from` of the turn's first agent.response
// (docs/06_Public_API仕様.md): CHAT is the viewer recipient (empty means the
// default recipient mio), PLAN / RESEARCH are mio, WILD is midori, ANALYZE is
// kuro and OPS / CODE* are shiro. The Mio relay that follows a delegated
// result carries the same text and is not a second utterance.
// recipient is consulted for CHAT only.
func RespondingAgentForRoute(route, recipient string) (agent string, ok bool) {
	switch normalizeRouteName(route) {
	case "CHAT":
		if strings.TrimSpace(recipient) == "" {
			return AgentMio, true
		}
		if _, _, known := VoiceForAgent(recipient); !known {
			return "", false
		}
		return strings.ToLower(strings.TrimSpace(recipient)), true
	case "PLAN", "RESEARCH":
		return AgentMio, true
	case "WILD":
		return AgentMidori, true
	case "ANALYZE":
		return AgentKuro, true
	case "OPS", "CODE", "CODE1", "CODE2", "CODE3", "CODE4":
		return AgentShiro, true
	default:
		return "", false
	}
}
