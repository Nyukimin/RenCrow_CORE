package idlechat

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

func mirrorsLatestOther(response, latestOther, topic string) bool {
	resp := strings.TrimSpace(response)
	other := strings.TrimSpace(latestOther)
	if resp == "" || other == "" {
		return false
	}
	common := longestCommonSubstring(resp, other)
	if utf8.RuneCountInString(common) < 6 {
		return false
	}
	if strings.TrimSpace(topic) != "" && strings.Contains(strings.TrimSpace(topic), common) {
		return false
	}
	return true
}

func repeatsLatestSelf(response, latestSelf string) bool {
	resp := strings.TrimSpace(response)
	self := strings.TrimSpace(latestSelf)
	if resp == "" || self == "" {
		return false
	}
	common := longestCommonSubstring(resp, self)
	return utf8.RuneCountInString(common) >= 10
}

func longestCommonSubstring(a, b string) string {
	ar := []rune(a)
	br := []rune(b)
	if len(ar) == 0 || len(br) == 0 {
		return ""
	}
	dp := make([]int, len(br)+1)
	bestLen := 0
	bestEnd := 0
	for i := 1; i <= len(ar); i++ {
		prev := 0
		for j := 1; j <= len(br); j++ {
			tmp := dp[j]
			if ar[i-1] == br[j-1] {
				dp[j] = prev + 1
				if dp[j] > bestLen {
					bestLen = dp[j]
					bestEnd = i
				}
			} else {
				dp[j] = 0
			}
			prev = tmp
		}
	}
	if bestLen == 0 {
		return ""
	}
	return string(ar[bestEnd-bestLen : bestEnd])
}

func hasExcessivePhraseRepetition(s string) bool {
	normalized := strings.ToLower(strings.TrimSpace(s))
	if normalized == "" {
		return false
	}
	tokens := splitIdleTokens(normalized)
	if len(tokens) < 4 {
		return false
	}
	counts := map[string]int{}
	for _, token := range tokens {
		if len([]rune(token)) <= 1 {
			continue
		}
		counts[token]++
		if counts[token] >= 3 {
			return true
		}
	}
	for size := 2; size <= 4; size++ {
		if len(tokens) < size*2 {
			continue
		}
		ngrams := map[string]int{}
		for i := 0; i+size <= len(tokens); i++ {
			key := strings.Join(tokens[i:i+size], " ")
			ngrams[key]++
			if ngrams[key] >= 2 {
				return true
			}
		}
	}
	return false
}

func splitIdleTokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	})
}
