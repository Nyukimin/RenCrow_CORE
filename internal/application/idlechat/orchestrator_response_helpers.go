package idlechat

import (
	"log"
	"strings"
	"unicode/utf8"
)

func firstTurnLabel(turn int) string {
	if turn == 0 {
		return "会話の最初の発話"
	}
	return "直前の相手発言への返答"
}

func idleMaxTokensForSpeaker(speaker string, defaultMax int) int {
	switch strings.ToLower(strings.TrimSpace(speaker)) {
	case "shiro", "chatworker":
		if defaultMax <= idleChatRetryMaxTokens {
			return idleChatShiroRetryMaxTokens
		}
		return idleChatShiroResponseMaxTokens
	}
	return defaultMax
}

func idleFunScorePercent(response, latestOther, latestSelf, topic string) int {
	s := strings.TrimSpace(response)
	if s == "" {
		return 0
	}
	score := 45
	runeLen := utf8.RuneCountInString(s)
	if runeLen >= 28 && runeLen <= 120 {
		score += 10
	} else if runeLen > 160 {
		score -= 15
	}
	if strings.ContainsAny(s, "？?") {
		score += 8
	}
	if containsAny(s, "秘密", "隠", "嘘", "鍵", "手紙", "封筒", "雨", "机", "駅", "階段", "選", "損", "怖", "失敗", "反転", "開ける", "落ちた") {
		score += 18
	}
	if containsAny(s, "誰", "なぜ", "どうして", "どちら", "選ぶ", "開ける", "守る", "困る") {
		score += 10
	}
	if containsAny(s, "面白いですね", "有効ですね", "構造", "整理", "検証", "可能性", "観点", "要素") {
		score -= 16
	}
	if latestOther != "" && textSimilarity(s, latestOther) >= 0.45 {
		score -= 12
	}
	if latestSelf != "" && textSimilarity(s, latestSelf) >= 0.45 {
		score -= 12
	}
	if topic != "" {
		for _, part := range strings.FieldsFunc(topic, func(r rune) bool {
			return r == ' ' || r == '　' || r == 'と' || r == '、' || r == '。' || r == ',' || r == '/'
		}) {
			part = strings.TrimSpace(part)
			if utf8.RuneCountInString(part) >= 2 && strings.Contains(s, part) {
				score += 4
				break
			}
		}
	}
	if englishDominantIdleText(s) || hasInternalReasoningLeak(s) || hasPromptLeak(s) {
		score -= 40
	}
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

func extractIdleTopicText(content string) string {
	s := strings.TrimSpace(content)
	if s == "" {
		return ""
	}
	prefixes := []string{"今日のお題", "お題"}
	for _, prefix := range prefixes {
		if !strings.HasPrefix(s, prefix) {
			continue
		}
		if idx := strings.IndexAny(s, ":：、,"); idx >= 0 && idx+1 < len(s) {
			return strings.TrimSpace(s[idx+1:])
		}
	}
	return ""
}

func (o *IdleChatOrchestrator) resolveDialogueTopic(sessionID, speaker, topic string) string {
	if normalized := strings.TrimSpace(topic); normalized != "" {
		return normalized
	}
	if sessionID != "" {
		for _, entry := range o.memory.GetUnifiedView(24) {
			if entry.Message.SessionID != sessionID {
				continue
			}
			if extracted := extractIdleTopicText(entry.Message.Content); extracted != "" {
				log.Printf("[IdleChat] Empty dialogue topic recovered from session memory: session=%s topic=%q", sessionID, truncate(extracted, 80))
				return extracted
			}
		}
	}
	o.mu.Lock()
	currentTopic := strings.TrimSpace(o.currentTopic)
	o.mu.Unlock()
	if currentTopic != "" && !strings.Contains(currentTopic, "準備中") {
		log.Printf("[IdleChat] Empty dialogue topic recovered from current topic: session=%s topic=%q", sessionID, truncate(currentTopic, 80))
		return currentTopic
	}
	log.Printf("[IdleChat] Empty dialogue topic; using emergency fallback: session=%s speaker=%s", sessionID, speaker)
	return "この会話の現在のお題"
}

func (o *IdleChatOrchestrator) temperatureForSpeaker(speaker string) float64 {
	switch strings.ToLower(strings.TrimSpace(speaker)) {
	case "mio", "shiro":
		return 0.65
	default:
		return o.temperature
	}
}
