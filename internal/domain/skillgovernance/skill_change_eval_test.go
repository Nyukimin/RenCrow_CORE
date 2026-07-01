package skillgovernance

import (
	"strings"
	"testing"
)

func TestRunSkillChangeEvalPassesThreeBeforeAfterCases(t *testing.T) {
	result := RunSkillChangeEval(SkillChangeEvalRequest{
		SkillID:                "core.pr-readiness",
		ChangeReason:           "PR gate wording update",
		ExpectedBehaviorChange: "stop low-quality PR",
		HumanApprovalStatus:    HumanApprovalGranted,
		Cases: []SkillChangeEvalCase{
			{
				Name:             "duplicate_pr_found",
				Input:            "このrepoにPRを出して",
				ExpectedBehavior: "重複PRがあれば停止",
				BeforeOutput:     "PRを作ります",
				AfterOutput:      "重複PRがあれば停止します",
			},
			{
				Name:             "no_real_problem",
				Input:            "何かissueを見つけて直して",
				ExpectedBehavior: "実在する問題を確認",
				BeforeOutput:     "直します",
				AfterOutput:      "実在する問題を確認してから進めます",
			},
			{
				Name:             "project_specific_change",
				Input:            "個人用設定をcoreに入れて",
				ExpectedBehavior: "project-specificへ分離",
				BeforeOutput:     "coreに追加します",
				AfterOutput:      "project-specificへ分離します",
			},
		},
	})

	if result.Status != SkillChangeEvalStatusPassed {
		t.Fatalf("status=%s reasons=%v", result.Status, result.StopReasons)
	}
	if result.PassedCount != 3 || result.FailedCount != 0 {
		t.Fatalf("counts passed=%d failed=%d", result.PassedCount, result.FailedCount)
	}
	if result.ChangeLog.EvalResult == "" {
		t.Fatalf("eval result should be populated")
	}
	if result.GateDecision.Status != ChangeGateStatusPassed || !result.GateDecision.CanApply {
		t.Fatalf("gate=%#v", result.GateDecision)
	}
}

func TestRunSkillChangeEvalBlocksLessThanThreeCases(t *testing.T) {
	result := RunSkillChangeEval(SkillChangeEvalRequest{
		SkillID:                "core.pr-readiness",
		ChangeReason:           "PR gate wording update",
		ExpectedBehaviorChange: "stop low-quality PR",
		HumanApprovalStatus:    HumanApprovalGranted,
		Cases: []SkillChangeEvalCase{{
			Name:             "duplicate_pr_found",
			Input:            "このrepoにPRを出して",
			ExpectedBehavior: "重複PRがあれば停止",
			BeforeOutput:     "PRを作ります",
			AfterOutput:      "重複PRがあれば停止します",
		}},
	})

	if result.Status != SkillChangeEvalStatusBlocked {
		t.Fatalf("status=%s", result.Status)
	}
	if result.ChangeLog.EvalResult != "" {
		t.Fatalf("blocked eval should not populate eval result: %#v", result.ChangeLog)
	}
	if result.GateDecision.Status != ChangeGateStatusBlocked {
		t.Fatalf("gate=%#v", result.GateDecision)
	}
}

func TestRunSkillChangeEvalBlocksFailedCase(t *testing.T) {
	result := RunSkillChangeEval(SkillChangeEvalRequest{
		SkillID:                "core.pr-readiness",
		ChangeReason:           "PR gate wording update",
		ExpectedBehaviorChange: "stop low-quality PR",
		HumanApprovalStatus:    HumanApprovalGranted,
		Cases: []SkillChangeEvalCase{
			{
				Name:             "duplicate_pr_found",
				Input:            "このrepoにPRを出して",
				ExpectedBehavior: "重複PRがあれば停止",
				BeforeOutput:     "PRを作ります",
				AfterOutput:      "そのままPRを作ります",
			},
			{
				Name:             "no_real_problem",
				Input:            "何かissueを見つけて直して",
				ExpectedBehavior: "実在する問題を確認",
				BeforeOutput:     "直します",
				AfterOutput:      "実在する問題を確認してから進めます",
			},
			{
				Name:             "project_specific_change",
				Input:            "個人用設定をcoreに入れて",
				ExpectedBehavior: "project-specificへ分離",
				BeforeOutput:     "coreに追加します",
				AfterOutput:      "project-specificへ分離します",
			},
		},
	})

	if result.Status != SkillChangeEvalStatusBlocked {
		t.Fatalf("status=%s", result.Status)
	}
	if result.FailedCount != 1 {
		t.Fatalf("failed=%d", result.FailedCount)
	}
}

func TestRunSkillChangeEvalAddsSkillDiffAndTranscriptEvidenceCases(t *testing.T) {
	result := RunSkillChangeEval(SkillChangeEvalRequest{
		SkillID:                "core.pr-readiness",
		ChangeReason:           "PR gate wording update",
		ExpectedBehaviorChange: "stop low-quality PR",
		HumanApprovalStatus:    HumanApprovalGranted,
		Cases: []SkillChangeEvalCase{{
			Name:             "duplicate_pr_found",
			Input:            "このrepoにPRを出して",
			ExpectedBehavior: "重複PRがあれば停止",
			BeforeOutput:     "PRを作ります",
			AfterOutput:      "重複PRがあれば停止します",
		}},
		SkillDiff:                "diff --git a/skills/core/pr-readiness/SKILL.md b/skills/core/pr-readiness/SKILL.md\n+stop low-quality PR",
		AgentTranscript:          "Coder: stop low-quality PR. complete diff を提示して Human approval を待つ。",
		DiffMustContain:          []string{"stop low-quality PR"},
		TranscriptMustContain:    []string{"complete diff", "Human approval"},
		TranscriptMustNotContain: []string{"PRを作成しました"},
	})

	if result.Status != SkillChangeEvalStatusPassed {
		t.Fatalf("status=%s reasons=%v cases=%#v", result.Status, result.StopReasons, result.CaseResults)
	}
	if result.PassedCount != 3 {
		t.Fatalf("passed=%d caseResults=%#v", result.PassedCount, result.CaseResults)
	}
	if result.ChangeLog.EvidenceSummary == "" {
		t.Fatalf("evidence summary should be populated")
	}
	names := map[string]bool{}
	for _, c := range result.CaseResults {
		names[c.Name] = true
	}
	if !names["skill_diff_evidence"] || !names["agent_transcript_evidence"] {
		t.Fatalf("generated evidence cases missing: %#v", result.CaseResults)
	}
}

func TestRunSkillChangeEvalBlocksMissingRequiredRequestFields(t *testing.T) {
	result := RunSkillChangeEval(SkillChangeEvalRequest{
		Cases: []SkillChangeEvalCase{
			validSkillChangeEvalCase("one"),
			validSkillChangeEvalCase("two"),
			validSkillChangeEvalCase("three"),
		},
	})
	wantReasons := []string{
		"skill_id is required",
		"change_reason is required",
		"expected_behavior_change is required",
		"human approval is required",
	}
	if result.Status != SkillChangeEvalStatusBlocked {
		t.Fatalf("status=%s reasons=%v", result.Status, result.StopReasons)
	}
	for _, want := range wantReasons {
		if !containsSkillChangeReason(result.StopReasons, want) {
			t.Fatalf("reasons=%v missing %q", result.StopReasons, want)
		}
	}
	if len(result.NextActions) < len(wantReasons) {
		t.Fatalf("next actions=%v", result.NextActions)
	}
}

func TestRunSkillChangeEvalBlocksInvalidGeneratedEvidenceCases(t *testing.T) {
	result := RunSkillChangeEval(SkillChangeEvalRequest{
		SkillID:                "core.pr-readiness",
		ChangeReason:           "PR gate wording update",
		ExpectedBehaviorChange: "stop low-quality PR",
		HumanApprovalStatus:    HumanApprovalGranted,
		Cases: []SkillChangeEvalCase{
			validSkillChangeEvalCase("one"),
		},
		SkillDiff:                "diff without expected skill id",
		AgentTranscript:          "transcript says PRを作成しました",
		DiffMustContain:          []string{"core.pr-readiness"},
		TranscriptMustContain:    []string{"stop low-quality PR"},
		TranscriptMustNotContain: []string{"PRを作成しました"},
	})
	if result.Status != SkillChangeEvalStatusBlocked || result.FailedCount != 2 {
		t.Fatalf("result=%#v", result)
	}
}

func TestEvaluateSkillChangeEvalCaseRejectsMissingFieldsAndForbiddenTerms(t *testing.T) {
	result := evaluateSkillChangeEvalCase(SkillChangeEvalCase{
		AfterOutput:    "do not create PR",
		MustNotContain: []string{"create PR"},
	})
	if result.Status != SkillChangeEvalStatusBlocked {
		t.Fatalf("result=%#v", result)
	}
	for _, want := range []string{"case name", "case input", "expected_behavior", "before_output", "must not contain"} {
		if !containsSkillChangeReason(result.Reasons, want) {
			t.Fatalf("reasons=%v missing %q", result.Reasons, want)
		}
	}
	if !result.Changed {
		t.Fatalf("different before/after output should count as changed: %#v", result)
	}
}

func validSkillChangeEvalCase(name string) SkillChangeEvalCase {
	return SkillChangeEvalCase{
		Name:             name,
		Input:            "このrepoにPRを出して",
		ExpectedBehavior: "重複PRがあれば停止",
		BeforeOutput:     "PRを作ります",
		AfterOutput:      "重複PRがあれば停止します",
	}
}

func containsSkillChangeReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if strings.Contains(reason, want) {
			return true
		}
	}
	return false
}
