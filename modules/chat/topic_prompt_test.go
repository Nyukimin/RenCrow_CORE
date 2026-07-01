package chat

import (
	"errors"
	"strings"
	"testing"
)

func TestBuildTopicGenerationPromptIncludesCategoryRulesAndRetry(t *testing.T) {
	seed := TopicSeed{Category: TopicCategoryDouble, Genre1: "潮汐", Genre2: "郵便制度"}
	prompt, err := BuildTopicGenerationPrompt(TopicCategoryDouble, seed, []RecentTopic{{Topic: "古い話題"}}, 2, 2, errors.New("topic_contract_violation"))
	if err != nil {
		t.Fatalf("BuildTopicGenerationPrompt error: %v", err)
	}
	for _, want := range []string{
		"candidates 配列に 2 件",
		"topic 文字列だけの配列",
		"category = double",
		"seed.genre_1 と seed.genre_2 の両方",
		"再生成条件",
		"topic_contract_violation",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "opening_hook") || strings.Contains(prompt, "rationale") || strings.Contains(prompt, "interestingness_axis") {
		t.Fatalf("generation prompt should not request shaping fields:\n%s", prompt)
	}
}

func TestBuildTopicJudgePromptIncludesCandidates(t *testing.T) {
	prompt, err := BuildTopicJudgePrompt(
		TopicCategoryMovie,
		TopicSeed{Category: TopicCategoryMovie},
		nil,
		[]TopicCandidate{{Topic: "「雨上がりの映写室」ってどんな映画？"}},
	)
	if err != nil {
		t.Fatalf("BuildTopicJudgePrompt error: %v", err)
	}
	for _, want := range []string{"topic judge", "winner_topic", "雨上がりの映写室", "movie=共同妄想"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("judge prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestRenderTopicPromptPlaceholders(t *testing.T) {
	got := RenderTopicPromptPlaceholders("category={category} count={candidate_count}", map[string]string{
		"category":        "news",
		"candidate_count": "5",
	})
	if got != "category=news count=5" {
		t.Fatalf("rendered = %q", got)
	}
}
