package speecheval

import (
	"fmt"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
)

func TestAllCasesContainTwentyValidFixedJapanesePlansAndNeutralClones(t *testing.T) {
	cases := AllCases()
	if len(cases) != 20 {
		t.Fatalf("AllCases() returned %d cases, want 20", len(cases))
	}
	seen := make(map[string]bool, len(cases))
	applicable := 0
	for i, item := range cases {
		wantID := fmt.Sprintf("%02d", i+1)
		if item.ID != wantID || seen[item.ID] {
			t.Fatalf("case[%d].ID = %q, want unique %q", i, item.ID, wantID)
		}
		seen[item.ID] = true
		if item.Text == "" || item.Intent == "" {
			t.Errorf("case[%s] is missing fixed text or intent", item.ID)
		}
		normalized := speech.Normalize(item.Plan)
		if !normalized.Valid {
			t.Errorf("case[%s] SpeechPlan invalid: %s", item.ID, normalized.FallbackReason)
		}
		if normalized.DisplayText != item.Text {
			t.Errorf("case[%s] text = %q, plan display text = %q", item.ID, item.Text, normalized.DisplayText)
		}
		hasCue := testPlanHasCue(item.Plan)
		if item.CueApplicable {
			applicable++
			if !hasCue {
				t.Errorf("case[%s] declares a cue applicable without an expressive cue", item.ID)
			}
		} else if hasCue {
			t.Errorf("case[%s] has a cue while cue_applicable is false", item.ID)
		}

		neutral := NeutralClone(item.Plan)
		if got := speech.Normalize(neutral); !got.Valid || got.DisplayText != item.Text {
			t.Errorf("case[%s] neutral clone invalid or changed text: valid=%t display=%q", item.ID, got.Valid, got.DisplayText)
		}
		for utteranceIndex, utterance := range neutral.Utterances {
			if utterance.Delivery != (speech.Delivery{Tone: "neutral", Attitude: "neutral", Energy: "medium", Pace: "normal", Mode: "dialogue"}) {
				t.Errorf("case[%s] neutral delivery[%d] = %#v", item.ID, utteranceIndex, utterance.Delivery)
			}
			if utterance.PauseAfterMS != 0 {
				t.Errorf("case[%s] neutral pause[%d] = %d, want 0", item.ID, utteranceIndex, utterance.PauseAfterMS)
			}
			for _, segment := range utterance.Segments {
				if len(segment.Before) != 0 || len(segment.After) != 0 {
					t.Errorf("case[%s] neutral clone retained cues: %#v", item.ID, segment)
				}
			}
		}
	}
	if applicable == 0 || applicable == len(cases) {
		t.Fatalf("cue-applicable mix = %d of %d, want both applicable and non-applicable cases", applicable, len(cases))
	}
}

func TestBuildPairKeepsIdentityAndTextForBothVariants(t *testing.T) {
	cases := AllCases()
	if len(cases) == 0 {
		t.Fatal("AllCases() returned no cases")
	}
	item := cases[0]
	got := BuildPair(item, "session-fixed", "turn-fixed")
	if got.SessionID != "session-fixed" || got.TurnID != "turn-fixed" {
		t.Fatalf("pair identity = session %q turn %q", got.SessionID, got.TurnID)
	}
	if speech.Normalize(got.NeutralPlan).DisplayText != item.Text || speech.Normalize(got.ExpressivePlan).DisplayText != item.Text {
		t.Fatalf("pair text changed: neutral=%q expressive=%q want %q", speech.Normalize(got.NeutralPlan).DisplayText, speech.Normalize(got.ExpressivePlan).DisplayText, item.Text)
	}
}

func testPlanHasCue(plan speech.SpeechPlan) bool {
	for _, utterance := range plan.Utterances {
		for _, segment := range utterance.Segments {
			if len(segment.Before) > 0 || len(segment.After) > 0 {
				return true
			}
		}
	}
	return false
}
