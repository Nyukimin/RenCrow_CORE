package idlechat

import (
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/session"
)

func latestOtherUtterance(entries []session.ConversationEntry, sessionID, speaker string) string {
	for i := len(entries) - 1; i >= 0; i-- {
		m := entries[i].Message
		if m.SessionID != sessionID || strings.EqualFold(m.From, speaker) {
			continue
		}
		return strings.TrimSpace(m.Content)
	}
	return ""
}

func latestSelfUtterance(entries []session.ConversationEntry, sessionID, speaker string) string {
	for i := len(entries) - 1; i >= 0; i-- {
		m := entries[i].Message
		if m.SessionID != sessionID || !strings.EqualFold(m.From, speaker) {
			continue
		}
		return strings.TrimSpace(m.Content)
	}
	return ""
}

func violatesAttribution(response, latestOther string) bool {
	resp := normalizeLoopText(response)
	other := normalizeLoopText(latestOther)
	if resp == "" || other == "" {
		return false
	}
	if textSimilarity(resp, other) < 0.93 {
		return false
	}
	lower := strings.ToLower(response)
	if strings.Contains(lower, "あなた") || strings.Contains(lower, "君") || strings.Contains(lower, "相手") || strings.Contains(lower, "その視点") {
		return false
	}
	return true
}
