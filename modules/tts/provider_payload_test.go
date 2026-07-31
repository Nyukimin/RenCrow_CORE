package tts

import "testing"

func TestBuildSynthesisPayloadUsesEmotionVoiceAndProsody(t *testing.T) {
	got, err := BuildSynthesisPayload(SynthesisPayloadInput{
		Text:           " hello ",
		DefaultVoiceID: "default",
		Speed:          1.2,
		Emotion: &EmotionState{
			ReasonTrace: ReasonTrace{VoiceProfile: "lumina_male"},
			Prosody:     Prosody{Speed: 1.1, Pitch: -0.2},
		},
	})
	if err != nil {
		t.Fatalf("BuildSynthesisPayload() error = %v", err)
	}
	if got["text"] != "hello" || got["voice_id"] != "male_01" || got["speed"] != 1.2 || got["pitch"] != -0.2 {
		t.Fatalf("BuildSynthesisPayload() = %#v", got)
	}
}

func TestBuildSynthesisPayloadKeepsExplicitAgentVoice(t *testing.T) {
	for _, voiceID := range []string{"mio", "shiro", "midori", "kuro"} {
		t.Run(voiceID, func(t *testing.T) {
			got, err := BuildSynthesisPayload(SynthesisPayloadInput{
				Text:           "hello",
				DefaultVoiceID: voiceID,
				Emotion: &EmotionState{
					ReasonTrace: ReasonTrace{VoiceProfile: "lumina_female"},
				},
			})
			if err != nil {
				t.Fatalf("BuildSynthesisPayload() error = %v", err)
			}
			if got["voice_id"] != voiceID {
				t.Fatalf("voice_id = %q, want explicit Agent voice %q", got["voice_id"], voiceID)
			}
		})
	}
}

func TestBuildSynthesisPayloadRejectsInvalidSpeed(t *testing.T) {
	_, err := BuildSynthesisPayload(SynthesisPayloadInput{
		Text:           "hello",
		DefaultVoiceID: "default",
		Emotion:        &EmotionState{Prosody: Prosody{Speed: -0.1}},
	})
	if err == nil || err.Error() != "speed must be > 0" {
		t.Fatalf("BuildSynthesisPayload() error = %v", err)
	}
}

func TestBuildSynthesisPayloadUsesExplicitSpeedOverride(t *testing.T) {
	got, err := BuildSynthesisPayload(SynthesisPayloadInput{
		Text:           "hello",
		DefaultVoiceID: "default",
		Speed:          1.2,
		Emotion:        &EmotionState{Prosody: Prosody{Speed: 0.6}},
	})
	if err != nil {
		t.Fatalf("BuildSynthesisPayload() error = %v", err)
	}
	if got["speed"] != 1.2 {
		t.Fatalf("BuildSynthesisPayload() = %#v", got)
	}
}

func TestBuildRequestIDHeaderSanitizesPrefix(t *testing.T) {
	if got := BuildRequestIDHeader(" idle/日本語_01 ", 3); got != "idle_01-0003" {
		t.Fatalf("BuildRequestIDHeader() = %q", got)
	}
	if got := BuildRequestIDHeader(" 日本語 ", 2); got != "ttsreq-0002" {
		t.Fatalf("BuildRequestIDHeader() fallback = %q", got)
	}
}
