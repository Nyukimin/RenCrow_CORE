package backlog

import (
	"errors"
	"testing"
)

func TestProjectLegacyOpenAndProposalReviewAreCandidates(t *testing.T) {
	for _, status := range []string{"open", StatusProposalReview} {
		item := ProjectLegacy(Item{ItemID: "legacy-" + status, Title: "legacy", Status: status})
		if item.ConceptState != ConceptCandidate || item.DeliveryState != DeliveryNone {
			t.Fatalf("status %q projected to %+v", status, item)
		}
	}
}

func TestTransitionDeliveryRejectsForgedCheckOK(t *testing.T) {
	item := ProjectLegacy(Item{ItemID: "legacy", Title: "legacy", Status: "ok", CheckOK: true})
	if item.DeliveryState == DeliveryLiveVerified || item.DeliveryState == DeliveryDone {
		t.Fatalf("legacy check_ok forged completion: %+v", item)
	}
	item.SchemaVersion = SchemaVersion2
	item.ConceptState = ConceptAdopted
	item.DeliveryState = DeliveryPostDeployVerify
	item.CheckOK = true
	if _, err := TransitionDelivery(item, DeliveryLiveVerified, nil); !errors.Is(err, ErrEvidenceRequired) {
		t.Fatalf("missing live evidence err=%v", err)
	}
}

func TestDeliveryTransitionsRequireEvidenceAndAreAdjacent(t *testing.T) {
	item := Item{SchemaVersion: SchemaVersion2, ItemID: "u", Title: "unit", ConceptState: ConceptAdopted, DeliveryState: DeliveryQueued}
	if _, err := TransitionDelivery(item, DeliveryBuild, []EvidenceRef{{Stage: DeliveryBuild, Ref: "build-1", Passed: true}}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected adjacent transition error, got %v", err)
	}
	next, err := TransitionDelivery(item, DeliverySpec, []EvidenceRef{{Kind: "spec", Ref: "spec-1", Passed: true}})
	if err != nil || next.DeliveryState != DeliverySpec {
		t.Fatalf("spec transition failed: next=%+v err=%v", next, err)
	}
}

func TestLiveVerifiedRequiresCumulativeStageEvidence(t *testing.T) {
	item := Item{
		SchemaVersion: SchemaVersion2, ItemID: "cumulative", Title: "unit",
		ConceptState: ConceptAdopted, DeliveryState: DeliveryPostDeployVerify,
	}
	if _, err := TransitionDelivery(item, DeliveryLiveVerified, []EvidenceRef{{Stage: DeliveryLiveVerified, Ref: "live-only", Passed: true}}); !errors.Is(err, ErrEvidenceRequired) {
		t.Fatalf("single live ref must not pass cumulative gate: %v", err)
	}
	refs := []EvidenceRef{
		{Stage: DeliverySpec, Ref: "spec", Passed: true},
		{Stage: DeliveryTDDRed, Ref: "red", Passed: true},
		{Stage: DeliveryTDDGreen, Ref: "green", Passed: true},
		{Stage: DeliveryRefactor, Ref: "refactor", Passed: true},
		{Stage: DeliveryE2EPredeploy, Ref: "e2e", Passed: true},
		{Stage: DeliveryBuild, Ref: "build", Passed: true},
		{Stage: DeliveryDeploy, Ref: "deploy", Passed: true},
		{Stage: DeliveryRestart, Ref: "restart", Passed: true},
		{Stage: DeliveryPostDeployVerify, Ref: "readiness", Passed: true},
		{Stage: DeliveryLiveVerified, Ref: "live", Passed: true},
	}
	next, err := TransitionDelivery(item, DeliveryLiveVerified, refs)
	if err != nil || next.DeliveryState != DeliveryLiveVerified || !next.CheckOK {
		t.Fatalf("complete cumulative evidence rejected: next=%+v err=%v", next, err)
	}
}
