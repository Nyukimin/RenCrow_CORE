package skillgovernance

import (
	"strings"
	"testing"
	"time"
)

func TestValidateSkillGovernanceRejectsMissingTimestamp(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "manifest", err: ValidateSkillManifest(SkillManifest{SkillID: "core.review", Name: "Review", Scope: ScopeCore, Path: "skills/review"}), want: "updated_at"},
		{name: "trigger", err: ValidateSkillTriggerLog(SkillTriggerLog{EventID: "evt_1", SkillID: "core.review", Status: TriggerStatusTriggered}), want: "created_at"},
		{name: "change", err: ValidateSkillChangeLog(SkillChangeLog{ChangeID: "chg_1", SkillID: "core.review"}), want: "created_at"},
		{name: "contribution", err: ValidateContributionGateLog(ContributionGateLog{EventID: "evt_1", Repo: "example/repo", GateStatus: GateStatusBlocked}), want: "created_at"},
		{name: "external PR", err: ValidateExternalPRSubmitRecord(ExternalPRSubmitRecord{SubmitID: "submit_1", ContributionEventID: "evt_1", Repo: "example/repo", Title: "Fix", ApprovalStatus: "approved", HumanApproved: true, SubmitStatus: ExternalPRSubmitStatusBlocked, FailureReason: "external PR adapter is not configured"}), want: "created_at"},
		{name: "transcript", err: ValidateCoderTranscriptEntry(CoderTranscriptEntry{EventID: "evt_1", Role: "assistant", Segment: "patch_evidence"}), want: "created_at"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil || !strings.Contains(tc.err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", tc.err, tc.want)
			}
		})
	}
}

func TestValidateSkillGovernanceAcceptsTimestampedRecords(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 40, 0, 0, time.UTC)
	if err := ValidateSkillManifest(SkillManifest{SkillID: "core.review", Name: "Review", Scope: ScopeCore, Path: "skills/review", UpdatedAt: now}); err != nil {
		t.Fatalf("manifest should be valid: %v", err)
	}
	if err := ValidateSkillTriggerLog(SkillTriggerLog{EventID: "evt_1", SkillID: "core.review", Status: TriggerStatusTriggered, CreatedAt: now}); err != nil {
		t.Fatalf("trigger should be valid: %v", err)
	}
	if err := ValidateSkillChangeLog(SkillChangeLog{ChangeID: "chg_1", SkillID: "core.review", CreatedAt: now}); err != nil {
		t.Fatalf("change should be valid: %v", err)
	}
	if err := ValidateContributionGateLog(ContributionGateLog{EventID: "evt_1", Repo: "example/repo", GateStatus: GateStatusBlocked, CreatedAt: now}); err != nil {
		t.Fatalf("contribution should be valid: %v", err)
	}
	if err := ValidateExternalPRSubmitRecord(ExternalPRSubmitRecord{SubmitID: "submit_1", ContributionEventID: "evt_1", Repo: "example/repo", Title: "Fix", ApprovalStatus: "approved", HumanApproved: true, SubmitStatus: ExternalPRSubmitStatusBlocked, FailureReason: "external PR adapter is not configured", CreatedAt: now}); err != nil {
		t.Fatalf("external PR should be valid: %v", err)
	}
	if err := ValidateCoderTranscriptEntry(CoderTranscriptEntry{EventID: "evt_1", Role: "assistant", Segment: "patch_evidence", CreatedAt: now}); err != nil {
		t.Fatalf("transcript should be valid: %v", err)
	}
}

func TestValidateSkillGovernanceRejectsMissingRequiredFields(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 40, 0, 0, time.UTC)
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "manifest missing skill id", err: ValidateSkillManifest(SkillManifest{Name: "Review", Scope: ScopeCore, Path: "skills/review", UpdatedAt: now}), want: "skill_id"},
		{name: "manifest missing name", err: ValidateSkillManifest(SkillManifest{SkillID: "core.review", Scope: ScopeCore, Path: "skills/review", UpdatedAt: now}), want: "name"},
		{name: "manifest missing scope", err: ValidateSkillManifest(SkillManifest{SkillID: "core.review", Name: "Review", Path: "skills/review", UpdatedAt: now}), want: "scope"},
		{name: "manifest missing path", err: ValidateSkillManifest(SkillManifest{SkillID: "core.review", Name: "Review", Scope: ScopeCore, UpdatedAt: now}), want: "path"},
		{name: "trigger missing event id", err: ValidateSkillTriggerLog(SkillTriggerLog{SkillID: "core.review", Status: TriggerStatusTriggered, CreatedAt: now}), want: "event_id"},
		{name: "trigger missing skill id", err: ValidateSkillTriggerLog(SkillTriggerLog{EventID: "evt_1", Status: TriggerStatusTriggered, CreatedAt: now}), want: "skill_id"},
		{name: "trigger missing status", err: ValidateSkillTriggerLog(SkillTriggerLog{EventID: "evt_1", SkillID: "core.review", CreatedAt: now}), want: "status"},
		{name: "change missing id", err: ValidateSkillChangeLog(SkillChangeLog{SkillID: "core.review", CreatedAt: now}), want: "change_id"},
		{name: "change missing skill id", err: ValidateSkillChangeLog(SkillChangeLog{ChangeID: "chg_1", CreatedAt: now}), want: "skill_id"},
		{name: "contribution missing event id", err: ValidateContributionGateLog(ContributionGateLog{Repo: "example/repo", GateStatus: GateStatusBlocked, CreatedAt: now}), want: "event_id"},
		{name: "contribution missing repo", err: ValidateContributionGateLog(ContributionGateLog{EventID: "evt_1", GateStatus: GateStatusBlocked, CreatedAt: now}), want: "repo"},
		{name: "contribution missing gate status", err: ValidateContributionGateLog(ContributionGateLog{EventID: "evt_1", Repo: "example/repo", CreatedAt: now}), want: "gate_status"},
		{name: "transcript missing event id", err: ValidateCoderTranscriptEntry(CoderTranscriptEntry{Role: "assistant", Segment: "patch_evidence", CreatedAt: now}), want: "event_id"},
		{name: "transcript missing role", err: ValidateCoderTranscriptEntry(CoderTranscriptEntry{EventID: "evt_1", Segment: "patch_evidence", CreatedAt: now}), want: "role"},
		{name: "transcript missing segment", err: ValidateCoderTranscriptEntry(CoderTranscriptEntry{EventID: "evt_1", Role: "assistant", CreatedAt: now}), want: "segment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil || !strings.Contains(tc.err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", tc.err, tc.want)
			}
		})
	}
}
