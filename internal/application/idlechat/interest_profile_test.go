package idlechat

import (
	"strings"
	"testing"
)

func TestIdleInterestProfileForTopicSwitchesByTopic(t *testing.T) {
	tests := []struct {
		name      string
		topic     string
		topicType string
		interest  string
	}{
		{
			name:      "tech operations",
			topic:     "RenCrow の Git 運用と CLI テスト設計",
			topicType: "技術・運用",
			interest:  "構造と対比",
		},
		{
			name:      "movie story",
			topic:     "架空映画「青い配線」のラストを考える",
			topicType: "物語・映画",
			interest:  "展開と感情",
		},
		{
			name:      "daily chat",
			topic:     "休日の散歩と帰り道のごはん",
			topicType: "日常・雑談",
			interest:  "具体と小さな意外性",
		},
		{
			name:      "forecast",
			topic:     "生成AI市場の今後と社会への影響",
			topicType: "ニュース・未来予測",
			interest:  "因果と生活への影響",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := idleInterestProfileForTopic(tt.topic)
			if got.TopicType != tt.topicType {
				t.Fatalf("TopicType = %q, want %q", got.TopicType, tt.topicType)
			}
			if got.Name != tt.interest {
				t.Fatalf("Name = %q, want %q", got.Name, tt.interest)
			}
		})
	}
}

func TestBuildIdleTurnPromptIncludesTopicInterestProfile(t *testing.T) {
	got := buildIdleTurnPrompt("RenCrow の Git 運用を整理する", "shiro", "自動化しすぎるのは怖いね。", "人間がpushする線引きが大事。", 3, 3, false)

	for _, want := range []string{
		"RenCrow の Git 運用を整理する",
		"直前の相手発言",
		"読者の楽しみ",
		"実際に動かす時の落とし穴",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt does not contain %q:\n%s", want, got)
		}
	}
}

func TestIdleAudienceAngleForProfileUsesTopicSpecificAngles(t *testing.T) {
	profile := idleInterestProfileForTopic("休日の散歩と帰り道のごはん")
	got := idleAudienceAngleForProfile(0, false, false, false, profile)
	if got != "その場面がすぐ浮かぶこと" {
		t.Fatalf("angle = %q", got)
	}
}

func TestNormalizeIdleTopicKeepsFullNonMovieTopic(t *testing.T) {
	raw := "話題: うーん、これは考えるのが楽しい課題だね！「医学」「賃金」「港町の倉庫街」を組み合わせるなんて、全部が遠そうで妙につながる感じがある"

	got := normalizeIdleTopic(raw, false)

	if strings.Contains(got, "...") {
		t.Fatalf("topic should not be truncated: %q", got)
	}
	if !strings.Contains(got, "港町の倉庫街") || !strings.Contains(got, "妙につながる感じがある") {
		t.Fatalf("topic lost expected content: %q", got)
	}
}

func TestNormalizeIdleTopicRemovesMioReactionFromTopic(t *testing.T) {
	raw := "えー！郵便と古書店って組み合わせ、めっちゃエモいじゃん！なんか物語になりそう〜✨"

	got := normalizeIdleTopic(raw, false)

	if got != "郵便と古書店" {
		t.Fatalf("normalizeIdleTopic() = %q, want %q", got, "郵便と古書店")
	}
}
