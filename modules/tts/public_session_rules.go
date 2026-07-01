package tts

import (
	"strconv"
	"strings"
)

func FormatFixed4(n int) string {
	if n < 0 {
		n = 0
	}
	if n < 10 {
		return "000" + string(rune('0'+n))
	}
	if n < 100 {
		return "00" + strconv.Itoa(n)
	}
	if n < 1000 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func ParseFixed4(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if len(value) != 4 {
		return 0, false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return n, true
}

func ParseTrailingResponseNumber(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	idx := strings.LastIndex(value, ":")
	if idx >= 0 {
		value = value[idx+1:]
	}
	return ParseFixed4(value)
}

func IsIdleChatPublicSession(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	return strings.HasPrefix(sessionID, "idle-") ||
		strings.HasPrefix(sessionID, "forecast-") ||
		strings.HasPrefix(sessionID, "story-") ||
		strings.HasPrefix(sessionID, "story-simple-")
}
