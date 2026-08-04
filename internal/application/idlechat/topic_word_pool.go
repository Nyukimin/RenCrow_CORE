package idlechat

import (
	"math/rand"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	staticTopicWordLimit   = 48
	freshTopicWordLimit    = 32
	topicWordPoolLimit     = staticTopicWordLimit + freshTopicWordLimit
	staticClassicWordLimit = 4
	freshSocialWordLimit   = 8
	recentTopicWordLimit   = 20

	topicWordKindStatic = "static"
	topicWordKindFresh  = "fresh"
)

type topicWord struct {
	Value      string
	Kind       string
	Classic    bool
	SourceType string
	Context    string
}

// staticTopicWords is intentionally small. Current terms arrive through the
// daily fresh-word projection instead of accumulating in this list forever.
var staticTopicWords = []topicWord{
	{Value: "生成AI", Kind: topicWordKindStatic},
	{Value: "AIエージェント", Kind: topicWordKindStatic},
	{Value: "ロボティクス", Kind: topicWordKindStatic},
	{Value: "スマートフォン", Kind: topicWordKindStatic},
	{Value: "SNS", Kind: topicWordKindStatic},
	{Value: "動画配信", Kind: topicWordKindStatic},
	{Value: "オンラインコミュニティ", Kind: topicWordKindStatic},
	{Value: "デジタル決済", Kind: topicWordKindStatic},
	{Value: "サブスクリプション", Kind: topicWordKindStatic},
	{Value: "リモートワーク", Kind: topicWordKindStatic},
	{Value: "サイバーセキュリティ", Kind: topicWordKindStatic},
	{Value: "個人データ", Kind: topicWordKindStatic},
	{Value: "クラウド", Kind: topicWordKindStatic},
	{Value: "半導体", Kind: topicWordKindStatic},
	{Value: "自動運転", Kind: topicWordKindStatic},
	{Value: "ドローン", Kind: topicWordKindStatic},
	{Value: "再生可能エネルギー", Kind: topicWordKindStatic},
	{Value: "バッテリー", Kind: topicWordKindStatic},
	{Value: "3Dプリンター", Kind: topicWordKindStatic},
	{Value: "ウェアラブル", Kind: topicWordKindStatic},
	{Value: "物流", Kind: topicWordKindStatic},
	{Value: "介護", Kind: topicWordKindStatic},
	{Value: "子育て", Kind: topicWordKindStatic},
	{Value: "教育", Kind: topicWordKindStatic},
	{Value: "医療", Kind: topicWordKindStatic},
	{Value: "住宅", Kind: topicWordKindStatic},
	{Value: "防災", Kind: topicWordKindStatic},
	{Value: "公共交通", Kind: topicWordKindStatic},
	{Value: "食品ロス", Kind: topicWordKindStatic},
	{Value: "働き方", Kind: topicWordKindStatic},
	{Value: "副業", Kind: topicWordKindStatic},
	{Value: "地域コミュニティ", Kind: topicWordKindStatic},
	{Value: "宇宙開発", Kind: topicWordKindStatic},
	{Value: "気候変動", Kind: topicWordKindStatic},
	{Value: "生物多様性", Kind: topicWordKindStatic},
	{Value: "ゲノム医療", Kind: topicWordKindStatic},
	{Value: "再生医療", Kind: topicWordKindStatic},
	{Value: "感染症", Kind: topicWordKindStatic},
	{Value: "海洋", Kind: topicWordKindStatic},
	{Value: "気象", Kind: topicWordKindStatic},
	{Value: "量子技術", Kind: topicWordKindStatic},
	{Value: "新素材", Kind: topicWordKindStatic},
	{Value: "脳科学", Kind: topicWordKindStatic},
	{Value: "睡眠", Kind: topicWordKindStatic},
	{Value: "落語", Kind: topicWordKindStatic, Classic: true},
	{Value: "陶芸", Kind: topicWordKindStatic, Classic: true},
	{Value: "将棋", Kind: topicWordKindStatic, Classic: true},
	{Value: "民俗学", Kind: topicWordKindStatic, Classic: true},
}

func staticTopicWordValues() []string {
	values := make([]string, 0, len(staticTopicWords))
	for _, word := range staticTopicWords {
		values = append(values, word.Value)
	}
	return values
}

func buildFreshTopicWords(cache *DailySeedCache, now time.Time) []topicWord {
	if cache == nil || cache.Date != now.In(jst).Format("2006-01-02") {
		return nil
	}
	staticKeys := make(map[string]struct{}, len(staticTopicWords))
	for _, word := range staticTopicWords {
		staticKeys[normalizeTopicWord(word.Value)] = struct{}{}
	}
	seen := make(map[string]struct{}, freshTopicWordLimit)
	result := make([]topicWord, 0, freshTopicWordLimit)

	appendTerms := func(social bool) {
		socialCount := 0
		for _, item := range cache.NewsSeedItems {
			itemSocial := isSocialTopicSource(item.SourceType)
			if itemSocial != social || strings.TrimSpace(item.SourceReadStatus) != "ready" {
				continue
			}
			for _, note := range item.TermNotes {
				status := strings.ToLower(strings.TrimSpace(note.Status))
				if status != "contextual" && status != "confirmed" {
					continue
				}
				value := cleanTopicWord(note.Term)
				key := normalizeTopicWord(value)
				if key == "" {
					continue
				}
				if _, ok := staticKeys[key]; ok {
					continue
				}
				if _, ok := seen[key]; ok {
					continue
				}
				if social && socialCount >= freshSocialWordLimit {
					return
				}
				seen[key] = struct{}{}
				result = append(result, topicWord{
					Value:      value,
					Kind:       topicWordKindFresh,
					SourceType: strings.ToLower(strings.TrimSpace(item.SourceType)),
					Context:    strings.TrimSpace(note.Explanation),
				})
				if social {
					socialCount++
				}
				if len(result) >= freshTopicWordLimit {
					return
				}
			}
		}
	}

	appendTerms(false)
	if len(result) < freshTopicWordLimit {
		appendTerms(true)
	}
	return result
}

func cleanTopicWord(value string) string {
	value = strings.Trim(strings.Join(strings.Fields(value), " "), "「」『』【】[]()（）・,，.。:：;；!?！？\"'")
	if value == "" || utf8.RuneCountInString(value) < 2 || utf8.RuneCountInString(value) > 24 {
		return ""
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "http://") || strings.Contains(lower, "https://") || strings.Contains(lower, "www.") {
		return ""
	}
	return value
}

func normalizeTopicWord(value string) string {
	return strings.ToLower(cleanTopicWord(value))
}

func isSocialTopicSource(sourceType string) bool {
	switch strings.ToLower(strings.TrimSpace(sourceType)) {
	case "reddit", "x":
		return true
	default:
		return false
	}
}

func chooseTopicWords(strategy TopicStrategy, staticWords, freshWords []topicWord, recent []string, previousClassic bool, intn func(int) int) []topicWord {
	if intn == nil {
		intn = rand.Intn
	}
	blocked := make(map[string]struct{}, len(recent))
	for _, value := range recent {
		if key := normalizeTopicWord(value); key != "" {
			blocked[key] = struct{}{}
		}
	}
	filter := func(words []topicWord) []topicWord {
		out := make([]topicWord, 0, len(words))
		for _, word := range words {
			key := normalizeTopicWord(word.Value)
			if key == "" || (previousClassic && word.Classic) {
				continue
			}
			if _, ok := blocked[key]; ok {
				continue
			}
			out = append(out, word)
		}
		return out
	}
	availableStatic := filter(staticWords)
	availableFresh := filter(freshWords)
	pick := func(words []topicWord) (topicWord, bool) {
		if len(words) == 0 {
			return topicWord{}, false
		}
		return words[intn(len(words))%len(words)], true
	}

	if strategy == StrategySingleGenre {
		if len(availableFresh) > 0 && intn(100)%100 < 70 {
			if word, ok := pick(availableFresh); ok {
				return []topicWord{word}
			}
		}
		if word, ok := pick(availableStatic); ok {
			return []topicWord{word}
		}
		if word, ok := pick(availableFresh); ok {
			return []topicWord{word}
		}
		return nil
	}

	if strategy == StrategyDoubleGenre {
		selected := make([]topicWord, 0, 2)
		if word, ok := pick(availableFresh); ok {
			selected = append(selected, word)
			blocked[normalizeTopicWord(word.Value)] = struct{}{}
		}
		remainingStatic := filter(staticWords)
		if word, ok := pick(remainingStatic); ok {
			selected = append(selected, word)
		}
		if len(selected) < 2 {
			blocked = make(map[string]struct{}, len(recent)+len(selected))
			for _, value := range recent {
				blocked[normalizeTopicWord(value)] = struct{}{}
			}
			for _, word := range selected {
				blocked[normalizeTopicWord(word.Value)] = struct{}{}
			}
			combined := append(filter(staticWords), filter(freshWords)...)
			if word, ok := pick(combined); ok {
				selected = append(selected, word)
			}
		}
		return selected
	}
	return nil
}

func appendRecentTopicWords(recent []string, selected []topicWord, limit int) []string {
	if limit <= 0 {
		limit = recentTopicWordLimit
	}
	out := append([]string(nil), recent...)
	for _, word := range selected {
		if value := cleanTopicWord(word.Value); value != "" {
			out = append(out, value)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func topicWordsUseClassic(words []topicWord) bool {
	for _, word := range words {
		if word.Classic {
			return true
		}
	}
	return false
}
