package tts

import (
	"strings"
)

func (b *RenCrowTTSBridge) getOrCreateSession(sessionID string) *renCrowTTSSession {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.sessions[sessionID]; ok {
		return s
	}
	s := &renCrowTTSSession{voiceID: b.cfg.VoiceID}
	b.sessions[sessionID] = s
	return s
}

func normalizeSynthesisURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	lower := strings.ToLower(base)
	if strings.HasSuffix(lower, "/api/tts") || strings.HasSuffix(lower, "/synthesis") || strings.HasSuffix(lower, "/synthesize") {
		return base
	}
	return base + "/api/tts"
}

func mediaBaseURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	lower := strings.ToLower(base)
	for _, suffix := range []string{"/api/tts", "/synthesis", "/synthesize"} {
		if strings.HasSuffix(lower, suffix) {
			return base[:len(base)-len(suffix)]
		}
	}
	return base
}
