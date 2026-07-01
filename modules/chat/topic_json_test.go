package chat

import (
	"errors"
	"testing"
)

func TestParseTopicCandidatesExtractsFencedJSON(t *testing.T) {
	raw := "```json\n{\"candidates\":[{\"topic\":\"「雨上がりの映写室」ってどんな映画？\",\"interestingness_axis\":\"共同妄想\"}]}\n```"
	candidates, err := ParseTopicCandidates(raw)
	if err != nil {
		t.Fatalf("ParseTopicCandidates error: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Topic != "「雨上がりの映写室」ってどんな映画？" {
		t.Fatalf("unexpected candidates: %+v", candidates)
	}
}

func TestParseTopicCandidatesAcceptsTopicStringArray(t *testing.T) {
	raw := `{"candidates":["古びた醤油壺が置かれた台所で、老婆と孫が語る味の変化"]}`
	candidates, err := ParseTopicCandidates(raw)
	if err != nil {
		t.Fatalf("ParseTopicCandidates error: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Topic != "古びた醤油壺が置かれた台所で、老婆と孫が語る味の変化" {
		t.Fatalf("unexpected candidates: %+v", candidates)
	}
	if candidates[0].InterestingnessAxis != "" || candidates[0].OpeningHook != "" || candidates[0].Avoid != "" {
		t.Fatalf("candidate should only contain topic: %+v", candidates[0])
	}
}

func TestParseTopicJudgeResultNormalizesTotal(t *testing.T) {
	raw := `{"winner_topic":"盆栽と都市計画に共通する、成長を待つための設計","scores":[{"topic":"盆栽と都市計画に共通する、成長を待つための設計","category_fit":4,"concreteness":4,"curiosity":4,"conversation_potential":4,"axis_strength":4,"novelty":4,"safety":4,"total":0}]}`
	judge, err := ParseTopicJudgeResult(raw)
	if err != nil {
		t.Fatalf("ParseTopicJudgeResult error: %v", err)
	}
	if judge.Scores[0].Total != 28 {
		t.Fatalf("total = %d, want 28", judge.Scores[0].Total)
	}
}

func TestParseTopicCandidatesRejectsInvalidJSON(t *testing.T) {
	_, err := ParseTopicCandidates("not json")
	if !errors.Is(err, ErrTopicGenerationInvalidJSON) {
		t.Fatalf("expected invalid json, got %v", err)
	}
}
