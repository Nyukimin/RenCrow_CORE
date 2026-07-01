package idlechat

import (
	"errors"
	"strings"
	"testing"
)

func TestDialogueQualityCheckerRejectsMetaLeak(t *testing.T) {
	checker := NewDialogueQualityChecker(DefaultDialogueInterestingnessConfig())
	state := DialogueArcState{Category: TopicCategoryExternal, Topic: "地下鉄博物館に残る音声案内と織物の記録性"}
	plan := DialogueTurnPlan{TurnIndex: 2, Phase: "development", RequiredMove: "素材とジャンルの接点を出す"}

	result := checker.Check(DialogueQualityInput{
		Category:    TopicCategoryExternal,
		Utterance:   "Wikipediaの記事から見ると、音声案内と織物は記録媒体として似ています。",
		LatestOther: "地下鉄博物館の案内音声って、古い布みたいに時間を抱えていそう。",
		State:       state,
		TurnPlan:    plan,
	})

	if result.OK {
		t.Fatalf("meta leak should be rejected: %#v", result)
	}
	if !containsDialogueReason(result.Reasons, DialogueMetaLeak) {
		t.Fatalf("expected meta leak reason, got %#v", result.Reasons)
	}
}

func TestDialogueQualityCheckerRequiresUptakeAndCategoryAxis(t *testing.T) {
	checker := NewDialogueQualityChecker(DefaultDialogueInterestingnessConfig())
	state := DialogueArcState{Category: TopicCategoryNews, Topic: "新しい医療制度の検討が、現場の判断に与える影響"}
	plan := DialogueTurnPlan{TurnIndex: 3, Phase: "development", RequiredMove: "論点または背景を出す"}

	result := checker.Check(DialogueQualityInput{
		Category:    TopicCategoryNews,
		Utterance:   "古書店の棚に残った封筒は、誰かの記憶みたいですね。",
		LatestOther: "医療現場では、判断を急がされる人が増えそうです。",
		State:       state,
		TurnPlan:    plan,
	})

	if result.OK {
		t.Fatalf("unrelated utterance should be rejected: %#v", result)
	}
	for _, want := range []IdleDialogueQualityReason{DialogueNoUptake, DialogueCategoryAxisMissing} {
		if !containsDialogueReason(result.Reasons, want) {
			t.Fatalf("expected %s in reasons: %#v", want, result.Reasons)
		}
	}
}

func TestDialogueQualityCheckerAcceptsCategorySpecificProgress(t *testing.T) {
	checker := NewDialogueQualityChecker(DefaultDialogueInterestingnessConfig())
	state := DialogueArcState{Category: TopicCategoryForecast, Topic: "AI技術が、個人の記憶整理をどう変えるか"}
	plan := DialogueTurnPlan{TurnIndex: 5, Phase: "deepening", RequiredMove: "影響を受ける主体を出す"}

	result := checker.Check(DialogueQualityInput{
		Category:    TopicCategoryForecast,
		Utterance:   "その記憶整理の変化は、介護記録を書く家族にまず影響しそうです。楽になる分、何を残さないかの判断も分岐します。",
		LatestOther: "個人の記憶整理って、写真を選ぶ場面から変わりそうです。",
		State:       state,
		TurnPlan:    plan,
	})

	if !result.OK {
		t.Fatalf("valid forecast utterance rejected: %#v", result)
	}
	if result.Score < MinDialogueQualityScore {
		t.Fatalf("score = %d, want >= %d", result.Score, MinDialogueQualityScore)
	}
}

func TestBuildDialogueRetryPromptIncludesRequiredMove(t *testing.T) {
	prompt := BuildDialogueRetryPrompt(DialogueTurnPlan{RequiredMove: "共通構造の仮説を出す"}, DialogueQualityResult{
		Reasons: []IdleDialogueQualityReason{DialogueNoUptake, DialogueCategoryAxisMissing},
	})
	for _, want := range []string{"自然な会話文", "共通構造の仮説を出す", "内部メタ", "dialogue_no_uptake"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("retry prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestDialogueQualityErrorWrapsInvalidResponse(t *testing.T) {
	err := dialogueQualityError(DialogueQualityResult{Reasons: []IdleDialogueQualityReason{DialogueTooGeneric}})
	if !errors.Is(err, errIdleInvalidResponse) {
		t.Fatalf("quality error should wrap errIdleInvalidResponse: %v", err)
	}
}
