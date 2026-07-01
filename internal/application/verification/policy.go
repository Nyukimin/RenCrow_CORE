package verification

import (
	"strings"

	domainverification "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/verification"
)

func DetermineTriggerLevel(req Request, fallback domainverification.TriggerLevel) domainverification.TriggerLevel {
	if fallback == "" || !fallback.Valid() {
		fallback = domainverification.TriggerLow
	}
	haystack := strings.ToLower(req.Route + "\n" + req.UserMessage + "\n" + req.DraftResponse)
	if containsAny(haystack, []string{
		"research", "news", "ニュース", "出典", "引用", "citation", "検索",
		"external_search", "source registry", "source_registry", "覚えて", "保存",
		"memory_write", "事実", "factual", "http://", "https://",
	}) {
		return domainverification.TriggerHigh
	}
	if containsAny(haystack, []string{
		"memory", "記憶", "recommend", "おすすめ", "推薦", "knowledge",
		"knowledge_db", "kb", "作品", "ユーザーについて", "好み",
	}) {
		return domainverification.TriggerMedium
	}
	return fallback
}

func containsAny(text string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(text, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}
