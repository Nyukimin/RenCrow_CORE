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

func TestFilterProviderParamsNormalizesAllowedValues(t *testing.T) {
	got, err := FilterProviderParams(map[string]any{
		"language":   " ja ",
		"line_split": "yes",
		"length":     1.2,
		"speaker_id": "mio",
	})
	if err != nil {
		t.Fatalf("FilterProviderParams() error = %v", err)
	}
	if got["language"] != "ja" || got["line_split"] != true || got["length"] != 1.2 || got["speaker_id"] != "mio" {
		t.Fatalf("FilterProviderParams() = %#v", got)
	}
}

func TestFilterProviderParamsRejectsUnknownKey(t *testing.T) {
	_, err := FilterProviderParams(map[string]any{"bad": true})
	if err == nil || err.Error() != "unknown provider_params key: bad" {
		t.Fatalf("FilterProviderParams() error = %v", err)
	}
}

func TestFilterProviderParamsRejectsInvalidLength(t *testing.T) {
	_, err := FilterProviderParams(map[string]any{"length": 0})
	if err == nil || err.Error() != "provider_params.length must be > 0" {
		t.Fatalf("FilterProviderParams() error = %v", err)
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
