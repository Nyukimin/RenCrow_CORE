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
	next, err := TransitionDelivery(item, DeliverySpec, []EvidenceRef{{Kind: "spec", Ref: "spec-1", Passed: true, Verified: true}})
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
		{Stage: DeliverySpec, Kind: "spec", Ref: "spec", Passed: true, Verified: true},
		{Stage: DeliveryTDDRed, Kind: "execution_report", Ref: "red", Passed: true, Verified: true},
		{Stage: DeliveryTDDGreen, Kind: "execution_report", Ref: "green", Passed: true, Verified: true},
		{Stage: DeliveryRefactor, Kind: "execution_report", Ref: "refactor", Passed: true, Verified: true},
		{Stage: DeliveryE2EPredeploy, Kind: "execution_report", Ref: "e2e", Passed: true, Verified: true},
		{Stage: DeliveryBuild, Kind: "execution_report", Ref: "build", Passed: true, Verified: true},
		{Stage: DeliveryDeploy, Kind: "deploy_receipt", Ref: "deploy", Passed: true, Verified: true},
		{Stage: DeliveryRestart, Kind: "deploy_receipt", Ref: "restart", Passed: true, Verified: true},
		{Stage: DeliveryPostDeployVerify, Kind: "readiness", Ref: "readiness", Passed: true, Verified: true},
		{Stage: DeliveryLiveVerified, Kind: "production_smoke", Ref: "live", Passed: true, Verified: true},
	}
	next, err := TransitionDelivery(item, DeliveryLiveVerified, refs)
	if err != nil || next.DeliveryState != DeliveryLiveVerified || !next.CheckOK {
		t.Fatalf("complete cumulative evidence rejected: next=%+v err=%v", next, err)
	}
}
