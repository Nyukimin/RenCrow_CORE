package tts

import (
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func canonicalRenCrowTTSTraceID(raw string) modulecore.TraceID {
	traceID := modulecore.TraceID(strings.TrimSpace(raw))
	if traceID.Validate() != nil {
		return modulecore.NewTraceID()
	}
	return traceID
}

func newRenCrowTTSSession(voiceID string) *renCrowTTSSession {
	return &renCrowTTSSession{
		voiceID: voiceID,
		traceID: modulecore.NewTraceID(),
	}
}

func (b *RenCrowTTSBridge) getOrCreateSession(sessionID string) *renCrowTTSSession {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.sessions[sessionID]; ok {
		return s
	}
	s := newRenCrowTTSSession(b.cfg.VoiceID)
	b.sessions[sessionID] = s
	return s
}

func (b *RenCrowTTSBridge) reserveSessionChunks(sessionID string, count int) (renCrowTTSSession, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	session, ok := b.sessions[sessionID]
	if !ok {
		session = newRenCrowTTSSession(b.cfg.VoiceID)
		b.sessions[sessionID] = session
	}
	first := session.nextChunk
	session.nextChunk += count
	return *session, first
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
