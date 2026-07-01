package dci

import (
	"fmt"
	"strings"
)

func ValidateSearchTrace(trace SearchTrace) error {
	if strings.TrimSpace(trace.EventID) == "" {
		return fmt.Errorf("dci search trace event_id is required")
	}
	if trace.StartedAt.IsZero() {
		return fmt.Errorf("dci search trace started_at is required")
	}
	if strings.TrimSpace(trace.Actor) == "" {
		return fmt.Errorf("dci search trace actor is required")
	}
	if strings.TrimSpace(trace.Mode) == "" {
		return fmt.Errorf("dci search trace mode is required")
	}
	if strings.TrimSpace(trace.UserQuery) == "" {
		return fmt.Errorf("dci search trace user_query is required")
	}
	status := strings.TrimSpace(trace.Status)
	if status == "" {
		return fmt.Errorf("dci search trace status is required")
	}
	if !isSearchTraceStatus(status) {
		return fmt.Errorf("dci search trace invalid status %q", trace.Status)
	}
	if isTerminalSearchTraceStatus(status) && trace.EndedAt.IsZero() {
		return fmt.Errorf("dci search trace terminal status %q requires ended_at", status)
	}
	if status == "failed" && strings.TrimSpace(trace.ErrorMessage) == "" {
		return fmt.Errorf("dci search trace failed status requires error_message")
	}
	if trace.FinalEvidenceCount < 0 {
		return fmt.Errorf("dci search trace final_evidence_count must be >= 0")
	}
	seenSteps := make(map[int]struct{}, len(trace.Steps))
	for _, step := range trace.Steps {
		if err := ValidateSearchStep(step); err != nil {
			return err
		}
		if _, ok := seenSteps[step.StepNo]; ok {
			return fmt.Errorf("dci search trace duplicate step_no %d", step.StepNo)
		}
		seenSteps[step.StepNo] = struct{}{}
	}
	return nil
}

func ValidateSearchStep(step SearchStep) error {
	if step.StepNo <= 0 {
		return fmt.Errorf("dci search step step_no is required")
	}
	if strings.TrimSpace(step.Tool) == "" {
		return fmt.Errorf("dci search step tool is required")
	}
	status := strings.TrimSpace(step.Status)
	if status == "" {
		return fmt.Errorf("dci search step status is required")
	}
	if !isSearchStepStatus(status) {
		return fmt.Errorf("dci search step invalid status %q", step.Status)
	}
	if status == "error" && strings.TrimSpace(step.ErrorMessage) == "" {
		return fmt.Errorf("dci search step error status requires error_message")
	}
	if step.ResultCount < 0 {
		return fmt.Errorf("dci search step result_count must be >= 0")
	}
	if step.CreatedAt.IsZero() {
		return fmt.Errorf("dci search step created_at is required")
	}
	return nil
}

func isSearchTraceStatus(status string) bool {
	switch status {
	case "completed", "failed":
		return true
	default:
		return false
	}
}

func isTerminalSearchTraceStatus(status string) bool {
	switch status {
	case "completed", "failed":
		return true
	default:
		return false
	}
}

func isSearchStepStatus(status string) bool {
	switch status {
	case "ok", "error", "stopped", "completed":
		return true
	default:
		return false
	}
}
