package skillgovernance

import (
	"strings"
	"testing"
	"time"
)

func TestNewBlockedExternalPRSubmitRecordRequiresHumanApproval(t *testing.T) {
	_, err := NewBlockedExternalPRSubmitRecord(ExternalPRSubmitRecord{
		SubmitID:            "submit_1",
		ContributionEventID: "evt_contrib_1",
		Repo:                "example/repo",
		Title:               "Fix bug",
		HumanApproved:       false,
	}, time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "human approval") {
		t.Fatalf("err=%v", err)
	}
}

func TestNewBlockedExternalPRSubmitRecordCreatesBlockedAudit(t *testing.T) {
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	record, err := NewBlockedExternalPRSubmitRecord(ExternalPRSubmitRecord{
		SubmitID:            "submit_1",
		ContributionEventID: "evt_contrib_1",
		Repo:                "example/repo",
		TargetBranch:        "main",
		Title:               "Fix bug",
		DiffPath:            "workspace/logs/skill_governance/coder_evidence/job-1/skill_diff.md",
		TestResult:          "go test ./internal/domain/skillgovernance",
		HumanApproved:       true,
	}, now)
	if err != nil {
		t.Fatalf("NewBlockedExternalPRSubmitRecord() error = %v", err)
	}
	if record.SubmitStatus != ExternalPRSubmitStatusBlocked || record.ExternalPRCreated || record.PostSubmitVerified {
		t.Fatalf("record=%#v", record)
	}
	if record.FailureReason != "external PR adapter is not configured" || record.PRAdapter != "unconfigured" {
		t.Fatalf("record=%#v", record)
	}
	if !record.CreatedAt.Equal(now) {
		t.Fatalf("created_at=%s", record.CreatedAt)
	}
}

func TestValidateExternalPRSubmitRecordRejectsMissingRequiredFieldsAndInvalidStatus(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 40, 0, 0, time.UTC)
	validBlocked := ExternalPRSubmitRecord{
		SubmitID:            "submit_1",
		ContributionEventID: "evt_contrib_1",
		Repo:                "example/repo",
		Title:               "Fix bug",
		ApprovalStatus:      "approved",
		HumanApproved:       true,
		SubmitStatus:        ExternalPRSubmitStatusBlocked,
		FailureReason:       "external PR adapter is not configured",
		CreatedAt:           now,
	}
	cases := []struct {
		name   string
		mutate func(*ExternalPRSubmitRecord)
		want   string
	}{
		{name: "missing submit id", mutate: func(record *ExternalPRSubmitRecord) { record.SubmitID = "" }, want: "submit_id"},
		{name: "missing contribution event id", mutate: func(record *ExternalPRSubmitRecord) { record.ContributionEventID = "" }, want: "contribution_event_id"},
		{name: "missing repo", mutate: func(record *ExternalPRSubmitRecord) { record.Repo = "" }, want: "repo"},
		{name: "missing title", mutate: func(record *ExternalPRSubmitRecord) { record.Title = "" }, want: "title"},
		{name: "missing human approval", mutate: func(record *ExternalPRSubmitRecord) { record.HumanApproved = false }, want: "human approval"},
		{name: "pending approval status", mutate: func(record *ExternalPRSubmitRecord) { record.ApprovalStatus = "pending" }, want: "approval_status"},
		{name: "missing submit status", mutate: func(record *ExternalPRSubmitRecord) { record.SubmitStatus = "" }, want: "submit_status"},
		{name: "invalid submit status", mutate: func(record *ExternalPRSubmitRecord) { record.SubmitStatus = "queued" }, want: "submit_status"},
		{name: "missing failure reason", mutate: func(record *ExternalPRSubmitRecord) { record.FailureReason = "" }, want: "failure_reason"},
		{name: "missing created at", mutate: func(record *ExternalPRSubmitRecord) { record.CreatedAt = time.Time{} }, want: "created_at"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := validBlocked
			tc.mutate(&record)
			err := ValidateExternalPRSubmitRecord(record)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateExternalPRSubmitRecordRejectsCreatedStatusWithoutCreatedPR(t *testing.T) {
	record := ExternalPRSubmitRecord{
		SubmitID:            "submit_1",
		ContributionEventID: "evt_contrib_1",
		Repo:                "example/repo",
		Title:               "Fix bug",
		ApprovalStatus:      "approved",
		HumanApproved:       true,
		SubmitStatus:        ExternalPRSubmitStatusCreated,
		ExternalPRCreated:   false,
		PostSubmitVerified:  false,
	}
	err := ValidateExternalPRSubmitRecord(record)
	if err == nil || !strings.Contains(err.Error(), "submit_status=created") {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateExternalPRSubmitRecordRejectsCreatedFlagWithoutCreatedStatus(t *testing.T) {
	record := ExternalPRSubmitRecord{
		SubmitID:            "submit_1",
		ContributionEventID: "evt_contrib_1",
		Repo:                "example/repo",
		Title:               "Fix bug",
		ApprovalStatus:      "approved",
		HumanApproved:       true,
		SubmitStatus:        ExternalPRSubmitStatusBlocked,
		FailureReason:       "external PR adapter is not configured",
		ExternalPRCreated:   true,
	}
	err := ValidateExternalPRSubmitRecord(record)
	if err == nil || !strings.Contains(err.Error(), "external_pr_created") {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateExternalPRSubmitRecordRejectsPostSubmitVerificationWithoutCreatedPR(t *testing.T) {
	record := ExternalPRSubmitRecord{
		SubmitID:            "submit_1",
		ContributionEventID: "evt_contrib_1",
		Repo:                "example/repo",
		Title:               "Fix bug",
		ApprovalStatus:      "approved",
		HumanApproved:       true,
		SubmitStatus:        ExternalPRSubmitStatusBlocked,
		FailureReason:       "external PR adapter is not configured",
		ExternalPRCreated:   false,
		PostSubmitVerified:  true,
	}
	err := ValidateExternalPRSubmitRecord(record)
	if err == nil || !strings.Contains(err.Error(), "post_submit_verified") {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateExternalPRSubmitRecordRejectsCreatedPRWithoutPostSubmitVerification(t *testing.T) {
	record := ExternalPRSubmitRecord{
		SubmitID:            "submit_1",
		ContributionEventID: "evt_contrib_1",
		Repo:                "example/repo",
		Title:               "Fix bug",
		ApprovalStatus:      "approved",
		HumanApproved:       true,
		SubmitStatus:        ExternalPRSubmitStatusCreated,
		PRURL:               "https://github.com/example/repo/pull/1",
		ExternalPRCreated:   true,
		PostSubmitVerified:  false,
	}
	err := ValidateExternalPRSubmitRecord(record)
	if err == nil || !strings.Contains(err.Error(), "post_submit_verified") {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateExternalPRSubmitRecordRejectsCreatedPRWithoutPRURL(t *testing.T) {
	record := ExternalPRSubmitRecord{
		SubmitID:            "submit_1",
		ContributionEventID: "evt_contrib_1",
		Repo:                "example/repo",
		Title:               "Fix bug",
		ApprovalStatus:      "approved",
		HumanApproved:       true,
		SubmitStatus:        ExternalPRSubmitStatusCreated,
		ExternalPRCreated:   true,
		PostSubmitVerified:  true,
		PostSubmitEvidence:  "checks passed at 2026-05-19T10:00:00Z",
	}
	err := ValidateExternalPRSubmitRecord(record)
	if err == nil || !strings.Contains(err.Error(), "pr_url") {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateExternalPRSubmitRecordRejectsPostSubmitVerificationWithoutEvidence(t *testing.T) {
	record := ExternalPRSubmitRecord{
		SubmitID:            "submit_1",
		ContributionEventID: "evt_contrib_1",
		Repo:                "example/repo",
		Title:               "Fix bug",
		ApprovalStatus:      "approved",
		HumanApproved:       true,
		SubmitStatus:        ExternalPRSubmitStatusCreated,
		PRURL:               "https://github.com/example/repo/pull/1",
		ExternalPRCreated:   true,
		PostSubmitVerified:  true,
	}
	err := ValidateExternalPRSubmitRecord(record)
	if err == nil || !strings.Contains(err.Error(), "post_submit_evidence") {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateExternalPRSubmitRecordAcceptsCreatedPRWithVerificationEvidence(t *testing.T) {
	record := ExternalPRSubmitRecord{
		SubmitID:            "submit_1",
		ContributionEventID: "evt_contrib_1",
		Repo:                "example/repo",
		Title:               "Fix bug",
		ApprovalStatus:      "approved",
		HumanApproved:       true,
		SubmitStatus:        ExternalPRSubmitStatusCreated,
		PRURL:               "https://github.com/example/repo/pull/1",
		ExternalPRCreated:   true,
		PostSubmitVerified:  true,
		PostSubmitEvidence:  "PR URL opened and required checks observed as passing",
		PRAdapter:           "github",
		CreatedAt:           time.Date(2026, 5, 20, 7, 40, 0, 0, time.UTC),
	}
	if err := ValidateExternalPRSubmitRecord(record); err != nil {
		t.Fatalf("ValidateExternalPRSubmitRecord() error = %v", err)
	}
}
