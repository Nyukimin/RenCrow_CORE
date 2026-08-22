package backlog

import (
	"errors"
	"testing"
)

// RED: Passed=true is a request-side claim.  It must not satisfy the
// LIVE_VERIFIED gate until the CORE-owned, kind-specific verifier has checked
// the referenced artifact, revision, hash, result, and observation time.
func TestRevision2OwnerVerificationRejectsPassedClaim(t *testing.T) {
	item := Item{
		SchemaVersion: SchemaVersion2,
		ItemID:        "revision2-evidence-gate",
		Title:         "revision 2 evidence gate",
		ConceptState:  ConceptAdopted,
		DeliveryState: DeliveryPostDeployVerify,
	}
	refs := []EvidenceRef{
		{Stage: DeliverySpec, Kind: "spec", Ref: "string-only-spec", Passed: true},
		{Stage: DeliveryTDDRed, Kind: "tdd_red", Ref: "string-only-red", Passed: true},
		{Stage: DeliveryTDDGreen, Kind: "tdd_green", Ref: "string-only-green", Passed: true},
		{Stage: DeliveryRefactor, Kind: "refactor", Ref: "string-only-refactor", Passed: true},
		{Stage: DeliveryE2EPredeploy, Kind: "e2e", Ref: "string-only-e2e", Passed: true},
		{Stage: DeliveryBuild, Kind: "build", Ref: "string-only-build", Passed: true},
		{Stage: DeliveryDeploy, Kind: "deploy_receipt", Ref: "string-only-deploy", Passed: true},
		{Stage: DeliveryRestart, Kind: "restart_receipt", Ref: "string-only-restart", Passed: true},
		{Stage: DeliveryPostDeployVerify, Kind: "readiness", Ref: "string-only-readiness", Passed: true},
		{Stage: DeliveryLiveVerified, Kind: "production_smoke", Ref: "string-only-smoke", Passed: true},
	}

	if _, err := TransitionDelivery(item, DeliveryLiveVerified, refs); !errors.Is(err, ErrEvidenceRequired) {
		t.Fatalf("unverified Passed=true claims must be rejected, got %v", err)
	}
}

func TestRevision2EvidenceKindsCannotBeRelabeledAcrossStages(t *testing.T) {
	tests := []struct {
		name   string
		target string
		ref    EvidenceRef
	}{
		{
			name:   "spec cannot satisfy tdd red",
			target: DeliveryTDDRed,
			ref:    EvidenceRef{Stage: DeliveryTDDRed, Kind: "spec", Ref: "spec-at-red", Verified: true},
		},
		{
			name:   "readiness cannot satisfy live",
			target: DeliveryLiveVerified,
			ref:    EvidenceRef{Stage: DeliveryLiveVerified, Kind: "readiness", Ref: "readiness-at-live", Verified: true},
		},
		{
			name:   "execution report must be bound to its stage",
			target: DeliveryTDDRed,
			ref:    EvidenceRef{Stage: DeliveryBuild, Kind: "execution_report", Ref: "build-at-red", Verified: true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if HasRequiredEvidence(test.target, []EvidenceRef{test.ref}) {
				t.Fatalf("cross-stage evidence unexpectedly satisfied target=%s ref=%+v", test.target, test.ref)
			}
		})
	}
}
