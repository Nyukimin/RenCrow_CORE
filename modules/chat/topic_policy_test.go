package chat

import (
	"errors"
	"testing"
)

func TestNormalizeTopicCategoryMapsStorySimpleToStory(t *testing.T) {
	got, err := NormalizeTopicCategory("story-simple")
	if err != nil {
		t.Fatalf("NormalizeTopicCategory returned error: %v", err)
	}
	if got != TopicCategoryStory {
		t.Fatalf("category = %q, want %q", got, TopicCategoryStory)
	}
}

func TestExternalTopicDoesNotLeakProvider(t *testing.T) {
	seed := TopicSeed{
		Category: TopicCategoryExternal,
		Genre1:   "織物",
		ExternalMaterial: &ExternalMaterialSeed{
			Title:    "地下鉄博物館",
			Provider: "Wikipedia",
			URL:      "https://example.test/wiki",
		},
	}
	candidate := TopicCandidate{
		Topic:               "Wikipediaで見つけた地下鉄博物館と織物",
		InterestingnessAxis: "偶然の意味化",
		OpeningHook:         "素材の記録性を拾う",
		Avoid:               "取得経路の説明で終わる",
	}
	err := ValidateTopicCandidate(TopicCategoryExternal, seed, candidate)
	if !errors.Is(err, ErrTopicContractViolation) {
		t.Fatalf("expected contract violation, got %v", err)
	}
}

func TestMovieTopicFormat(t *testing.T) {
	seed := TopicSeed{Category: TopicCategoryMovie}
	valid := TopicCandidate{
		Topic:               "「雨上がりの映写室」ってどんな映画？",
		InterestingnessAxis: "共同妄想",
		OpeningHook:         "映像の質感から入る",
		Avoid:               "あらすじを説明し切る",
	}
	if err := ValidateTopicCandidate(TopicCategoryMovie, seed, valid); err != nil {
		t.Fatalf("valid movie topic rejected: %v", err)
	}

	invalid := valid
	invalid.Topic = "雨上がりの映写室について"
	err := ValidateTopicCandidate(TopicCategoryMovie, seed, invalid)
	if !errors.Is(err, ErrTopicContractViolation) {
		t.Fatalf("expected movie format violation, got %v", err)
	}
}

func TestNewsTopicUsesOnlyNewsSeed(t *testing.T) {
	seed := TopicSeed{Category: TopicCategoryNews}
	candidate := TopicCandidate{
		Topic:               "新しい医療制度の検討が、現場の判断に与える影響",
		InterestingnessAxis: "現実の影響",
		OpeningHook:         "制度変更が現場判断へ落ちる点を拾う",
		Avoid:               "見出しの紹介だけで終わる",
	}
	if err := ValidateSeedForCategory(TopicCategoryNews, seed); !errors.Is(err, ErrTopicSeedUnavailable) {
		t.Fatalf("expected missing news seed, got %v", err)
	}

	seed.News = &NewsSeed{Title: "新しい医療制度の検討が始まる", Source: "NHK"}
	if err := ValidateTopicCandidate(TopicCategoryNews, seed, candidate); err != nil {
		t.Fatalf("valid news topic rejected: %v", err)
	}
	candidate.Topic = "NHKの記事から考える医療制度"
	err := ValidateTopicCandidate(TopicCategoryNews, seed, candidate)
	if !errors.Is(err, ErrTopicContractViolation) {
		t.Fatalf("expected source leak violation, got %v", err)
	}
}

func TestJudgeWinnerMustExistInCandidates(t *testing.T) {
	candidates := []TopicCandidate{
		{Topic: "盆栽と都市計画に共通する、成長を待つための設計"},
	}
	judge := TopicJudgeResult{
		WinnerTopic: "候補外のお題",
		Scores: []TopicJudgeScore{
			{Topic: "候補外のお題", CategoryFit: 5, Concreteness: 5, Curiosity: 5, ConversationPotential: 5, AxisStrength: 5, Novelty: 5, Safety: 5},
		},
	}
	_, _, err := ValidateJudgeResult(judge, candidates)
	if !errors.Is(err, ErrTopicJudgeWinnerMissing) {
		t.Fatalf("expected missing winner, got %v", err)
	}
}

func TestJudgeUsesConfiguredThresholds(t *testing.T) {
	candidates := []TopicCandidate{
		{Topic: "盆栽と都市計画に共通する、成長を待つための設計"},
	}
	judge := TopicJudgeResult{
		WinnerTopic: candidates[0].Topic,
		Scores: []TopicJudgeScore{
			{Topic: candidates[0].Topic, CategoryFit: 4, Concreteness: 4, Curiosity: 4, ConversationPotential: 4, AxisStrength: 4, Novelty: 4, Safety: 4},
		},
	}
	if _, _, err := ValidateJudgeResultWithThresholds(judge, candidates, 30, 4, 4); !errors.Is(err, ErrTopicJudgeLowScore) {
		t.Fatalf("expected configured total threshold to reject winner, got %v", err)
	}
	if _, _, err := ValidateJudgeResultWithThresholds(judge, candidates, 24, 4, 4); err != nil {
		t.Fatalf("expected configured threshold to accept winner, got %v", err)
	}
}

func TestRecentTopicSimilarityRejectsDuplicate(t *testing.T) {
	recent := []RecentTopic{{Topic: "潮汐と郵便制度に共通する、遅れて届くものの設計"}}
	err := CheckRecentTopicSimilarity("潮汐と郵便制度に共通する、遅れて届くものの設計", recent, RecentTopicSimilarityThreshold)
	if !errors.Is(err, ErrRecentTopicExactDuplicate) {
		t.Fatalf("expected exact duplicate, got %v", err)
	}
}
