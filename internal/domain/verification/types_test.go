package verification

import (
	"strings"
	"testing"
	"time"
)

func TestVerificationStatusValid(t *testing.T) {
	valid := []VerificationStatus{
		StatusVerified,
		StatusWeaklySupported,
		StatusUnsupported,
		StatusConflict,
		StatusNotChecked,
	}
	for _, status := range valid {
		if !status.Valid() {
			t.Fatalf("expected status %s to be valid", status)
		}
	}
	if VerificationStatus("success").Valid() {
		t.Fatal("success must not be a verification status")
	}
}

func TestTriggerLevelAndEvidenceSourceValidity(t *testing.T) {
	for _, level := range []TriggerLevel{TriggerLow, TriggerMedium, TriggerHigh} {
		if !level.Valid() {
			t.Fatalf("expected trigger level %s to be valid", level)
		}
	}
	if TriggerLevel("urgent").Valid() {
		t.Fatal("urgent must not be a trigger level")
	}

	for _, sourceType := range []EvidenceSourceType{
		EvidenceRecallPack,
		EvidenceConversationMemory,
		EvidenceL1SQLite,
		EvidenceVectorThreadMemory,
		EvidenceVectorKB,
		EvidenceSQLiteArchive,
		EvidenceSourceRegistry,
		EvidenceSearchCache,
		EvidenceRawExternalSource,
		EvidenceExecutionReport,
	} {
		if !sourceType.Valid() {
			t.Fatalf("expected source type %s to be valid", sourceType)
		}
	}
	if EvidenceSourceType("screenshot").Valid() {
		t.Fatal("screenshot must not be accepted as an evidence source type")
	}
	if !EvidenceSourceType("duckdb_archive").Valid() {
		t.Fatal("persisted legacy archive evidence must remain readable")
	}
}

func TestClaimValidateRejectsEmptyText(t *testing.T) {
	claim := Claim{ID: "claim-1", Priority: TriggerHigh, Status: StatusNotChecked}
	if err := claim.Validate(); err == nil {
		t.Fatal("expected empty claim text to be rejected")
	}
	claim = Claim{Text: "answer", Priority: TriggerHigh, Status: StatusNotChecked}
	if err := claim.Validate(); err == nil || !strings.Contains(err.Error(), "claim id") {
		t.Fatalf("expected empty claim id to be rejected, got %v", err)
	}
}

func TestClaimValidateAcceptsEvidenceAndRejectsInvalidMetadata(t *testing.T) {
	claim := Claim{
		ID:       "claim-1",
		Text:     "RenCrow has memory layers",
		Priority: TriggerMedium,
		Status:   StatusWeaklySupported,
		Evidence: []EvidenceRef{{
			ID:         "ev-1",
			SourceType: EvidenceRecallPack,
			Supports:   true,
		}},
	}
	if err := claim.Validate(); err != nil {
		t.Fatalf("expected claim to validate: %v", err)
	}
	claim.Priority = TriggerLevel("urgent")
	if err := claim.Validate(); err == nil {
		t.Fatal("invalid claim priority should fail")
	}
	claim.Priority = TriggerMedium
	claim.Status = VerificationStatus("passed")
	if err := claim.Validate(); err == nil {
		t.Fatal("invalid claim status should fail")
	}
}

func TestEvidenceRefRejectsInvalidSource(t *testing.T) {
	evidence := EvidenceRef{ID: "ev-1", SourceType: EvidenceSourceType("raw_log"), Supports: true}
	if err := evidence.Validate(); err == nil {
		t.Fatal("raw_log must not be accepted as evidence source")
	}
}

func TestEvidenceRefRejectsMissingIDAndConflictWithSupport(t *testing.T) {
	if err := (EvidenceRef{SourceType: EvidenceRecallPack, Supports: true}).Validate(); err == nil {
		t.Fatal("missing evidence id should fail")
	}
	if err := (EvidenceRef{ID: "ev-1", SourceType: EvidenceRecallPack, Supports: true, Conflicts: true}).Validate(); err == nil {
		t.Fatal("evidence cannot both support and conflict")
	}
}

func TestVerificationQuestionValidate(t *testing.T) {
	question := VerificationQuestion{ID: "q-1", ClaimID: "claim-1", Query: "What supports this?"}
	if err := question.Validate(); err != nil {
		t.Fatalf("expected question to validate: %v", err)
	}
	for _, invalid := range []VerificationQuestion{
		{ClaimID: "claim-1", Query: "query"},
		{ID: "q-1", Query: "query"},
		{ID: "q-1", ClaimID: "claim-1"},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid question should fail: %#v", invalid)
		}
	}
}

func TestVerificationPolicyNormalized(t *testing.T) {
	policy := (VerificationPolicy{}).Normalized()
	if policy.Default != TriggerLow || policy.Mode != "dry_run" {
		t.Fatalf("unexpected defaults: %#v", policy)
	}
	policy = (VerificationPolicy{Enabled: true, Mode: "revise", Default: TriggerHigh}).Normalized()
	if !policy.Enabled || policy.Default != TriggerHigh || policy.Mode != "revise" {
		t.Fatalf("explicit policy should be preserved: %#v", policy)
	}
}

func TestVerificationReportValidate(t *testing.T) {
	report := VerificationReport{
		ID:           "verify-job-1",
		JobID:        "job-1",
		SessionID:    "session-1",
		Route:        "CHAT",
		Status:       StatusNotChecked,
		TriggerLevel: TriggerLow,
		CreatedAt:    time.Now().UTC(),
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("expected report to validate: %v", err)
	}
	report.Status = VerificationStatus("passed")
	if err := report.Validate(); err == nil {
		t.Fatal("expected invalid report status to be rejected")
	}
}

func TestVerificationReportValidateRejectsMissingFields(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name string
		item VerificationReport
		want string
	}{
		{name: "missing id", item: VerificationReport{JobID: "job-1", SessionID: "session-1", Status: StatusNotChecked, CreatedAt: now}, want: "report id"},
		{name: "missing job", item: VerificationReport{ID: "verify-job-1", SessionID: "session-1", Status: StatusNotChecked, CreatedAt: now}, want: "job_id"},
		{name: "missing session", item: VerificationReport{ID: "verify-job-1", JobID: "job-1", Status: StatusNotChecked, CreatedAt: now}, want: "session_id"},
		{name: "missing status", item: VerificationReport{ID: "verify-job-1", JobID: "job-1", SessionID: "session-1", CreatedAt: now}, want: "status"},
		{name: "missing created", item: VerificationReport{ID: "verify-job-1", JobID: "job-1", SessionID: "session-1", Status: StatusNotChecked}, want: "created_at"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.item.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestVerificationReportValidateNestedObjects(t *testing.T) {
	report := VerificationReport{
		ID:           "verify-job-1",
		JobID:        "job-1",
		SessionID:    "session-1",
		Route:        "CHAT",
		Status:       StatusVerified,
		TriggerLevel: TriggerHigh,
		Claims: []Claim{{
			ID:       "claim-1",
			Text:     "answer",
			Priority: TriggerLow,
			Status:   StatusVerified,
		}},
		Questions: []VerificationQuestion{{
			ID:      "q-1",
			ClaimID: "claim-1",
			Query:   "verify answer",
		}},
		Evidence: []EvidenceRef{{
			ID:         "ev-1",
			SourceType: EvidenceExecutionReport,
			Supports:   true,
		}},
		CreatedAt: time.Now().UTC(),
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("expected nested report to validate: %v", err)
	}
	report.TriggerLevel = TriggerLevel("urgent")
	if err := report.Validate(); err == nil {
		t.Fatal("invalid report trigger level should fail")
	}
	report.TriggerLevel = TriggerHigh
	report.Claims[0].Text = ""
	if err := report.Validate(); err == nil || !strings.Contains(err.Error(), "claim text") {
		t.Fatalf("expected nested claim error, got %v", err)
	}
	report.Claims[0].Text = "answer"
	report.Questions[0].Query = ""
	if err := report.Validate(); err == nil || !strings.Contains(err.Error(), "query") {
		t.Fatalf("expected nested question error, got %v", err)
	}
	report.Questions[0].Query = "verify answer"
	report.Evidence[0].Supports = true
	report.Evidence[0].Conflicts = true
	if err := report.Validate(); err == nil || !strings.Contains(err.Error(), "both support and conflict") {
		t.Fatalf("expected nested evidence error, got %v", err)
	}
}
