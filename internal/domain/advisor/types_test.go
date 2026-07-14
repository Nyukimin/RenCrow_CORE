package advisor

import (
	"strings"
	"testing"
)

func TestAdviceRequestValidate(t *testing.T) {
	req := AdviceRequest{
		RequestedByAgent: "shiro",
		AdvisorID:        AdvisorCodex,
		Purpose:          "code_advice",
		Prompt:           "調査して",
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
}

func TestAdviceRequestValidateRejectsMissingPrompt(t *testing.T) {
	req := AdviceRequest{
		RequestedByAgent: "shiro",
		AdvisorID:        AdvisorCodex,
		Purpose:          "code_advice",
	}
	err := req.Validate()
	if err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("expected prompt validation error, got %v", err)
	}
}

func TestAdviceResultOutputTextPrefersSummary(t *testing.T) {
	result := AdviceResult{
		Summary: "summary",
		Plan:    "plan",
		Patch:   "patch",
	}
	if got := result.OutputText(); got != "summary" {
		t.Fatalf("OutputText = %q, want summary", got)
	}
}
