package idlechat

import (
	"sort"
	"strings"
	"unicode"
)

func topicTooSimilar(topic string, recent []string) bool {
	n := normalizeLoopText(topic)
	if n == "" {
		return true
	}
	for _, prev := range recent {
		if textSimilarity(n, normalizeLoopText(prev)) >= 0.9 {
			return true
		}
	}
	return false
}

func isResponseTooSimilar(response string, transcript []string) bool {
	if len(transcript) < 4 {
		return false
	}
	cur := normalizeLoopText(response)
	if cur == "" {
		return false
	}
	start := len(transcript) - 6
	if start < 0 {
		start = 0
	}
	hits := 0
	for i := start; i < len(transcript); i++ {
		prev := normalizeLoopText(transcript[i])
		if prev == "" {
			continue
		}
		if textSimilarity(cur, prev) >= 0.93 {
			hits++
		}
	}
	return hits >= 2
}

func normalizeLoopText(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if idx := strings.Index(s, ":"); idx >= 0 {
		s = strings.TrimSpace(s[idx+1:])
	}
	s = strings.TrimPrefix(s, "[mio]")
	s = strings.TrimPrefix(s, "[shiro]")
	s = strings.TrimPrefix(s, "[worker]")
	s = strings.TrimPrefix(s, "[chat]")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func textSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	ag := runeNGrams(a, 2)
	bg := runeNGrams(b, 2)
	if len(ag) == 0 || len(bg) == 0 {
		if a == b {
			return 1
		}
		return 0
	}
	inter := 0
	i, j := 0, 0
	for i < len(ag) && j < len(bg) {
		if ag[i] == bg[j] {
			inter++
			i++
			j++
			continue
		}
		if ag[i] < bg[j] {
			i++
		} else {
			j++
		}
	}
	return (2.0 * float64(inter)) / float64(len(ag)+len(bg))
}

func runeNGrams(s string, n int) []string {
	r := []rune(s)
	if len(r) < n || n <= 0 {
		return nil
	}
	out := make([]string, 0, len(r)-n+1)
	for i := 0; i <= len(r)-n; i++ {
		out = append(out, string(r[i:i+n]))
	}
	sort.Strings(out)
	return out
}
