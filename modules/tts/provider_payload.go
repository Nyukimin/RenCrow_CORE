package tts

import (
	"fmt"
	"strings"
)

var AllowedProviderParamKeys = map[string]struct{}{
	"model_name":     {},
	"model_file":     {},
	"speaker_id":     {},
	"speaker_name":   {},
	"style":          {},
	"style_weight":   {},
	"language":       {},
	"sdp_ratio":      {},
	"noise":          {},
	"noise_w":        {},
	"split_interval": {},
	"line_split":     {},
	"length":         {},
}

type SynthesisPayloadInput struct {
	Text           string
	DefaultVoiceID string
	Speed          float64
	Emotion        *EmotionState
	ProviderParams map[string]any
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
	if len(input.ProviderParams) > 0 {
		filtered, err := FilterProviderParams(input.ProviderParams)
		if err != nil {
			return nil, err
		}
		if len(filtered) > 0 {
			payload["provider_params"] = filtered
		}
	}
	return payload, nil
}

func FallbackVoiceID(defaultVoiceID string, emotion *EmotionState) string {
	if emotion != nil {
		switch strings.ToLower(strings.TrimSpace(emotion.ReasonTrace.VoiceProfile)) {
		case "lumina_male":
			return "male_01"
		case "lumina_female":
			return "female_01"
		}
	}
	return defaultVoiceID
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

func FilterProviderParams(in map[string]any) (map[string]any, error) {
	if in == nil {
		return nil, nil
	}
	out := make(map[string]any)
	for k, v := range in {
		if _, ok := AllowedProviderParamKeys[k]; !ok {
			return nil, fmt.Errorf("unknown provider_params key: %s", k)
		}
		normalized, err := NormalizeProviderParamValue(k, v)
		if err != nil {
			return nil, err
		}
		if k == "length" {
			f, ok := toFloat64(normalized)
			if !ok || f <= 0 {
				return nil, fmt.Errorf("provider_params.length must be > 0")
			}
		}
		out[k] = normalized
	}
	return out, nil
}

func BuildRequestIDHeader(sessionID string, chunkIndex int) string {
	prefix := SanitizeAudioPrefix(sessionID)
	if prefix == "" {
		prefix = "ttsreq"
	}
	return fmt.Sprintf("%s-%04d", prefix, chunkIndex)
}

func NormalizeProviderParamValue(key string, value any) (any, error) {
	switch key {
	case "model_name", "model_file", "speaker_name", "style", "language":
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("provider_params.%s must be string", key)
		}
		s = strings.TrimSpace(s)
		if key == "language" && !IsAllowedLanguage(s) {
			return nil, fmt.Errorf("provider_params.language must be one of JP/EN/ZH")
		}
		return s, nil
	case "line_split":
		if b, ok := value.(bool); ok {
			return b, nil
		}
		if s, ok := value.(string); ok {
			if b, parsed := ParseBoolLike(s); parsed {
				return b, nil
			}
		}
		return nil, fmt.Errorf("provider_params.line_split must be bool")
	case "speaker_id":
		if isNumeric(value) || isString(value) {
			return value, nil
		}
		return nil, fmt.Errorf("provider_params.speaker_id must be string or number")
	case "style_weight", "sdp_ratio", "noise", "noise_w", "split_interval", "length":
		if isNumeric(value) {
			return value, nil
		}
		return nil, fmt.Errorf("provider_params.%s must be number", key)
	default:
		return nil, fmt.Errorf("unknown provider_params key: %s", key)
	}
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

func ParseBoolLike(v string) (bool, bool) {
	s := strings.ToLower(strings.TrimSpace(v))
	switch s {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func IsAllowedLanguage(language string) bool {
	switch strings.ToUpper(strings.TrimSpace(language)) {
	case "JP", "JA", "EN", "ZH":
		return true
	default:
		return false
	}
}

func isString(v any) bool {
	_, ok := v.(string)
	return ok
}

func isNumeric(v any) bool {
	switch v.(type) {
	case int, int8, int16, int32, int64:
		return true
	case uint, uint8, uint16, uint32, uint64:
		return true
	case float32, float64:
		return true
	default:
		return false
	}
}

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}
