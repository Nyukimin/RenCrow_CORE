package idlechat

import (
	"strings"
	"testing"
)

func TestDialogueDirectorBuildsCategoryArcPlan(t *testing.T) {
	director := NewDialogueDirector(DefaultDialogueInterestingnessConfig())
	result := TopicGenerationResult{
		Topic:               "盆栽と都市計画に共通する、成長を待つための設計",
		Category:            TopicCategoryDouble,
		Strategy:            "double",
		InterestingnessAxis: "接続",
		OpeningHook:         "成長を待つ設計の共通点を拾う",
		Avoid:               "似ている点の列挙だけで終わらせない",
	}

	plan := director.BuildArcPlan(result)

	if plan.Category != TopicCategoryDouble || plan.Strategy != "double" {
		t.Fatalf("plan category/strategy = %q/%q", plan.Category, plan.Strategy)
	}
	if !strings.Contains(plan.CoreQuestion, "共通構造") {
		t.Fatalf("double core question should focus common structure: %q", plan.CoreQuestion)
	}
	if len(plan.TurnPlans) != 12 {
		t.Fatalf("turn plans = %d, want 12", len(plan.TurnPlans))
	}
	if got := plan.TurnPlans[0].Phase; got != "opening" {
		t.Fatalf("turn 1 phase = %q", got)
	}
	if got := plan.TurnPlans[5].Phase; got != "deepening" {
		t.Fatalf("turn 6 phase = %q", got)
	}
	if got := plan.TurnPlans[10].Phase; got != "closing" {
		t.Fatalf("turn 11 phase = %q", got)
	}
	if !strings.Contains(plan.TurnPlans[3].RequiredMove, "共通構造") {
		t.Fatalf("turn 4 should require double category move: %q", plan.TurnPlans[3].RequiredMove)
	}
	if plan.SpeakerRoles["mio"].PrimaryMove == "" || plan.SpeakerRoles["shiro"].PrimaryMove == "" {
		t.Fatalf("speaker roles were not populated: %#v", plan.SpeakerRoles)
	}
}

func TestBuildDialoguePromptUsesArcPlanAndInternalMeta(t *testing.T) {
	director := NewDialogueDirector(DefaultDialogueInterestingnessConfig())
	result := TopicGenerationResult{
		Topic:               "「雨上がりの映写室」ってどんな映画？",
		Category:            TopicCategoryMovie,
		Strategy:            "movie",
		InterestingnessAxis: "共同妄想",
		OpeningHook:         "映像の質感から入る",
		Avoid:               "あらすじを一気に説明しない",
	}
	plan := director.BuildArcPlan(result)
	state := director.NewArcState("idle-dialogue-plan", result, plan)
	prompt := BuildDialoguePrompt(DialoguePromptInput{
		Result:             result,
		Plan:               plan,
		State:              state,
		TurnPlan:           plan.TurnPlans[0],
		Speaker:            "mio",
		PreviousUtterances: []string{"shiro: まだ誰も映写室に入っていない感じがします。"},
	})

	for _, want := range []string{
		"topic: 「雨上がりの映写室」ってどんな映画？",
		"category: movie",
		"interestingness_axis: 共同妄想",
		"required_move:",
		"映像の質感から入る",
		"あらすじを一気に説明しない",
		"発話本文のみ",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("dialogue prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestUpdateDialogueArcStateRecordsMovesAndSignals(t *testing.T) {
	director := NewDialogueDirector(DefaultDialogueInterestingnessConfig())
	result := TopicGenerationResult{Topic: "AI技術が、個人の記憶整理をどう変えるか", Category: TopicCategoryForecast, Strategy: "forecast", InterestingnessAxis: "変化の分岐"}
	plan := director.BuildArcPlan(result)
	state := director.NewArcState("idle-forecast", result, plan)
	quality := DialogueQualityResult{OK: true, Score: 82}

	state = director.UpdateArcState(state, "その変化は、家族写真を選ぶ仕事をAIが手伝う場面から始まりそうです。便利だけど、何を残すかの判断は揺れます。", plan.TurnPlans[0], quality)

	if state.TurnIndex != 1 {
		t.Fatalf("turn index = %d, want 1", state.TurnIndex)
	}
	if len(state.UsedMoves) != 1 || state.UsedMoves[0] != plan.TurnPlans[0].RequiredMove {
		t.Fatalf("used moves not updated: %#v", state.UsedMoves)
	}
	if len(state.ConcreteAnchors) == 0 {
		t.Fatalf("concrete anchors should be extracted: %#v", state)
	}
	if len(state.TensionPoints) == 0 {
		t.Fatalf("tension points should include judgement difficulty: %#v", state)
	}
}

func TestIdleChatTurnLimitUsesDialogueConfig(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, nil, []string{"mio", "shiro"}, 5, 12, 0.7, nil, "")
	o.SetDialogueInterestingnessConfig(DialogueInterestingnessConfig{
		Enabled:          true,
		MaxTurnsPerTopic: 8,
		MinQualityScore:  70,
		Utterance: DialogueUtteranceConfig{
			MinRunes:              20,
			MaxRunes:              160,
			PreferredMaxSentences: 2,
		},
	})

	if got := o.idleChatTurnLimit(); got != 8 {
		t.Fatalf("idleChatTurnLimit() = %d, want dialogue max turns", got)
	}
}
