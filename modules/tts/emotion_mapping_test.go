package tts

import "testing"

func TestBuildEmotionProviderReason(t *testing.T) {
	got := BuildEmotionProviderReason(&EmotionState{
		VoiceProfile: "mio",
		Prosody:      Prosody{Speed: 1.1},
		Metadata:     map[string]any{"source": "test"},
	})
	if got["voice_profile"] != "mio" {
		t.Fatalf("voice_profile was not mapped: %+v", got)
	}
	if got["metadata"].(map[string]any)["source"] != "test" {
		t.Fatalf("metadata was not mapped: %+v", got)
	}
}

func TestBuildEmotionProviderReasonNil(t *testing.T) {
	if got := BuildEmotionProviderReason(nil); got != nil {
		t.Fatalf("expected nil reason, got %+v", got)
	}
}
