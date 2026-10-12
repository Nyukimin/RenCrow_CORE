package tts

import (
	"strings"
	"time"
)

const (
	IdleChatDefaultVoiceID      = "mio"
	IdleChatDefaultVoiceProfile = "lumina_female"
	IdleChatShiroVoiceID        = "shiro"
	IdleChatMaleVoiceProfile    = "lumina_male"
)

// IdleChatVoiceForSpeaker returns the voice of a CORE-managed Agent speaker.
// IdleChat scripts only carry Agent speakers; any other speaker keeps this
// path's existing mio default (it never reaches Agent-response TTS).
func IdleChatVoiceForSpeaker(speaker string) (voiceID, voiceProfile string) {
	if agentVoiceID, agentProfile, ok := VoiceForAgent(NormalizeIdleChatCharacterID(speaker)); ok {
		return agentVoiceID, agentProfile
	}
	return IdleChatDefaultVoiceID, IdleChatDefaultVoiceProfile
}

func NormalizeIdleChatCharacterID(speaker string) string {
	switch strings.ToLower(strings.TrimSpace(speaker)) {
	case "shiro", "しろ":
		return "shiro"
	case "mio", "みお":
		return "mio"
	case "れん", "ren", "user":
		return "user"
	default:
		return strings.ToLower(strings.TrimSpace(speaker))
	}
}

func IdleChatTimeOfDayAt(t time.Time) string {
	hour := t.Hour()
	if hour < 6 || hour >= 21 {
		return "night"
	}
	return "day"
}
