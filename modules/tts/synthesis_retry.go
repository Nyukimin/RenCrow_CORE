package tts

import (
	"encoding/json"
	"strings"
	"time"
)

func ShouldRetrySynthesis(code string, attempt int) bool {
	switch NormalizeSynthesisErrorCode(code) {
	case "ENGINE_UNAVAILABLE":
		return attempt < 2
	case "SYNTHESIS_FAILED":
		return attempt < 1
	default:
		return false
	}
}

func SynthesisBackoffForAttempt(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	base := 200 * time.Millisecond
	return time.Duration(1<<attempt) * base
}

func ParseSynthesisError(body []byte) (string, string) {
	var out struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", ""
	}
	return NormalizeSynthesisErrorCode(out.Error.Code), strings.TrimSpace(out.Error.Message)
}

func NormalizeSynthesisErrorCode(code string) string {
	code = strings.TrimSpace(strings.ToUpper(code))
	code = strings.ReplaceAll(code, "-", "_")
	return code
}

func ShouldRetrySynthesisTransportError(message string, attempt int) bool {
	if attempt >= 2 {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return false
	}
	return strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "client.timeout exceeded") ||
		strings.Contains(msg, "timeout")
}
