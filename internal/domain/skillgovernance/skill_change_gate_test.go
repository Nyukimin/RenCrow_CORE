package skillgovernance

import (
	"testing"
	"time"
)

func TestEvaluateSkillChangeGateBlocksMissingEvaluation(t *testing.T) {
	decision := EvaluateSkillChangeGate(SkillChangeLog{
		SkillID:                "core.pr-readiness",
		ChangeReason:           "PR gate wording update",
		ExpectedBehaviorChange: "stop low-quality PR",
		HumanApprovalStatus:    HumanApprovalGranted,
	})
	if decision.Status != ChangeGateStatusBlocked || decision.CanApply {
		t.Fatalf("decision=%#v", decision)
	}
	if len(decision.StopReasons) != 1 || decision.StopReasons[0] != "eval_result is required" {
		t.Fatalf("reasons=%#v", decision.StopReasons)
	}
}

func TestEvaluateSkillChangeGateBlocksAllMissingInputs(t *testing.T) {
	decision := EvaluateSkillChangeGate(SkillChangeLog{})
	if decision.Status != ChangeGateStatusBlocked || decision.CanApply || !decision.ReviewNeeded {
		t.Fatalf("decision=%#v", decision)
	}
	wantReasons := []string{
		"skill_id is required",
		"change_reason is required",
		"expected_behavior_change is required",
		"eval_result is required",
		"human approval is required",
	}
	if len(decision.StopReasons) != len(wantReasons) || len(decision.NextActions) != len(wantReasons) {
		t.Fatalf("reasons=%#v actions=%#v", decision.StopReasons, decision.NextActions)
	}
	for i, want := range wantReasons {
		if decision.StopReasons[i] != want {
			t.Fatalf("reason[%d]=%q, want %q", i, decision.StopReasons[i], want)
		}
	}
}

func TestEvaluateSkillChangeGatePassesWithEvaluationAndApproval(t *testing.T) {
	decision := EvaluateSkillChangeGate(SkillChangeLog{
		SkillID:                "core.pr-readiness",
		ChangeReason:           "PR gate wording update",
		ExpectedBehaviorChange: "stop low-quality PR",
		EvalResult:             "before/after passed",
		HumanApprovalStatus:    HumanApprovalGranted,
	})
	if decision.Status != ChangeGateStatusPassed || !decision.CanApply || decision.ReviewNeeded {
		t.Fatalf("decision=%#v", decision)
	}
}

func TestNewSkillChangeLogSetsIDAndCreatedAt(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	log, decision, err := NewSkillChangeLog("chg_1", SkillChangeLog{
		SkillID:                "core.pr-readiness",
		ChangeReason:           "PR gate wording update",
		ExpectedBehaviorChange: "stop low-quality PR",
		EvalResult:             "before/after passed",
		HumanApprovalStatus:    HumanApprovalGranted,
	}, now)
	if err != nil {
		t.Fatalf("NewSkillChangeLog failed: %v", err)
	}
	if log.ChangeID != "chg_1" || !log.CreatedAt.Equal(now) {
		t.Fatalf("log=%#v", log)
	}
	if decision.Status != ChangeGateStatusPassed {
		t.Fatalf("decision=%#v", decision)
	}
}

func TestNewSkillChangeLogRequiresChangeID(t *testing.T) {
	_, _, err := NewSkillChangeLog("", SkillChangeLog{}, time.Now())
	if err == nil {
		t.Fatal("expected missing change_id to fail")
	}
}
