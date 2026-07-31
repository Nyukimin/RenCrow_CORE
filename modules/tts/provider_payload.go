package tts

import (
	"fmt"
	"strings"
)

type SynthesisPayloadInput struct {
	Text           string
	DefaultVoiceID string
	Speed          float64
	Emotion        *EmotionState
}

func BuildSynthesisPayload(input SynthesisPayloadInput) (map[string]any, error) {
	payload := map[string]any{
		"text":     strings.TrimSpace(input.Text),
		"voice_id": FallbackVoiceID(input.DefaultVoiceID, input.Emotion),
	}
	if speed, ok := SpeechSpeed(input.Speed, input.Emotion); ok {
		if speed <= 0 {
			return nil, fmt.Errorf("speed must be > 0")
		}
		payload["speed"] = speed
	}
	if pitch, ok := SpeechPitch(input.Emotion); ok {
		payload["pitch"] = pitch
	}
	return payload, nil
}

func FallbackVoiceID(defaultVoiceID string, emotion *EmotionState) string {
	trimmedVoiceID := strings.TrimSpace(defaultVoiceID)
	switch strings.ToLower(trimmedVoiceID) {
	case "mio", "shiro", "midori", "kuro":
		return trimmedVoiceID
	}
	if emotion != nil {
		switch strings.ToLower(strings.TrimSpace(emotion.ReasonTrace.VoiceProfile)) {
		case "lumina_male":
			return "male_01"
		case "lumina_female":
			return "female_01"
		}
	}
	return trimmedVoiceID
}

func SpeechSpeed(speed float64, emotion *EmotionState) (float64, bool) {
	if speed > 0 {
		return speed, true
	}
	if emotion == nil || emotion.Prosody.Speed == 0 {
		return 0, false
	}
	return emotion.Prosody.Speed, true
}

func SpeechPitch(emotion *EmotionState) (float64, bool) {
	if emotion == nil {
		return 0, false
	}
	return emotion.Prosody.Pitch, true
}

func BuildRequestIDHeader(sessionID string, chunkIndex int) string {
	prefix := SanitizeAudioPrefix(sessionID)
	if prefix == "" {
		prefix = "ttsreq"
	}
	return fmt.Sprintf("%s-%04d", prefix, chunkIndex)
}

func SanitizeAudioPrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range prefix {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-_")
}
