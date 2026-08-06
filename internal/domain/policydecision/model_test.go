package policydecision

import (
	"strings"
	"testing"
	"time"
)

func TestValidateAcceptsRevisionedEnvelope(t *testing.T) {
	record := validRecord()
	if err := Validate(record); err != nil {
		t.Fatalf("Validate error=%v", err)
	}
}

func TestValidateRejectsMissingRevisionAndUnknownOutcome(t *testing.T) {
	record := validRecord()
	record.GlobalBundleRevision = ""
	if err := Validate(record); err == nil || !strings.Contains(err.Error(), "global_bundle_revision") {
		t.Fatalf("error=%v", err)
	}
	record = validRecord()
	record.Outcome = Outcome("maybe")
	if err := Validate(record); err == nil || !strings.Contains(err.Error(), "outcome") {
		t.Fatalf("error=%v", err)
	}
}

func TestValidateRejectsNonHashSnapshot(t *testing.T) {
	record := validRecord()
	record.InputSnapshotSHA256 = "raw-order-payload"
	if err := Validate(record); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("error=%v", err)
	}
}

func validRecord() Record {
	return Record{
		RecordVersion: RecordVersion,
		DecisionID:    "decision_1", Requester: "user:local", Module: "trade", Action: "live_order",
		BinaryContractRevision: "trade-binary/v1", GlobalBundleRevision: "bundle.1",
		ModulePolicyRevision: "trade-policy.1", DeploymentRevision: "production.1",
		Outcome: OutcomeBlocked, Reasons: []string{"binary hard limit"}, CreatedAt: time.Now().UTC(),
	}
}
