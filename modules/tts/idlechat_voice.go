package tts

import (
	"strings"
	"time"
)

const (
	IdleChatDefaultVoiceID      = "mio"
	IdleChatDefaultVoiceProfile = "lumina_female"
	IdleChatMaleVoiceID         = "male_01"
	IdleChatMaleVoiceProfile    = "lumina_male"
)

func IdleChatVoiceForSpeaker(speaker string) (voiceID, voiceProfile string) {
	switch NormalizeIdleChatCharacterID(speaker) {
	case "shiro":
		return IdleChatMaleVoiceID, IdleChatMaleVoiceProfile
	default:
		return IdleChatDefaultVoiceID, IdleChatDefaultVoiceProfile
	}
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
