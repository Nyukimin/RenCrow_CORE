package main

import (
	"time"

	moduletts "github.com/Nyukimin/picoclaw_multiLLM/modules/tts"
)

func idleChatVoiceForSpeaker(speaker string) (voiceID, voiceProfile string) {
	return moduletts.IdleChatVoiceForSpeaker(speaker)
}

func normalizeIdleChatCharacterID(speaker string) string {
	return moduletts.NormalizeIdleChatCharacterID(speaker)
}

func idleChatTimeOfDay() string {
	return moduletts.IdleChatTimeOfDayAt(time.Now())
}
