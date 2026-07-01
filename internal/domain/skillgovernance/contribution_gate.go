package skillgovernance

import (
	"errors"
	"strings"
	"time"
)

type ContributionGateDecision struct {
	Status        string   `json:"status"`
	StopReasons   []string `json:"stop_reasons,omitempty"`
	NextActions   []string `json:"next_actions,omitempty"`
	CanContribute bool     `json:"can_contribute"`
}

func EvaluateContributionGate(log ContributionGateLog) ContributionGateDecision {
	var reasons []string
	var actions []string
	if strings.TrimSpace(log.Repo) == "" {
		reasons = append(reasons, "repo is required")
		actions = append(actions, "対象リポジトリを明示する")
	}
	if strings.TrimSpace(log.ProblemStatement) == "" {
		reasons = append(reasons, "problem_statement is required")
		actions = append(actions, "実在する問題の説明を追加する")
	}
	if !log.ExistingPRsChecked {
		reasons = append(reasons, "existing PRs were not checked")
		actions = append(actions, "open / closed PR を確認する")
	}
	if !log.RealProblemVerified {
		reasons = append(reasons, "real problem is not verified")
		actions = append(actions, "再現ログ、失敗テスト、Issue などで実問題を確認する")
	}
	if !log.CoreChangeVerified {
		reasons = append(reasons, "core change fit is not verified")
		actions = append(actions, "core / plugin / project-specific の切り分けを確認する")
	}
	if !log.DiffHumanApproved {
		reasons = append(reasons, "complete diff was not human-approved")
		actions = append(actions, "complete diff を人間に提示して明示承認を得る")
	}
	if strings.TrimSpace(log.TestResult) == "" {
		reasons = append(reasons, "test result is required")
		actions = append(actions, "実行したテストまたは未実行理由を記録する")
	}
	if len(reasons) > 0 {
		return ContributionGateDecision{
			Status:        GateStatusBlocked,
			StopReasons:   reasons,
			NextActions:   actions,
			CanContribute: false,
		}
	}
	return ContributionGateDecision{
		Status:        GateStatusPassed,
		CanContribute: true,
	}
}

func NewContributionGateLog(eventID string, input ContributionGateLog, now time.Time) (ContributionGateLog, ContributionGateDecision, error) {
	if strings.TrimSpace(eventID) == "" {
		return ContributionGateLog{}, ContributionGateDecision{}, errors.New("event_id is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	input.EventID = eventID
	input.CreatedAt = now
	decision := EvaluateContributionGate(input)
	input.GateStatus = decision.Status
	return input, decision, nil
}
