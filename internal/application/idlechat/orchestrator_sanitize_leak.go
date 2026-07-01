package idlechat

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

func hasPromptLeak(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	if lower == "" {
		return false
	}
	markers := []string{
		"<|",
		"|>",
		"channel>thought",
		"channel=analysis",
		"analysis to=",
		"assistant to=",
		"発言帰属ガード",
		"相手の発言として受ける",
		"相手の案を整理",
		"前に自分も触れた",
		"次に起きそうな場面",
		"直前の相手発言",
		"直前の自分",
		"1〜2文",
		"1-2文",
		"具体物・選択",
		"具体物・理由・問い",
		"条件・制約",
		"直前と違う入口",
		"直前と入口を変え",
		"どれか一つを足してください",
		"自然な日本語だけ",
		"文で返してください",
		"要件:",
		"要件：",
		"（話題:",
		"現在の状況",
		"目標:",
		"目標：",
		"制約事項",
		"会話の制約",
		"システムプロンプト",
	}
	for _, m := range markers {
		if strings.Contains(lower, strings.ToLower(m)) {
			return true
		}
	}
	if strings.Contains(lower, "発言として受け") {
		return true
	}
	return false
}

func hasInternalReasoningLeak(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	markers := []string{
		"okay, let's",
		"okay, so",
		"ok, let's",
		"alright,",
		"let me",
		"the user is asking",
		"the user's question",
		"the user wants me",
		"looking at the",
		"the example response",
		"example responses",
		"possible response",
		"the task is",
		"the requirements",
		"the previous message",
		"the user's instruction",
		"but wait",
		"maybe better",
		"i need to",
		"i should",
		"should explain",
		"ユーザーは私",
		"私はmioとして",
		"私はshiroとして",
		"mioとして、",
		"shiroとして、",
		"必要がある",
		"遵守する必要",
		"以下の点",
		"会話の制約",
		"キャラクター（",
		"**現在の状況**",
		"**目標**",
		"1. **",
		"2. **",
		"好的",
		"我现在需要",
		"用户",
		"规则",
		"检查",
		"首先",
		"比如",
		"或者",
		"因为",
		"所以",
	}
	for _, marker := range markers {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return true
		}
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) >= 3 {
		bullets := 0
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || regexp.MustCompile(`^\d+[.)．]\s*`).MatchString(line) {
				bullets++
			}
		}
		if bullets >= 2 {
			return true
		}
	}
	return false
}

func englishDominantIdleText(s string) bool {
	totalLetters := 0
	asciiLetters := 0
	japaneseLetters := 0
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		totalLetters++
		if r <= unicode.MaxASCII {
			asciiLetters++
			continue
		}
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) {
			japaneseLetters++
		}
	}
	if totalLetters < 12 {
		return false
	}
	return japaneseLetters == 0 && asciiLetters*100/totalLetters >= 80
}

func dropLeadingReasoningParagraphs(s string) string {
	parts := strings.Split(strings.TrimSpace(s), "\n\n")
	if len(parts) <= 1 {
		return strings.TrimSpace(s)
	}
	start := 0
	for start < len(parts) {
		p := strings.TrimSpace(parts[start])
		if p == "" || hasPromptLeak(p) || hasInternalReasoningLeak(p) || summaryLooksLikeEnglishReasoningLead(p) {
			start++
			continue
		}
		break
	}
	if start >= len(parts) {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(strings.Join(parts[start:], "\n\n"))
}

func summaryLooksLikeEnglishReasoningLead(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	if lower == "" {
		return false
	}
	leadings := []string{
		"okay,",
		"ok,",
		"alright,",
		"first,",
		"the user wants me",
		"the user asks me",
		"i need to",
		"let me",
	}
	for _, p := range leadings {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

func summaryLooksLikeEnglishMetaReasoning(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	if lower == "" {
		return false
	}
	markers := []string{
		"the user provided",
		"the user wants me",
		"the output format",
		"first, i need",
		"looking at the",
		"let me break it down",
		"wait, the",
	}
	metaHits := 0
	for _, m := range markers {
		if strings.Contains(lower, m) {
			metaHits++
		}
	}
	// メタ推論キーワード一致のみで判定する
	if metaHits >= 2 {
		return true
	}
	return false
}

func invalidIdleResponse(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return true
	}
	if containsUnexpectedIdleScript(trimmed) {
		return true
	}
	hasText := false
	for _, r := range trimmed {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) {
			hasText = true
			break
		}
	}
	if !hasText {
		return true
	}
	if utf8.RuneCountInString(trimmed) < 12 && !hasIdleSentenceEnd(trimmed) {
		return true
	}
	if looksLikeUnfinishedIdleResponse(trimmed) {
		return true
	}
	first, _ := utf8.DecodeRuneInString(trimmed)
	if unicode.IsPunct(first) || unicode.IsSymbol(first) {
		return true
	}
	lower := strings.ToLower(trimmed)
	if lower == "。" || lower == "、" || lower == "!" || lower == "！" || lower == "?" || lower == "？" {
		return true
	}
	return false
}

func looksLikeUnfinishedIdleResponse(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return true
	}
	trimmed = strings.TrimRightFunc(trimmed, func(r rune) bool {
		return unicode.IsSpace(r) || r == '」' || r == '』' || r == ')' || r == '）' || r == ']' || r == '】'
	})
	if trimmed == "" {
		return true
	}
	last, _ := utf8.DecodeLastRuneInString(trimmed)
	switch last {
	case '。', '！', '？', '!', '?', '.', '…':
		return false
	default:
		return true
	}
}

func containsUnexpectedIdleScript(s string) bool {
	for _, r := range s {
		switch {
		case unicode.In(r, unicode.Devanagari, unicode.Hangul, unicode.Arabic, unicode.Hebrew, unicode.Thai, unicode.Cyrillic):
			return true
		}
	}
	return false
}
