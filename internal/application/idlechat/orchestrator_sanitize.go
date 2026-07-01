package idlechat

import (
	"strings"
	"unicode"
)

func sanitizeIdleResponse(s, topic string) string {
	return sanitizeIdleResponseForSpeaker(s, topic, "")
}

func sanitizeIdleResponseForSpeaker(s, topic, speaker string) string {
	out := strings.TrimSpace(extractVisibleLLMAnswer(s))
	if out == "" {
		return out
	}
	for _, marker := range []string{"<|channel", "channel>thought", "channel=analysis"} {
		if idx := strings.Index(strings.ToLower(out), strings.ToLower(marker)); idx >= 0 {
			out = strings.TrimSpace(out[:idx])
			break
		}
	}
	if strings.HasPrefix(out, "（話題:") {
		if idx := strings.Index(out, "）"); idx >= 0 && idx+len("）") < len(out) {
			out = strings.TrimSpace(out[idx+len("）"):])
		}
	}
	leaks := []string{
		"相手の発言として受ける",
		"相手の発言として受け、",
		"前に自分も触れた発言への応答として、",
		"前に自分も触れたように、",
		"要件:",
		"要件：",
	}
	for _, leak := range leaks {
		out = strings.ReplaceAll(out, leak, "")
	}
	out = selectIdleSpeakerLine(out, speaker)
	out = stripLeadingIdleSpeakerPrefix(out)
	out = promptLeakLineRe.ReplaceAllString(out, "")
	out = strings.TrimLeftFunc(out, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	})
	out = strings.ReplaceAll(out, "  ", " ")
	out = strings.TrimSpace(out)
	return out
}

func selectIdleSpeakerLine(out, speaker string) string {
	normalizedSpeaker := normalizeIdleSpeakerName(speaker)
	if normalizedSpeaker == "" || !strings.Contains(out, "\n") {
		return out
	}
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		lineSpeaker, body, ok := splitIdleSpeakerLine(line)
		if ok && lineSpeaker == normalizedSpeaker && strings.TrimSpace(body) != "" {
			return strings.TrimSpace(body)
		}
	}
	return out
}

func stripLeadingIdleSpeakerPrefix(out string) string {
	for {
		_, body, ok := splitIdleSpeakerLine(out)
		if !ok {
			return out
		}
		out = strings.TrimSpace(body)
	}
}

func splitIdleSpeakerLine(line string) (speaker, body string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return "", "", false
	}
	lower := strings.ToLower(trimmed)
	prefixes := []struct {
		raw     string
		speaker string
	}{
		{"assistant: [mio]:", "mio"},
		{"assistant: [mio]：", "mio"},
		{"assistant: [shiro]:", "shiro"},
		{"assistant: [shiro]：", "shiro"},
		{"assistant: mio:", "mio"},
		{"assistant: mio：", "mio"},
		{"assistant: shiro:", "shiro"},
		{"assistant: shiro：", "shiro"},
		{"[mio]:", "mio"},
		{"[mio]：", "mio"},
		{"[shiro]:", "shiro"},
		{"[shiro]：", "shiro"},
		{"mio]:", "mio"},
		{"mio]：", "mio"},
		{"shiro]:", "shiro"},
		{"shiro]：", "shiro"},
		{"mioさん:", "mio"},
		{"mio さん:", "mio"},
		{"shiroさん:", "shiro"},
		{"shiro さん:", "shiro"},
		{"みお:", "mio"},
		{"みお：", "mio"},
		{"ミオ:", "mio"},
		{"ミオ：", "mio"},
		{"しろ:", "shiro"},
		{"しろ：", "shiro"},
		{"シロ:", "shiro"},
		{"シロ：", "shiro"},
		{"mio:", "mio"},
		{"mio：", "mio"},
		{"shiro:", "shiro"},
		{"shiro：", "shiro"},
		{"assistant:", ""},
		{"assistant：", ""},
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, strings.ToLower(prefix.raw)) {
			return prefix.speaker, strings.TrimSpace(trimmed[len(prefix.raw):]), true
		}
	}
	return "", "", false
}

func normalizeIdleSpeakerName(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "mio", "みお", "ミオ":
		return "mio"
	case "shiro", "しろ", "シロ":
		return "shiro"
	default:
		return ""
	}
}

func sanitizeIdleSummaryResponse(raw, topic string) string {
	out := strings.TrimSpace(extractVisibleLLMAnswer(raw))
	if out == "" {
		return ""
	}
	out = dropLeadingReasoningParagraphs(out)
	if hasPromptLeak(out) || hasInternalReasoningLeak(out) {
		// 同一抽出器で再抽出（Final answer / 末尾日本語ブロック）
		out = strings.TrimSpace(extractVisibleLLMAnswer(out))
		out = dropLeadingReasoningParagraphs(out)
	}
	out = strings.TrimSpace(out)
	if out == "" || hasPromptLeak(out) || hasInternalReasoningLeak(out) || summaryLooksLikeEnglishMetaReasoning(out) {
		return ""
	}
	return out
}
