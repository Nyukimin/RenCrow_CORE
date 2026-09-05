package skillgovernance

import (
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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
		{name: "external PR", err: ValidateExternalPRSubmitRecord(ExternalPRSubmitRecord{SubmitID: "submit_1", ContributionEventID: "evt_1", Repo: "example/repo", Title: "Fix", SubmitStatus: ExternalPRSubmitStatusBlocked, FailureReason: "external PR adapter is not configured"}), want: "created_at"},
		{name: "transcript", err: ValidateCoderTranscriptEntry(CoderTranscriptEntry{EventID: "evt_1", TaskID: modulecore.NewTaskID(), Role: "assistant", Segment: "patch_evidence"}), want: "created_at"},
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
	if err := ValidateExternalPRSubmitRecord(ExternalPRSubmitRecord{SubmitID: "submit_1", ContributionEventID: "evt_1", Repo: "example/repo", Title: "Fix", SubmitStatus: ExternalPRSubmitStatusBlocked, FailureReason: "external PR adapter is not configured", CreatedAt: now}); err != nil {
		t.Fatalf("external PR should be valid: %v", err)
	}
	if err := ValidateCoderTranscriptEntry(CoderTranscriptEntry{EventID: "evt_1", TaskID: modulecore.NewTaskID(), Role: "assistant", Segment: "patch_evidence", CreatedAt: now}); err != nil {
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
		{name: "transcript missing task id", err: ValidateCoderTranscriptEntry(CoderTranscriptEntry{EventID: "evt_1", Role: "assistant", Segment: "patch_evidence", CreatedAt: now}), want: "task_id"},
		{name: "transcript missing event id", err: ValidateCoderTranscriptEntry(CoderTranscriptEntry{TaskID: modulecore.NewTaskID(), Role: "assistant", Segment: "patch_evidence", CreatedAt: now}), want: "event_id"},
		{name: "transcript missing role", err: ValidateCoderTranscriptEntry(CoderTranscriptEntry{EventID: "evt_1", TaskID: modulecore.NewTaskID(), Segment: "patch_evidence", CreatedAt: now}), want: "role"},
		{name: "transcript missing segment", err: ValidateCoderTranscriptEntry(CoderTranscriptEntry{EventID: "evt_1", TaskID: modulecore.NewTaskID(), Role: "assistant", CreatedAt: now}), want: "segment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil || !strings.Contains(tc.err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", tc.err, tc.want)
			}
		})
	}
}

func TestValidateSkillManifestAcceptsLegacyAndCompleteDevelopmentContracts(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 40, 0, 0, time.UTC)
	legacy := SkillManifest{
		SkillID:   "core.legacy",
		Name:      "Legacy",
		Scope:     ScopeCore,
		Path:      "skills/core/legacy",
		UpdatedAt: now,
	}
	if err := ValidateSkillManifest(legacy); err != nil {
		t.Fatalf("legacy manifest should remain valid: %v", err)
	}
	complete := legacy
	complete.SkillID = "core.complete"
	complete.DevelopmentContract = DevelopmentContract{
		RequiredCapability:   "inspect",
		RequiredTools:        []string{"rg"},
		RequiredKnowledge:    []string{"AGENTS.md"},
		AuthorityRequirement: "authenticated_request_scope",
		InputContract:        "request",
		OutputContract:       "receipt",
		CostHint:             "low",
		RiskLevel:            "low",
		EvaluationMethod:     "focused tests",
		Version:              "1.0.0",
	}
	if err := ValidateSkillManifest(complete); err != nil {
		t.Fatalf("complete development contract should be valid: %v", err)
	}
}

func TestValidateSkillManifestRejectsIncompleteDevelopmentContract(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 40, 0, 0, time.UTC)
	base := SkillManifest{
		SkillID:   "core.incomplete",
		Name:      "Incomplete",
		Scope:     ScopeCore,
		Path:      "skills/core/incomplete",
		UpdatedAt: now,
	}
	cases := []struct {
		name string
		item DevelopmentContract
		want string
	}{
		{name: "required capability", item: DevelopmentContract{Version: "1.0.0"}, want: "required_capability"},
		{name: "required tools", item: DevelopmentContract{RequiredCapability: "inspect", Version: "1.0.0"}, want: "required_tools"},
		{name: "required knowledge", item: DevelopmentContract{RequiredCapability: "inspect", RequiredTools: []string{"rg"}, Version: "1.0.0"}, want: "required_knowledge"},
		{name: "authority requirement", item: DevelopmentContract{RequiredCapability: "inspect", RequiredTools: []string{"rg"}, RequiredKnowledge: []string{"AGENTS.md"}, Version: "1.0.0"}, want: "authority_requirement"},
		{name: "input contract", item: DevelopmentContract{RequiredCapability: "inspect", RequiredTools: []string{"rg"}, RequiredKnowledge: []string{"AGENTS.md"}, AuthorityRequirement: "none", Version: "1.0.0"}, want: "input_contract"},
		{name: "output contract", item: DevelopmentContract{RequiredCapability: "inspect", RequiredTools: []string{"rg"}, RequiredKnowledge: []string{"AGENTS.md"}, AuthorityRequirement: "none", InputContract: "request", Version: "1.0.0"}, want: "output_contract"},
		{name: "cost hint", item: DevelopmentContract{RequiredCapability: "inspect", RequiredTools: []string{"rg"}, RequiredKnowledge: []string{"AGENTS.md"}, AuthorityRequirement: "none", InputContract: "request", OutputContract: "receipt", Version: "1.0.0"}, want: "cost_hint"},
		{name: "risk level", item: DevelopmentContract{RequiredCapability: "inspect", RequiredTools: []string{"rg"}, RequiredKnowledge: []string{"AGENTS.md"}, AuthorityRequirement: "none", InputContract: "request", OutputContract: "receipt", CostHint: "low", Version: "1.0.0"}, want: "risk_level"},
		{name: "evaluation method", item: DevelopmentContract{RequiredCapability: "inspect", RequiredTools: []string{"rg"}, RequiredKnowledge: []string{"AGENTS.md"}, AuthorityRequirement: "none", InputContract: "request", OutputContract: "receipt", CostHint: "low", RiskLevel: "low", Version: "1.0.0"}, want: "evaluation_method"},
		{name: "version", item: DevelopmentContract{RequiredCapability: "inspect", RequiredTools: []string{"rg"}, RequiredKnowledge: []string{"AGENTS.md"}, AuthorityRequirement: "none", InputContract: "request", OutputContract: "receipt", CostHint: "low", RiskLevel: "low", EvaluationMethod: "tests"}, want: "version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := base
			item.DevelopmentContract = tc.item
			err := ValidateSkillManifest(item)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateDevelopmentContractRejectsBlankListItems(t *testing.T) {
	base := completeDevelopmentContractForTest()
	base.RequiredTools = []string{"rg", " "}
	if err := ValidateDevelopmentContract(base); err == nil || !strings.Contains(err.Error(), "required_tools[1]") {
		t.Fatalf("blank required tool err=%v", err)
	}

	base = completeDevelopmentContractForTest()
	base.RequiredKnowledge = []string{"AGENTS.md", ""}
	if err := ValidateDevelopmentContract(base); err == nil || !strings.Contains(err.Error(), "required_knowledge[1]") {
		t.Fatalf("blank required knowledge err=%v", err)
	}
}

func completeDevelopmentContractForTest() DevelopmentContract {
	return DevelopmentContract{
		RequiredCapability:   "inspect",
		RequiredTools:        []string{"rg"},
		RequiredKnowledge:    []string{"AGENTS.md"},
		AuthorityRequirement: "none",
		InputContract:        "request",
		OutputContract:       "receipt",
		CostHint:             "low",
		RiskLevel:            "low",
		EvaluationMethod:     "tests",
		Version:              "1.0.0",
	}
}
