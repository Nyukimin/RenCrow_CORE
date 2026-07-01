package tts

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	markdownImageRE       = regexp.MustCompile(`!\[([^\]]*)\]\([^)]+\)`)
	markdownLinkRE        = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	markdownHeadingRE     = regexp.MustCompile(`^\s{0,3}#{1,6}\s*`)
	markdownBlockquoteRE  = regexp.MustCompile(`^\s{0,3}>\s?`)
	markdownBulletRE      = regexp.MustCompile(`^\s*[-+*]\s+`)
	markdownOrderedListRE = regexp.MustCompile(`^\s*\d+[.)]\s+`)
	markdownTableRuleRE   = regexp.MustCompile(`^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)+\|?\s*$`)
)

// FormatTTSSpeechPlainText removes Markdown notation and normalizes bracket
// symbols for provider-facing speech text. Viewer display text is handled
// separately and should not use this as its source of truth.
func FormatTTSSpeechPlainText(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, "\r\n", "\n")
	trimmed = strings.ReplaceAll(trimmed, "\r", "\n")
	trimmed = markdownImageRE.ReplaceAllString(trimmed, "$1")
	trimmed = markdownLinkRE.ReplaceAllString(trimmed, "$1")
	trimmed = strings.ReplaceAll(trimmed, "```", " ")
	trimmed = strings.ReplaceAll(trimmed, "`", "")
	trimmed = strings.ReplaceAll(trimmed, "**", "")
	trimmed = strings.ReplaceAll(trimmed, "__", "")
	trimmed = strings.ReplaceAll(trimmed, "~~", "")
	trimmed = strings.ReplaceAll(trimmed, "*", "")
	trimmed = strings.ReplaceAll(trimmed, "_", "")
	trimmed = strings.ReplaceAll(trimmed, "|", " ")

	lines := strings.Split(trimmed, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || isMarkdownRuleLine(line) || markdownTableRuleRE.MatchString(line) {
			continue
		}
		line = markdownHeadingRE.ReplaceAllString(line, "")
		line = markdownBlockquoteRE.ReplaceAllString(line, "")
		line = markdownBulletRE.ReplaceAllString(line, "")
		line = markdownOrderedListRE.ReplaceAllString(line, "")
		line = normalizeSpeechBrackets(line)
		line = normalizeSpeechIcons(line)
		line = compactSpeechWhitespace(line)
		if line != "" {
			kept = append(kept, line)
		}
	}
	return strings.TrimSpace(strings.Join(kept, " "))
}

func isMarkdownRuleLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < 3 {
		return false
	}
	var marker rune
	count := 0
	for _, r := range trimmed {
		if unicode.IsSpace(r) {
			continue
		}
		if r != '-' && r != '*' && r != '_' {
			return false
		}
		if marker == 0 {
			marker = r
		}
		if r != marker {
			return false
		}
		count++
	}
	return count >= 3
}

func normalizeSpeechBrackets(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch r {
		case '「', '」':
			b.WriteRune(r)
		case '『', '【', '（', '(', '[', '［', '{', '｛', '〔', '〈', '《', '<':
			b.WriteRune('「')
		case '』', '】', '）', ')', ']', '］', '}', '｝', '〕', '〉', '》', '>':
			b.WriteRune('」')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func normalizeSpeechIcons(text string) string {
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	prefixes := make([]string, 0, 2)
	for {
		if trimmed == "" {
			return strings.Join(prefixes, "")
		}
		icon, rest, ok := splitLeadingSpeechIconCluster(trimmed)
		if !ok {
			break
		}
		if isAllowedSpeechIcon(icon) && len(prefixes) < 2 {
			prefixes = append(prefixes, icon)
		}
		trimmed = strings.TrimLeftFunc(rest, unicode.IsSpace)
	}
	cleaned := strings.TrimSpace(removeSpeechIcons(trimmed))
	prefixText := strings.Join(prefixes, "")
	if prefixText == "" {
		return cleaned
	}
	if cleaned == "" {
		return prefixText
	}
	return prefixText + " " + cleaned
}

func isAllowedSpeechIcon(icon string) bool {
	for _, item := range EmotionEmojiPaletteItems {
		if item.Emoji == icon {
			return true
		}
	}
	return false
}

func removeSpeechIcons(text string) string {
	var b strings.Builder
	for offset := 0; offset < len(text); {
		if _, rest, ok := splitLeadingSpeechIconCluster(text[offset:]); ok {
			offset = len(text) - len(rest)
			continue
		}
		r, size := utf8.DecodeRuneInString(text[offset:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		b.WriteRune(r)
		offset += size
	}
	return b.String()
}

func splitLeadingSpeechIconCluster(text string) (icon, rest string, ok bool) {
	if text == "" {
		return "", "", false
	}
	end := 0
	for i, r := range text {
		if i != 0 {
			break
		}
		if !isSpeechIconRune(r) {
			return "", text, false
		}
		end = i + utf8.RuneLen(r)
	}
	joinNext := false
	for end < len(text) {
		r, size := runeAt(text, end)
		switch {
		case r == '\u200d':
			joinNext = true
			end += size
		case joinNext:
			joinNext = false
			end += size
		case isSpeechIconSuffixRune(r):
			end += size
		default:
			return text[:end], text[end:], true
		}
	}
	return text[:end], text[end:], true
}

func runeAt(text string, offset int) (rune, int) {
	return utf8.DecodeRuneInString(text[offset:])
}

func isSpeechIconRune(r rune) bool {
	return unicode.Is(unicode.So, r) || unicode.Is(unicode.Sk, r)
}

func isSpeechIconSuffixRune(r rune) bool {
	return r == '\ufe0e' || r == '\ufe0f' || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Sk, r)
}

func compactSpeechWhitespace(text string) string {
	return strings.Join(strings.FieldsFunc(text, unicode.IsSpace), " ")
}
