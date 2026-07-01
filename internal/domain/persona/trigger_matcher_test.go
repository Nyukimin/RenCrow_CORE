package persona

import "testing"

func TestMatchTriggerSelectsMostSpecificTrigger(t *testing.T) {
	definitions := []TriggerDefinition{
		{
			TriggerID:   "mio_tired",
			CharacterID: "mio",
			Category:    "tiredness",
			Keywords:    []string{"疲れた"},
			Priority:    1,
		},
		{
			TriggerID:   "mio_tired_blocked",
			CharacterID: "mio",
			Category:    "tiredness",
			Keywords:    []string{"疲れた", "進まない"},
			Priority:    1,
		},
	}

	match, ok := MatchTrigger("今日は疲れたし、作業が進まない", definitions)

	if !ok {
		t.Fatal("expected trigger match")
	}
	if match.TriggerID != "mio_tired_blocked" || match.Confidence != 1 {
		t.Fatalf("match=%#v", match)
	}
}

func TestMatchTriggerUsesPriorityWhenConfidenceTies(t *testing.T) {
	definitions := []TriggerDefinition{
		{
			TriggerID:   "mio_soft",
			CharacterID: "mio",
			Category:    "warm",
			Keywords:    []string{"ありがとう"},
			Priority:    1,
		},
		{
			TriggerID:   "mio_thanks",
			CharacterID: "mio",
			Category:    "thanks",
			Keywords:    []string{"ありがとう"},
			Priority:    10,
		},
	}

	match, ok := MatchTrigger("ありがとう", definitions)

	if !ok {
		t.Fatal("expected trigger match")
	}
	if match.TriggerID != "mio_thanks" {
		t.Fatalf("match=%#v", match)
	}
}

func TestCanUseCanonicalResponseRespectsCooldownMaxAndContext(t *testing.T) {
	policy := CanonicalResponsePolicy{
		ResponseID:       "kuro_destructive_block",
		CooldownTurns:    3,
		MaxPerSession:    2,
		RequiredContexts: []string{"danger", "destructive"},
	}
	if !CanUseCanonicalResponse(policy, nil, []string{"danger", "destructive"}) {
		t.Fatal("expected response to be allowed before recent use")
	}
	recent := []CanonicalResponseLog{
		{ResponseID: "kuro_destructive_block", Used: true},
		{ResponseID: "other", Used: true},
	}
	if CanUseCanonicalResponse(policy, recent, []string{"danger", "destructive"}) {
		t.Fatal("expected cooldown to block recent canonical response")
	}
	recent = []CanonicalResponseLog{
		{ResponseID: "kuro_destructive_block", Used: true},
		{ResponseID: "other", Used: true},
		{ResponseID: "other", Used: true},
		{ResponseID: "other", Used: true},
		{ResponseID: "kuro_destructive_block", Used: true},
		{ResponseID: "other", Used: true},
		{ResponseID: "other", Used: true},
		{ResponseID: "other", Used: true},
	}
	if CanUseCanonicalResponse(policy, recent, []string{"danger", "destructive"}) {
		t.Fatal("expected max per session to block canonical response")
	}
	if CanUseCanonicalResponse(policy, nil, []string{"danger"}) {
		t.Fatal("expected missing required context to block canonical response")
	}
}
