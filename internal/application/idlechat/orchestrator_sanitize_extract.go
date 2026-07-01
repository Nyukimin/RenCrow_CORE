package idlechat

import (
	"regexp"
	"strings"
	"unicode"
)

func extractVisibleLLMAnswer(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	finalMarkers := []string{
		"<|channel|>final",
		"<|channel>final",
		"channel>final",
		"channel=final",
	}
	for _, marker := range finalMarkers {
		if idx := strings.LastIndex(lower, marker); idx >= 0 {
			return trimHarmonyTail(strings.TrimSpace(s[idx+len(marker):]))
		}
	}
	if strings.Contains(lower, "<|channel") || strings.Contains(lower, "channel>thought") || strings.Contains(lower, "channel=analysis") {
		return ""
	}
	if extracted := extractFinalAnswerBlock(s); extracted != "" {
		return trimHarmonyTail(extracted)
	}
	if hasInternalReasoningLeak(s) {
		if extracted := extractQuotedJapaneseDialogueCandidate(s); extracted != "" {
			return trimHarmonyTail(extracted)
		}
	}
	if extracted := extractTrailingJapaneseDialogueBlock(s); extracted != "" {
		return trimHarmonyTail(extracted)
	}
	return trimHarmonyTail(s)
}

func extractFinalAnswerBlock(s string) string {
	type marker struct {
		raw        string
		allowColon bool
	}
	markers := []marker{
		{raw: "final answer", allowColon: true},
		{raw: "final response", allowColon: true},
		{raw: "answer", allowColon: true},
		{raw: "最終回答", allowColon: true},
		{raw: "最終返答", allowColon: true},
		{raw: "回答", allowColon: true},
	}
	lower := strings.ToLower(s)
	for _, m := range markers {
		idx := strings.LastIndex(lower, strings.ToLower(m.raw))
		if idx < 0 {
			continue
		}
		start := idx + len(m.raw)
		tail := strings.TrimSpace(s[start:])
		if m.allowColon {
			tail = strings.TrimLeftFunc(tail, func(r rune) bool {
				return r == ':' || r == '：' || unicode.IsSpace(r)
			})
		}
		if looksLikeDialogueBody(tail) {
			return tail
		}
	}
	return ""
}

func extractTrailingJapaneseDialogueBlock(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return ""
	}
	blocks := strings.Split(trimmed, "\n\n")
	for i := len(blocks) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(blocks[i])
		if candidate == "" {
			continue
		}
		if looksLikeDialogueBody(candidate) {
			return candidate
		}
	}
	return ""
}

func extractQuotedJapaneseDialogueCandidate(s string) string {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`「([^「」]{12,240})」`),
		regexp.MustCompile(`“([^“”]{12,240})”`),
		regexp.MustCompile(`"([^"]{12,240})"`),
	}
	for _, re := range patterns {
		matches := re.FindAllStringSubmatch(s, -1)
		for i := len(matches) - 1; i >= 0; i-- {
			candidate := strings.TrimSpace(matches[i][1])
			if looksLikeDialogueBody(candidate) {
				return candidate
			}
		}
	}
	return ""
}

func looksLikeDialogueBody(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if hasPromptLeak(s) || hasInternalReasoningLeak(s) {
		return false
	}
	if !hasIdleSentenceEnd(s) {
		return false
	}
	hasKana := false
	for _, r := range s {
		if unicode.In(r, unicode.Hiragana, unicode.Katakana) {
			hasKana = true
			break
		}
	}
	return hasKana
}

func trimHarmonyTail(s string) string {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	for _, marker := range []string{"<|end|>", "<|return|>", "<|message|>", "<|endoftext|>"} {
		if idx := strings.Index(lower, marker); idx >= 0 {
			s = strings.TrimSpace(s[:idx])
			lower = strings.ToLower(s)
		}
	}
	return s
}
