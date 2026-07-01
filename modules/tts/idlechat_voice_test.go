package tts

import (
	"testing"
	"time"
)

func TestIdleChatVoiceForSpeaker(t *testing.T) {
	voiceID, voiceProfile := IdleChatVoiceForSpeaker("shiro")
	if voiceID != IdleChatMaleVoiceID || voiceProfile != IdleChatMaleVoiceProfile {
		t.Fatalf("unexpected shiro voice mapping: %q %q", voiceID, voiceProfile)
	}
	voiceID, voiceProfile = IdleChatVoiceForSpeaker("mio")
	if voiceID != IdleChatDefaultVoiceID || voiceProfile != IdleChatDefaultVoiceProfile {
		t.Fatalf("unexpected mio voice mapping: %q %q", voiceID, voiceProfile)
	}
}

func TestNormalizeIdleChatCharacterID(t *testing.T) {
	tests := map[string]string{
		"しろ":   "shiro",
		"みお":   "mio",
		"ren":  "user",
		"user": "user",
	}
	for input, want := range tests {
		if got := NormalizeIdleChatCharacterID(input); got != want {
			t.Fatalf("NormalizeIdleChatCharacterID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIdleChatTimeOfDayAt(t *testing.T) {
	if got := IdleChatTimeOfDayAt(time.Date(2026, 5, 30, 5, 0, 0, 0, time.UTC)); got != "night" {
		t.Fatalf("5:00 = %q, want night", got)
	}
	if got := IdleChatTimeOfDayAt(time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)); got != "day" {
		t.Fatalf("12:00 = %q, want day", got)
	}
	if got := IdleChatTimeOfDayAt(time.Date(2026, 5, 30, 21, 0, 0, 0, time.UTC)); got != "night" {
		t.Fatalf("21:00 = %q, want night", got)
	}
}
