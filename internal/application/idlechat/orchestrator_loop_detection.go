package idlechat

import (
	"strings"
)

func (o *IdleChatOrchestrator) getRecentTopics(limit int) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if limit <= 0 || limit > len(o.history) {
		limit = len(o.history)
	}
	out := make([]string, 0, limit)
	for i := len(o.history) - 1; i >= 0 && len(out) < limit; i-- {
		t := strings.TrimSpace(o.history[i].Topic)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func hasAlternatingLoop(transcript []string) bool {
	if len(transcript) < 8 {
		return false
	}
	a := normalizeLoopText(transcript[len(transcript)-1])
	b := normalizeLoopText(transcript[len(transcript)-2])
	if a == "" || b == "" {
		return false
	}
	matches := 0
	for i := len(transcript) - 3; i >= 0 && i >= len(transcript)-7; i -= 2 {
		if textSimilarity(a, normalizeLoopText(transcript[i])) >= 0.9 {
			matches++
		}
	}
	for i := len(transcript) - 4; i >= 0 && i >= len(transcript)-8; i -= 2 {
		if textSimilarity(b, normalizeLoopText(transcript[i])) >= 0.9 {
			matches++
		}
	}
	return matches >= 3
}

func hasShortAlternatingLoop(transcript []string) bool {
	if len(transcript) < 4 {
		return false
	}
	a := normalizeLoopText(transcript[len(transcript)-1])
	b := normalizeLoopText(transcript[len(transcript)-2])
	c := normalizeLoopText(transcript[len(transcript)-3])
	d := normalizeLoopText(transcript[len(transcript)-4])
	if a == "" || b == "" || c == "" || d == "" {
		return false
	}
	return textSimilarity(a, c) >= 0.9 && textSimilarity(b, d) >= 0.9
}

func hasHighSimilarityLoop(transcript []string) bool {
	if len(transcript) < 10 {
		return false
	}
	start := len(transcript) - 10
	base := make([]string, 0, 10)
	for i := start; i < len(transcript); i++ {
		t := normalizeLoopText(transcript[i])
		if t != "" {
			base = append(base, t)
		}
	}
	if len(base) < 6 {
		return false
	}
	similarPairs := 0
	totalPairs := 0
	for i := 0; i < len(base); i++ {
		for j := i + 1; j < len(base); j++ {
			totalPairs++
			if textSimilarity(base[i], base[j]) >= 0.92 {
				similarPairs++
			}
		}
	}
	return totalPairs > 0 && similarPairs*3 >= totalPairs
}

func hasShortHighSimilarityLoop(transcript []string) bool {
	if len(transcript) < 4 {
		return false
	}
	start := len(transcript) - 4
	base := make([]string, 0, 4)
	for i := start; i < len(transcript); i++ {
		t := normalizeLoopText(transcript[i])
		if t != "" {
			base = append(base, t)
		}
	}
	if len(base) < 4 {
		return false
	}
	similarPairs := 0
	for i := 0; i < len(base); i++ {
		for j := i + 1; j < len(base); j++ {
			if textSimilarity(base[i], base[j]) >= 0.94 {
				similarPairs++
			}
		}
	}
	return similarPairs >= 3
}

func hasSpeakerTemplateLoop(transcript []string) bool {
	if len(transcript) < 6 {
		return false
	}
	type speakerTurn struct {
		speaker string
		text    string
	}
	turns := make([]speakerTurn, 0, 10)
	start := len(transcript) - 10
	if start < 0 {
		start = 0
	}
	for i := start; i < len(transcript); i++ {
		speaker, text := splitTranscriptSpeaker(transcript[i])
		if speaker == "" || text == "" {
			continue
		}
		turns = append(turns, speakerTurn{speaker: speaker, text: text})
	}
	if len(turns) < 6 {
		return false
	}

	perSpeaker := map[string][]string{}
	for _, turn := range turns {
		key := transcriptLeadPattern(turn.text)
		if key == "" {
			continue
		}
		perSpeaker[turn.speaker] = append(perSpeaker[turn.speaker], key)
	}
	for _, keys := range perSpeaker {
		if repeatedLeadPattern(keys) {
			return true
		}
	}

	for speaker := range perSpeaker {
		msgs := make([]string, 0, 4)
		for i := len(turns) - 1; i >= 0 && len(msgs) < 4; i-- {
			if turns[i].speaker == speaker {
				msgs = append(msgs, normalizeLoopText(turns[i].text))
			}
		}
		if len(msgs) < 3 {
			continue
		}
		similarPairs := 0
		for i := 0; i < len(msgs); i++ {
			for j := i + 1; j < len(msgs); j++ {
				if textSimilarity(msgs[i], msgs[j]) >= 0.82 {
					similarPairs++
				}
			}
		}
		if similarPairs >= 2 {
			return true
		}
	}
	return false
}

func hasShortSpeakerTemplateLoop(transcript []string) bool {
	if len(transcript) < 6 {
		return false
	}
	type speakerTurn struct {
		speaker string
		text    string
	}
	// 直近6ターンを検査。同一話者3ターン連続一致で発火。
	// 2ターン一致（4ターン窓）は深い議論での誤発火が多いため閾値を上げる。
	turns := make([]speakerTurn, 0, 6)
	for i := len(transcript) - 6; i < len(transcript); i++ {
		speaker, text := splitTranscriptSpeaker(transcript[i])
		if speaker == "" || text == "" {
			continue
		}
		turns = append(turns, speakerTurn{speaker: speaker, text: text})
	}
	if len(turns) < 6 {
		return false
	}
	perSpeaker := map[string][]string{}
	for _, turn := range turns {
		key := transcriptLeadPattern(turn.text)
		if key == "" {
			continue
		}
		perSpeaker[turn.speaker] = append(perSpeaker[turn.speaker], key)
	}
	for _, keys := range perSpeaker {
		// 同一話者3ターン分が揃い、かつ最後の3ターンすべて同一パターン
		if len(keys) >= 3 && keys[len(keys)-1] == keys[len(keys)-2] && keys[len(keys)-2] == keys[len(keys)-3] {
			return true
		}
	}
	return false
}
