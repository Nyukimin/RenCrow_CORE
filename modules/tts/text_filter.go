package tts

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	codeBlockRe              = regexp.MustCompile("(?s)```.*?```")
	urlRe                    = regexp.MustCompile(`https?://\S+`)
	outerParenRe             = regexp.MustCompile(`(?s)^[(（][^)）]*[)）]\s*`)
	onlyPunctRe              = regexp.MustCompile(`^[\p{P}\p{S}\s]+$`)
	multiSpaceRe             = regexp.MustCompile(`\s+`)
	ackPrefixRe              = regexp.MustCompile(`^(はい、承知いたしました。|はい、承知しました。|承知いたしました。|承知しました。|了解しました。|かしこまりました。)\s*`)
	idleChatTopicPauseMarker = "__RENCROW_IDLECHAT_TOPIC_PAUSE__"
	speakNameRe              = strings.NewReplacer(
		"Mio", "みお",
		"mio", "みお",
		"Shiro", "しろ",
		"shiro", "しろ",
		"Aka", "あか",
		"aka", "あか",
		"Ao", "あお",
		"ao", "あお",
		"Gin", "ぎん",
		"gin", "ぎん",
	)
)

func FilterSpeakableText(eventType, route, text string) string {
	if strings.TrimSpace(eventType) != "agent.response" {
		return ""
	}
	s := strings.TrimSpace(text)
	if s == "" {
		return ""
	}
	s = codeBlockRe.ReplaceAllString(s, " ")
	s = urlRe.ReplaceAllString(s, " ")
	s = outerParenRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if onlyPunctRe.MatchString(line) {
			continue
		}
		out = append(out, line)
	}
	s = strings.Join(out, " ")
	s = ackPrefixRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "きょうのおだいです、", "きょうのおだいです"+idleChatTopicPauseMarker)
	s = multiSpaceRe.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, idleChatTopicPauseMarker, "、")
	s = speakNameRe.Replace(s)
	s = strings.TrimLeftFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	})
	s = strings.TrimSpace(s)
	if onlyPunctRe.MatchString(s) {
		return ""
	}
	_ = route
	return s
}
