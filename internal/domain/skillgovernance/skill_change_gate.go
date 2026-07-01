package skillgovernance

import (
	"errors"
	"strings"
	"time"
)

const (
	ChangeGateStatusPassed  = "passed"
	ChangeGateStatusBlocked = "blocked"

	HumanApprovalGranted = "granted"
)

type SkillChangeGateDecision struct {
	Status       string   `json:"status"`
	StopReasons  []string `json:"stop_reasons,omitempty"`
	NextActions  []string `json:"next_actions,omitempty"`
	CanApply     bool     `json:"can_apply"`
	ReviewNeeded bool     `json:"review_needed"`
}

func EvaluateSkillChangeGate(log SkillChangeLog) SkillChangeGateDecision {
	var reasons []string
	var actions []string
	if strings.TrimSpace(log.SkillID) == "" {
		reasons = append(reasons, "skill_id is required")
		actions = append(actions, "対象 Skill を明示する")
	}
	if strings.TrimSpace(log.ChangeReason) == "" {
		reasons = append(reasons, "change_reason is required")
		actions = append(actions, "Skill 変更理由を記録する")
	}
	if strings.TrimSpace(log.ExpectedBehaviorChange) == "" {
		reasons = append(reasons, "expected_behavior_change is required")
		actions = append(actions, "期待する Agent 行動変化を記録する")
	}
	if strings.TrimSpace(log.EvalResult) == "" {
		reasons = append(reasons, "eval_result is required")
		actions = append(actions, "before / after 評価結果を記録する")
	}
	if strings.TrimSpace(log.HumanApprovalStatus) != HumanApprovalGranted {
		reasons = append(reasons, "human approval is required")
		actions = append(actions, "Human approval を granted にする")
	}
	if len(reasons) > 0 {
		return SkillChangeGateDecision{
			Status:       ChangeGateStatusBlocked,
			StopReasons:  reasons,
			NextActions:  actions,
			CanApply:     false,
			ReviewNeeded: true,
		}
	}
	return SkillChangeGateDecision{
		Status:   ChangeGateStatusPassed,
		CanApply: true,
	}
}

func NewSkillChangeLog(changeID string, input SkillChangeLog, now time.Time) (SkillChangeLog, SkillChangeGateDecision, error) {
	if strings.TrimSpace(changeID) == "" {
		return SkillChangeLog{}, SkillChangeGateDecision{}, errors.New("change_id is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	input.ChangeID = changeID
	input.CreatedAt = now
	decision := EvaluateSkillChangeGate(input)
	return input, decision, nil
}
