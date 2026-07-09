package idlechat

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
)

func splitTranscriptSpeaker(line string) (speaker, text string) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", strings.TrimSpace(line)
	}
	speaker = strings.ToLower(strings.TrimSpace(line[:idx]))
	text = strings.TrimSpace(line[idx+1:])
	return speaker, text
}

func transcriptLeadPattern(text string) string {
	s := strings.TrimSpace(strings.ToLower(text))
	s = strings.TrimLeftFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	})
	s = strings.TrimPrefix(s, "[mio]")
	s = strings.TrimPrefix(s, "[shiro]")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	count := 0
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			break
		}
		b.WriteRune(r)
		count++
		if count >= 8 {
			break
		}
	}
	// 5文字未満は「確かに」「なるほど」等の短い同意接頭辞。
	// 構造的テンプレートとはみなさず、誤検知を防ぐ。
	if b.Len() < 5 {
		return ""
	}
	return b.String()
}

func repeatedLeadPattern(keys []string) bool {
	if len(keys) < 3 {
		return false
	}
	counts := map[string]int{}
	for _, key := range keys {
		if key == "" {
			continue
		}
		counts[key]++
		if counts[key] >= 3 {
			return true
		}
	}
	return false
}

func splitSpeakerContexts(entries []session.ConversationEntry, sessionID, speaker string, limit int) ([]string, []string) {
	self := make([]string, 0, limit)
	other := make([]string, 0, limit)
	for i := len(entries) - 1; i >= 0 && (len(self) < limit || len(other) < limit); i-- {
		m := entries[i].Message
		if m.SessionID != sessionID {
			continue
		}
		text := truncate(strings.TrimSpace(m.Content), 80)
		if text == "" {
			continue
		}
		if strings.EqualFold(m.From, speaker) {
			if len(self) < limit {
				self = append(self, text)
			}
			continue
		}
		if len(other) < limit {
			other = append(other, fmt.Sprintf("%s: %s", m.From, text))
		}
	}
	if len(self) == 0 {
		self = append(self, "なし")
	}
	if len(other) == 0 {
		other = append(other, "なし")
	}
	return self, other
}
