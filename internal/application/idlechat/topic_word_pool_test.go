package idlechat

import (
	"strings"
	"testing"
	"time"

	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

func TestStaticTopicWordPoolIsCompactAndUnique(t *testing.T) {
	if got, want := len(staticTopicWords), staticTopicWordLimit; got != want {
		t.Fatalf("static words = %d, want %d", got, want)
	}
	seen := make(map[string]struct{}, len(staticTopicWords))
	classic := 0
	for _, word := range staticTopicWords {
		key := normalizeTopicWord(word.Value)
		if key == "" {
			t.Fatalf("static pool contains empty word: %+v", word)
		}
		if _, ok := seen[key]; ok {
			t.Fatalf("static pool contains duplicate word %q", word.Value)
		}
		seen[key] = struct{}{}
		if word.Classic {
			classic++
		}
	}
	if classic != staticClassicWordLimit {
		t.Fatalf("classic words = %d, want %d", classic, staticClassicWordLimit)
	}
}

func TestBuildFreshTopicWordsUsesOnlyCurrentVerifiedTermsAndCapsSocial(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, jst)
	socialTerms := make([]modulechat.NewsTermNote, 0, 12)
	for i := 0; i < 12; i++ {
		socialTerms = append(socialTerms, modulechat.NewsTermNote{Term: "SNS新語" + string(rune('A'+i)), Status: "contextual"})
	}
	cache := &DailySeedCache{
		Date:      "2026-08-04",
		FetchedAt: now,
		NewsSeedItems: []NewsSeed{
			{
				SourceType: "rss", SourceReadStatus: "ready",
				TermNotes: []modulechat.NewsTermNote{
					{Term: "エージェント型AI", Explanation: "目標に沿って複数手順を進めるAIです。", Status: "contextual"},
					{Term: "検索拡張生成", Status: "confirmed"},
					{Term: "未確認語", Status: "unresolved"},
					{Term: "生成AI", Status: "confirmed"},
				},
			},
			{SourceType: "reddit", SourceReadStatus: "ready", TermNotes: socialTerms},
			{SourceType: "rss", SourceReadStatus: "unprocessed", TermNotes: []modulechat.NewsTermNote{{Term: "未処理語", Status: "confirmed"}}},
		},
	}

	got := buildFreshTopicWords(cache, now)
	if len(got) != 2+freshSocialWordLimit {
		t.Fatalf("fresh words = %d, want %d: %+v", len(got), 2+freshSocialWordLimit, got)
	}
	if got[0].Value != "エージェント型AI" || got[1].Value != "検索拡張生成" {
		t.Fatalf("verified RSS terms were not prioritized: %+v", got)
	}
	if got[0].Context != "目標に沿って複数手順を進めるAIです。" {
		t.Fatalf("fresh word context was not preserved: %+v", got[0])
	}
	for _, word := range got {
		if word.Value == "未確認語" || word.Value == "未処理語" || word.Value == "生成AI" {
			t.Fatalf("invalid or static duplicate term leaked into fresh pool: %+v", word)
		}
	}

	stale := *cache
	stale.Date = "2026-08-03"
	if got := buildFreshTopicWords(&stale, now); len(got) != 0 {
		t.Fatalf("stale cache produced fresh words: %+v", got)
	}
}

func TestFreshWordContextIsCarriedIntoTopicSeedAndDialogueGuidance(t *testing.T) {
	selected := []topicWord{{
		Value: "エージェント型AI", Kind: topicWordKindFresh,
		Context: "目標に沿って複数手順を進めるAIです。",
	}}
	seed, ok := buildTopicSeedForStrategyWithWords(StrategySingleGenre, selected)
	if !ok || seed.Genre1Context == "" || seed.Genre1Kind != topicWordKindFresh {
		t.Fatalf("fresh seed context = %+v ok=%v", seed, ok)
	}
	guidance := formatTopicGenerationContext(TopicGenerationResult{Seed: seed})
	if guidance == "" || !strings.Contains(guidance, "補足にない事実を推測で追加しない") {
		t.Fatalf("fresh word guidance = %q", guidance)
	}
}

func TestChooseTopicWordsPrefersFreshAndPreventsClassicConsecutiveUse(t *testing.T) {
	staticWords := []topicWord{
		{Value: "生成AI", Kind: topicWordKindStatic},
		{Value: "落語", Kind: topicWordKindStatic, Classic: true},
		{Value: "防災", Kind: topicWordKindStatic},
	}
	freshWords := []topicWord{{Value: "エージェント型AI", Kind: topicWordKindFresh}}

	selected := chooseTopicWords(StrategySingleGenre, staticWords, freshWords, nil, false, func(int) int { return 0 })
	if len(selected) != 1 || selected[0].Kind != topicWordKindFresh {
		t.Fatalf("single did not prefer fresh word: %+v", selected)
	}
	selected = chooseTopicWords(StrategySingleGenre, staticWords, freshWords, nil, false, func(int) int { return 99 })
	if len(selected) != 1 || selected[0].Kind != topicWordKindStatic {
		t.Fatalf("single did not keep the 30 percent static branch: %+v", selected)
	}

	selected = chooseTopicWords(StrategyDoubleGenre, staticWords, freshWords, []string{"生成AI"}, true, func(int) int { return 0 })
	if len(selected) != 2 || selected[0].Kind != topicWordKindFresh || selected[1].Value != "防災" {
		t.Fatalf("double selection ignored fresh/static or cooldown rules: %+v", selected)
	}

	recent := appendRecentTopicWords([]string{"A", "B"}, selected, recentTopicWordLimit)
	if len(recent) != 4 || recent[len(recent)-2] != "エージェント型AI" || recent[len(recent)-1] != "防災" {
		t.Fatalf("recent word history = %#v", recent)
	}
}
