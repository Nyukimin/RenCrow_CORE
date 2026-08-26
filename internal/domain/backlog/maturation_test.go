package backlog

import (
	"errors"
	"testing"
)

func TestMaturationStateTransitionsKeepConceptAndDeliveryOrthogonal(t *testing.T) {
	for _, state := range []string{
		MaturationStateMaturation,
		MaturationStateRevalidation,
		MaturationStatePromoted,
		MaturationStateMerged,
		MaturationStateHold,
		MaturationStateDropped,
	} {
		if err := ValidateMaturationState(state); err != nil {
			t.Fatalf("state %q rejected: %v", state, err)
		}
	}
	item := Item{
		SchemaVersion: SchemaVersion2, ItemID: "maturation-domain", Title: "domain",
		ConceptState: ConceptCandidate, DeliveryState: DeliveryNone,
		MaturationState: MaturationStateMaturation,
	}
	next, err := TransitionMaturation(item, MaturationStatePromoted)
	if err != nil {
		t.Fatal(err)
	}
	if next.MaturationState != MaturationStatePromoted || next.ConceptState != ConceptCandidate || next.DeliveryState != DeliveryNone {
		t.Fatalf("orthogonal state changed unexpectedly: %+v", next)
	}
	if _, err := TransitionMaturation(next, MaturationStateMerged); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal PROMOTED transition unexpectedly accepted: %v", err)
	}
}

func TestMaturationTransitionAllowsHoldRevalidationAndMaterialReset(t *testing.T) {
	for _, pair := range [][2]string{
		{MaturationStateMaturation, MaturationStateRevalidation},
		{MaturationStateRevalidation, MaturationStateHold},
		{MaturationStateHold, MaturationStatePromoted},
		{MaturationStatePromoted, MaturationStateMaturation},
	} {
		if err := ValidateMaturationTransition(pair[0], pair[1]); err != nil {
			t.Fatalf("transition %s -> %s rejected: %v", pair[0], pair[1], err)
		}
	}
	for _, terminal := range []string{MaturationStateMerged, MaturationStateDropped} {
		if err := ValidateMaturationTransition(terminal, MaturationStateMaturation); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("terminal state %s can be reset: %v", terminal, err)
		}
	}
}

func TestValidateRevalidationRecordRequiresAuditFields(t *testing.T) {
	base := RevalidationRecord{
		BacklogID: "atlas-1", RevalidationDate: "2026-08-26T00:00:00Z", MaturationDays: 7,
		Decision: RevalidationDecisionPromote, Reason: "still valuable", ReviewAgents: []string{"Mio"},
	}
	if err := ValidateRevalidationRecord(base); err != nil {
		t.Fatal(err)
	}
	cases := []RevalidationRecord{
		func() RevalidationRecord { value := base; value.Decision = "UNKNOWN"; return value }(),
		func() RevalidationRecord { value := base; value.Reason = ""; return value }(),
		func() RevalidationRecord { value := base; value.ReviewAgents = nil; return value }(),
		func() RevalidationRecord { value := base; value.Decision = RevalidationDecisionMerge; return value }(),
		func() RevalidationRecord {
			value := base
			value.Forced = true
			value.BypassReason = "operator_preference"
			return value
		}(),
	}
	for index, value := range cases {
		if err := ValidateRevalidationRecord(value); err == nil {
			t.Fatalf("case %d unexpectedly accepted: %+v", index, value)
		}
	}
}

func TestValidateRevalidationRecordRequiresHoldTrigger(t *testing.T) {
	record := RevalidationRecord{
		BacklogID: "hold", RevalidationDate: "2026-08-26T00:00:00Z", MaturationDays: 7,
		Decision: RevalidationDecisionHold, Reason: "dependency pending", ReviewAgents: []string{"Shiro"},
	}
	if err := ValidateRevalidationRecord(record); err == nil {
		t.Fatal("HOLD record without next_review_trigger was accepted")
	}
	record.NextReviewTrigger = "dependency-ready"
	if err := ValidateRevalidationRecord(record); err != nil {
		t.Fatalf("valid HOLD record rejected: %v", err)
	}
}

func TestValidateItemAcceptsLegacyCandidateAndAdoptedWithoutMaturation(t *testing.T) {
	for _, item := range []Item{
		{SchemaVersion: SchemaVersion2, ItemID: "legacy-candidate", Title: "candidate", ConceptState: ConceptCandidate, DeliveryState: DeliveryNone},
		{SchemaVersion: SchemaVersion2, ItemID: "legacy-adopted", Title: "adopted", ConceptState: ConceptAdopted, DeliveryState: DeliveryQueued},
	} {
		if err := ValidateItem(item); err != nil {
			t.Fatalf("legacy item rejected: %+v: %v", item, err)
		}
	}
}
