package idlechat

import (
	"strings"
)

func hasIdleSentenceEnd(s string) bool {
	return strings.ContainsAny(strings.TrimSpace(s), "。！？!?")
}

func hasAwkwardIdleStyle(speaker, s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	if lower == "" {
		return false
	}
	banned := []string{
		"前に自分も触れた",
		"相手の発言として受ける",
		"まさにその通りですね",
		"ご発言",
	}
	for _, phrase := range banned {
		if strings.Contains(lower, strings.ToLower(phrase)) {
			return true
		}
	}
	if strings.EqualFold(strings.TrimSpace(speaker), "shiro") {
		shiroBanned := []string{
			"mioさん",
			"mio さん",
			"非常に興味深いですね",
			"非常に的確",
			"硬すぎました",
			"言い直すと",
			"少し硬すぎました",
		}
		for _, phrase := range shiroBanned {
			if strings.Contains(lower, strings.ToLower(phrase)) {
				return true
			}
		}
	}
	if strings.EqualFold(strings.TrimSpace(speaker), "mio") {
		mioBanned := []string{
			"ご懸念はもっともかと存じます",
			"非常に興味深いですね",
			"その光",
		}
		for _, phrase := range mioBanned {
			if strings.Contains(lower, strings.ToLower(phrase)) {
				return true
			}
		}
	}
	return false
}

func needsIdleStyleRetry(speaker, response, latestOther, latestSelf, topic string) bool {
	return hasAwkwardIdleStyle(speaker, response) ||
		hasExcessivePhraseRepetition(response) ||
		mirrorsLatestOther(response, latestOther, topic) ||
		repeatsLatestSelf(response, latestSelf)
}
