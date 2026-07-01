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
	if strings.HasSuffix(strings.ToLower(base), "/synthesis") {
		return base
	}
	return base + "/synthesis"
}

func mediaBaseURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(strings.ToLower(base), "/synthesis") {
		return strings.TrimSuffix(base, "/synthesis")
	}
	return base
}
