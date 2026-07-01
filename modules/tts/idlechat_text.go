package tts

import (
	"regexp"
	"strings"
)

var idleChatTopicPrefixRe = regexp.MustCompile(`^今日のお題（[^）]+）:\s*`)

type IdleChatSpeechInput struct {
	Type    string
	From    string
	To      string
	Content string
}

func FormatIdleChatTTSText(input IdleChatSpeechInput) string {
	content := strings.TrimSpace(input.Content)
	if IsIdleChatTopicAnnouncement(input) {
		topic := strings.TrimSpace(idleChatTopicPrefixRe.ReplaceAllString(content, ""))
		if topic == "" {
			return "きょうのおだい。"
		}
		return "きょうのおだい、" + EnsureIdleChatSentencePause(topic)
	}
	return EnsureIdleChatSentencePause(StripIdleChatSpeechNotes(StripIdleChatSpeakerAndReasoningLines(content, input.From)))
}

func FormatIdleChatDisplayText(input IdleChatSpeechInput) string {
	content := strings.TrimSpace(input.Content)
	if IsIdleChatTopicAnnouncement(input) {
		topic := strings.TrimSpace(idleChatTopicPrefixRe.ReplaceAllString(content, ""))
		if topic == "" {
			return "今日のお題："
		}
		return "今日のお題：" + topic
	}
	return EnsureIdleChatSentencePause(StripIdleChatSpeechNotes(StripIdleChatSpeakerAndReasoningLines(content, input.From)))
}

func EnsureIdleChatSentencePause(content string) string {
	if content == "" {
		return ""
	}
	switch {
	case strings.HasSuffix(content, "。"),
		strings.HasSuffix(content, "！"),
		strings.HasSuffix(content, "？"),
		strings.HasSuffix(content, "."),
		strings.HasSuffix(content, "!"),
		strings.HasSuffix(content, "?"):
		return content
	default:
		return content + "。"
	}
}

func StripIdleChatSpeechNotes(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(kept) > 0 && kept[len(kept)-1] != "" {
				kept = append(kept, "")
			}
			continue
		}
		if strings.HasPrefix(line, "注記:") || strings.HasPrefix(line, "注記：") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func StripIdleChatSpeakerAndReasoningLines(content, speaker string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) == 0 {
		return ""
	}
	currentSpeaker := NormalizeIdleChatSpeakerName(speaker)
	type labeledLine struct {
		speaker string
		body    string
	}
	labeled := make([]labeledLine, 0, len(lines))
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = StripIdleChatInlineReasoningFragment(line)
		if line == "" {
			continue
		}
		line = StripIdleChatPossibleResponsePrefix(line)
		lineSpeaker, body, ok := SplitIdleChatSpeakerLine(line)
		if ok {
			body = StripIdleChatPossibleResponsePrefix(body)
			body = StripIdleChatInlineReasoningFragment(body)
			if body == "" || LooksLikeIdleChatReasoningLine(body) {
				continue
			}
			labeled = append(labeled, labeledLine{speaker: lineSpeaker, body: body})
			continue
		}
		if LooksLikeIdleChatReasoningLine(line) {
			continue
		}
		kept = append(kept, line)
	}
	if currentSpeaker != "" && len(labeled) > 0 {
		current := make([]string, 0, len(labeled))
		for _, line := range labeled {
			if line.speaker == currentSpeaker {
				current = append(current, line.body)
			}
		}
		if len(current) > 0 {
			return strings.TrimSpace(strings.Join(current, "\n"))
		}
	}
	for _, line := range labeled {
		kept = append(kept, line.body)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func StripIdleChatInlineReasoningFragment(line string) string {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	cut := -1
	markers := []string{
		"\" that's one sentence",
		"” that's one sentence",
		"that's one sentence",
		"maybe add",
		"maybe a ",
		"so mio is suggesting",
		"so shiro is suggesting",
		"so みお is suggesting",
		"so しろ is suggesting",
	}
	for _, marker := range markers {
		if idx := strings.Index(lower, marker); idx >= 0 && (cut < 0 || idx < cut) {
			cut = idx
		}
	}
	if cut >= 0 {
		trimmed = strings.TrimSpace(trimmed[:cut])
		trimmed = strings.TrimRight(trimmed, "\"“” ")
	}
	return strings.TrimSpace(trimmed)
}

func StripIdleChatPossibleResponsePrefix(line string) string {
	trimmed := strings.TrimSpace(line)
	for _, prefix := range []string{"Possible response:", "possible response:", "Possible response：", "possible response："} {
		if strings.HasPrefix(trimmed, prefix) {
			trimmed = strings.TrimSpace(trimmed[len(prefix):])
			trimmed = strings.Trim(trimmed, "\"“”")
			return strings.TrimSpace(trimmed)
		}
	}
	return trimmed
}

func SplitIdleChatSpeakerLine(line string) (speaker, body string, ok bool) {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	prefixes := []struct {
		raw     string
		speaker string
	}{
		{"assistant: [mio]:", "mio"},
		{"assistant: [mio]：", "mio"},
		{"assistant: [shiro]:", "shiro"},
		{"assistant: [shiro]：", "shiro"},
		{"[mio]:", "mio"},
		{"[mio]：", "mio"},
		{"[shiro]:", "shiro"},
		{"[shiro]：", "shiro"},
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
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, strings.ToLower(prefix.raw)) {
			return prefix.speaker, strings.TrimSpace(trimmed[len(prefix.raw):]), true
		}
	}
	return "", "", false
}

func NormalizeIdleChatSpeakerName(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "mio", "みお", "ミオ":
		return "mio"
	case "shiro", "しろ", "シロ":
		return "shiro"
	default:
		return ""
	}
}

func LooksLikeIdleChatReasoningLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	if lower == "" {
		return false
	}
	markers := []string{
		"okay, let's",
		"the user wants me",
		"the user said",
		"the user's instruction",
		"looking at the",
		"first, check",
		"example responses",
		"possible response",
		"but wait",
		"maybe better",
		"i need to",
		"i should",
		"mio says",
		"shiro says",
		"should respond",
		"is suggesting",
		"someone at the",
		"waiting presence",
		"latest message",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	if IsIdleChatEnglishDominantLine(lower) {
		return true
	}
	return false
}

func IsIdleChatEnglishDominantLine(line string) bool {
	var asciiLetters, japaneseLetters int
	for _, r := range line {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			asciiLetters++
		case (r >= '\u3040' && r <= '\u30ff') || (r >= '\u3400' && r <= '\u9fff'):
			japaneseLetters++
		}
	}
	return asciiLetters >= 12 && asciiLetters > japaneseLetters*2
}

func IsIdleChatTopicAnnouncement(input IdleChatSpeechInput) bool {
	content := strings.TrimSpace(input.Content)
	return strings.EqualFold(input.From, "user") &&
		strings.EqualFold(input.To, "mio") &&
		idleChatTopicPrefixRe.MatchString(content)
}

func IsIdleChatTTSEventType(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "idlechat.message", "idlechat.topic", "idlechat.tts":
		return true
	default:
		return false
	}
}
