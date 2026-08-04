package idlechat

import (
	"strings"
	"testing"
)

func TestValidateStoryEpisodeAcceptsReaderAndListenerPerformance(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	got := ValidateStoryEpisode(artifact, StorySemanticReview{Valid: true})
	if !got.Valid || len(got.Errors) != 0 {
		t.Fatalf("validation=%+v, want valid", got)
	}
}

func TestValidateStoryEpisodeRejectsReaderSwap(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	artifact.Turns[2].Speaker = artifact.Listener
	got := ValidateStoryEpisode(artifact, StorySemanticReview{Valid: true})
	assertStoryValidationCode(t, got, "story_performance_violation")
}

func TestValidateStoryEpisodeRejectsBrokenReading(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	artifact.Ledger.Entities = []StoryLedgerEntity{{ID: "hero", Name: "正一", Reading: "しょういち", Role: "主人公"}}
	artifact.Turns[0].DisplayText = "正一は扉を開けた。"
	artifact.Turns[0].SpeechText = "まさいちは扉を開けた。"
	got := ValidateStoryEpisode(artifact, StorySemanticReview{Valid: true})
	assertStoryValidationCode(t, got, "reading_violation")
}

func TestValidateStoryEpisodeDoesNotTreatOneRuneNameInsideCompoundAsEntity(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	artifact.Ledger.Entities = append(artifact.Ledger.Entities, StoryLedgerEntity{ID: "turtle", Name: "亀", Reading: "かめ", Role: "案内役"})
	artifact.Turns[0].DisplayText += " 防波堤の亀裂を調べた。"
	artifact.Turns[0].SpeechText += " 防波堤のきれつを調べた。"
	got := ValidateStoryEpisode(artifact, StorySemanticReview{Valid: true})
	if !got.Valid {
		t.Fatalf("compound substring must not require entity reading: %+v", got)
	}
}

func TestValidateStoryEpisodeRejectsLexicalCorruption(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	artifact.Turns[0].DisplayText = "記憶晶�が光った。"
	got := ValidateStoryEpisode(artifact, StorySemanticReview{Valid: true})
	assertStoryValidationCode(t, got, "lexical_corruption")
}

func TestValidateStoryEpisodeIncludesSemanticContinuityFailure(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	review := StorySemanticReview{Valid: false, Errors: []StoryValidationError{{
		Code: "entity_relation_violation", TurnIndex: 5, Evidence: "兄として登録された人物を父と呼んでいる",
	}}}
	got := ValidateStoryEpisode(artifact, review)
	assertStoryValidationCode(t, got, "entity_relation_violation")
	if got.FirstInvalidTurn != 5 {
		t.Fatalf("first_invalid_turn=%d, want 5", got.FirstInvalidTurn)
	}
}

func validStoryEpisodeFixture() StoryEpisodeArtifact {
	narration := strings.Repeat("桃から生まれた主人公は、村を守るため仲間と鬼ヶ島へ向かった。", 4)
	return StoryEpisodeArtifact{
		SchemaVersion: StoryEpisodeSchemaVersion,
		EpisodeID:     "episode-1",
		Revision:      1,
		EpisodeKind:   StoryEpisodeKind,
		GenerationID:  "generation-1",
		Source:        StoryEpisodeSource{Title: "桃太郎", Synopsis: "桃から生まれた子が仲間と鬼ヶ島へ向かう"},
		Reader:        "mio",
		Listener:      "shiro",
		Contract: StoryEpisodeContract{
			TransformationAxis: "鬼側の新人職員を主人公にする",
			Genre:              "near_future_sf",
			InterestDirection:  "funny",
			InterestContract:   []string{"前振り", "段階的な誇張", "オチ"},
			ContentMode:        "free",
		},
		Ledger: StoryEpisodeLedger{
			Entities:   []StoryLedgerEntity{{ID: "hero", Name: "桃太郎", Reading: "ももたろう", Role: "元話の主人公"}},
			WorldRules: []string{"死者は説明なく復活しない"},
		},
		Turns: []StoryEpisodeTurn{
			{TurnIndex: 1, Speaker: "mio", UtteranceRole: StoryUtteranceNarration, DisplayText: narration, SpeechText: narration},
			{TurnIndex: 2, Speaker: "mio", UtteranceRole: StoryUtteranceNarration, DisplayText: narration, SpeechText: narration},
			{TurnIndex: 3, Speaker: "mio", UtteranceRole: StoryUtteranceNarration, DisplayText: narration, SpeechText: narration},
			{TurnIndex: 4, Speaker: "shiro", UtteranceRole: StoryUtteranceInterjection, ReactsTo: 3, DisplayText: "そこから営業するのか……。", SpeechText: "そこから営業するのか……。"},
			{TurnIndex: 5, Speaker: "mio", UtteranceRole: StoryUtteranceNarration, DisplayText: narration, SpeechText: narration},
			{TurnIndex: 6, Speaker: "mio", UtteranceRole: StoryUtteranceNarration, DisplayText: narration, SpeechText: narration},
			{TurnIndex: 7, Speaker: "mio", UtteranceRole: StoryUtteranceNarration, DisplayText: narration, SpeechText: narration},
		},
	}
}

func assertStoryValidationCode(t *testing.T, got StoryValidationResult, want string) {
	t.Helper()
	if got.Valid {
		t.Fatalf("validation=%+v, want invalid code %s", got, want)
	}
	for _, item := range got.Errors {
		if item.Code == want {
			return
		}
	}
	t.Fatalf("validation=%+v, missing code %s", got, want)
}
