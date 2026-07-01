package skillgovernance

import (
	"fmt"
	"strings"
)

const (
	SkillChangeEvalStatusPassed  = "passed"
	SkillChangeEvalStatusBlocked = "blocked"
)

type SkillChangeEvalCase struct {
	Name             string   `json:"name"`
	Input            string   `json:"input"`
	ExpectedBehavior string   `json:"expected_behavior"`
	BeforeOutput     string   `json:"before_output"`
	AfterOutput      string   `json:"after_output"`
	MustContain      []string `json:"must_contain,omitempty"`
	MustNotContain   []string `json:"must_not_contain,omitempty"`
}

type SkillChangeEvalRequest struct {
	ChangeID                 string                `json:"change_id,omitempty"`
	SkillID                  string                `json:"skill_id"`
	OldVersion               string                `json:"old_version,omitempty"`
	NewVersion               string                `json:"new_version,omitempty"`
	ChangeReason             string                `json:"change_reason"`
	ExpectedBehaviorChange   string                `json:"expected_behavior_change"`
	HumanApprovalStatus      string                `json:"human_approval_status"`
	Cases                    []SkillChangeEvalCase `json:"cases"`
	SkillDiff                string                `json:"skill_diff,omitempty"`
	AgentTranscript          string                `json:"agent_transcript,omitempty"`
	SkillDiffPath            string                `json:"skill_diff_path,omitempty"`
	AgentTranscriptPath      string                `json:"agent_transcript_path,omitempty"`
	DiffMustContain          []string              `json:"diff_must_contain,omitempty"`
	TranscriptMustContain    []string              `json:"transcript_must_contain,omitempty"`
	TranscriptMustNotContain []string              `json:"transcript_must_not_contain,omitempty"`
}

type SkillChangeEvalCaseResult struct {
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Reasons     []string `json:"reasons,omitempty"`
	Changed     bool     `json:"changed"`
	ContainHits []string `json:"contain_hits,omitempty"`
}

type SkillChangeEvalResult struct {
	Status       string                      `json:"status"`
	Summary      string                      `json:"summary"`
	PassedCount  int                         `json:"passed_count"`
	FailedCount  int                         `json:"failed_count"`
	StopReasons  []string                    `json:"stop_reasons,omitempty"`
	NextActions  []string                    `json:"next_actions,omitempty"`
	CaseResults  []SkillChangeEvalCaseResult `json:"case_results"`
	ChangeLog    SkillChangeLog              `json:"change_log"`
	GateDecision SkillChangeGateDecision     `json:"gate_decision"`
}

func RunSkillChangeEval(req SkillChangeEvalRequest) SkillChangeEvalResult {
	var stopReasons []string
	var nextActions []string
	req.Cases = append(req.Cases, BuildSkillChangeEvidenceCases(req)...)
	if strings.TrimSpace(req.SkillID) == "" {
		stopReasons = append(stopReasons, "skill_id is required")
		nextActions = append(nextActions, "対象 Skill を明示する")
	}
	if strings.TrimSpace(req.ChangeReason) == "" {
		stopReasons = append(stopReasons, "change_reason is required")
		nextActions = append(nextActions, "Skill 変更理由を記録する")
	}
	if strings.TrimSpace(req.ExpectedBehaviorChange) == "" {
		stopReasons = append(stopReasons, "expected_behavior_change is required")
		nextActions = append(nextActions, "期待する Agent 行動変化を記録する")
	}
	if strings.TrimSpace(req.HumanApprovalStatus) != HumanApprovalGranted {
		stopReasons = append(stopReasons, "human approval is required")
		nextActions = append(nextActions, "Human approval を granted にする")
	}
	if len(req.Cases) < 3 {
		stopReasons = append(stopReasons, "at least 3 eval cases are required")
		nextActions = append(nextActions, "最低3件の before / after eval case を用意する")
	}

	caseResults := make([]SkillChangeEvalCaseResult, 0, len(req.Cases))
	passed := 0
	failed := 0
	for _, c := range req.Cases {
		result := evaluateSkillChangeEvalCase(c)
		caseResults = append(caseResults, result)
		if result.Status == SkillChangeEvalStatusPassed {
			passed++
		} else {
			failed++
		}
	}
	if failed > 0 {
		stopReasons = append(stopReasons, fmt.Sprintf("%d eval case(s) failed", failed))
		nextActions = append(nextActions, "失敗した eval case の after_output または Skill 変更内容を見直す")
	}

	status := SkillChangeEvalStatusPassed
	if len(stopReasons) > 0 {
		status = SkillChangeEvalStatusBlocked
	}
	summary := fmt.Sprintf("skill change eval %s: passed=%d failed=%d cases=%d", status, passed, failed, len(req.Cases))
	changeLog := SkillChangeLog{
		ChangeID:               req.ChangeID,
		SkillID:                req.SkillID,
		OldVersion:             req.OldVersion,
		NewVersion:             req.NewVersion,
		ChangeReason:           req.ChangeReason,
		ExpectedBehaviorChange: req.ExpectedBehaviorChange,
		EvalResult:             summary,
		EvidenceSummary:        BuildSkillChangeEvidenceSummary(req),
		HumanApprovalStatus:    req.HumanApprovalStatus,
	}
	if status != SkillChangeEvalStatusPassed {
		changeLog.EvalResult = ""
	}
	gate := EvaluateSkillChangeGate(changeLog)
	return SkillChangeEvalResult{
		Status:       status,
		Summary:      summary,
		PassedCount:  passed,
		FailedCount:  failed,
		StopReasons:  stopReasons,
		NextActions:  nextActions,
		CaseResults:  caseResults,
		ChangeLog:    changeLog,
		GateDecision: gate,
	}
}

func BuildSkillChangeEvidenceCases(req SkillChangeEvalRequest) []SkillChangeEvalCase {
	var cases []SkillChangeEvalCase
	diff := strings.TrimSpace(req.SkillDiff)
	if diff != "" {
		mustContain := nonEmptyStrings(req.DiffMustContain)
		if len(mustContain) == 0 {
			mustContain = []string{req.SkillID}
		}
		cases = append(cases, SkillChangeEvalCase{
			Name:             "skill_diff_evidence",
			Input:            "Skill file diff",
			ExpectedBehavior: "Skill diff evidence is attached",
			BeforeOutput:     "no skill diff evidence",
			AfterOutput:      diff,
			MustContain:      mustContain,
		})
	}
	transcript := strings.TrimSpace(req.AgentTranscript)
	if transcript != "" {
		mustContain := nonEmptyStrings(req.TranscriptMustContain)
		if len(mustContain) == 0 {
			mustContain = []string{req.ExpectedBehaviorChange}
		}
		cases = append(cases, SkillChangeEvalCase{
			Name:             "agent_transcript_evidence",
			Input:            "Agent transcript",
			ExpectedBehavior: "Agent transcript demonstrates the expected behavior",
			BeforeOutput:     "no agent transcript evidence",
			AfterOutput:      transcript,
			MustContain:      mustContain,
			MustNotContain:   nonEmptyStrings(req.TranscriptMustNotContain),
		})
	}
	return cases
}

func BuildSkillChangeEvidenceSummary(req SkillChangeEvalRequest) string {
	var parts []string
	if strings.TrimSpace(req.SkillDiff) != "" {
		parts = append(parts, fmt.Sprintf("skill_diff_chars=%d", len(req.SkillDiff)))
		if strings.TrimSpace(req.SkillDiffPath) != "" {
			parts = append(parts, "skill_diff_path="+strings.TrimSpace(req.SkillDiffPath))
		}
		if terms := nonEmptyStrings(req.DiffMustContain); len(terms) > 0 {
			parts = append(parts, "diff_terms="+strings.Join(terms, ","))
		}
	}
	if strings.TrimSpace(req.AgentTranscript) != "" {
		parts = append(parts, fmt.Sprintf("agent_transcript_chars=%d", len(req.AgentTranscript)))
		if strings.TrimSpace(req.AgentTranscriptPath) != "" {
			parts = append(parts, "agent_transcript_path="+strings.TrimSpace(req.AgentTranscriptPath))
		}
		if terms := nonEmptyStrings(req.TranscriptMustContain); len(terms) > 0 {
			parts = append(parts, "transcript_terms="+strings.Join(terms, ","))
		}
		if terms := nonEmptyStrings(req.TranscriptMustNotContain); len(terms) > 0 {
			parts = append(parts, "transcript_forbidden_terms="+strings.Join(terms, ","))
		}
	}
	return strings.Join(parts, "; ")
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func evaluateSkillChangeEvalCase(c SkillChangeEvalCase) SkillChangeEvalCaseResult {
	var reasons []string
	var hits []string
	if strings.TrimSpace(c.Name) == "" {
		reasons = append(reasons, "case name is required")
	}
	if strings.TrimSpace(c.Input) == "" {
		reasons = append(reasons, "case input is required")
	}
	if strings.TrimSpace(c.ExpectedBehavior) == "" {
		reasons = append(reasons, "expected_behavior is required")
	}
	if strings.TrimSpace(c.BeforeOutput) == "" {
		reasons = append(reasons, "before_output is required")
	}
	if strings.TrimSpace(c.AfterOutput) == "" {
		reasons = append(reasons, "after_output is required")
	}
	mustContain := c.MustContain
	if len(mustContain) == 0 && strings.TrimSpace(c.ExpectedBehavior) != "" {
		mustContain = []string{c.ExpectedBehavior}
	}
	for _, term := range mustContain {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		if strings.Contains(c.AfterOutput, term) {
			hits = append(hits, term)
		} else {
			reasons = append(reasons, fmt.Sprintf("after_output must contain %q", term))
		}
	}
	for _, term := range c.MustNotContain {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		if strings.Contains(c.AfterOutput, term) {
			reasons = append(reasons, fmt.Sprintf("after_output must not contain %q", term))
		}
	}
	status := SkillChangeEvalStatusPassed
	if len(reasons) > 0 {
		status = SkillChangeEvalStatusBlocked
	}
	return SkillChangeEvalCaseResult{
		Name:        c.Name,
		Status:      status,
		Reasons:     reasons,
		Changed:     strings.TrimSpace(c.BeforeOutput) != strings.TrimSpace(c.AfterOutput),
		ContainHits: hits,
	}
}
