package advisor

import (
	"strings"
	"testing"
	"time"
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

func TestAdviceRunRecordValidate(t *testing.T) {
	now := time.Now().UTC()
	record := AdviceRunRecord{
		RunID:            "run-1",
		RequestedByAgent: "shiro",
		AdvisorID:        AdvisorCodex,
		ApprovalMode:     "advice_only",
		Status:           AdviceStatus(StatusCompleted),
		StartedAt:        now,
		FinishedAt:       now.Add(time.Second),
		LatencyMillis:    1000,
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
}

func TestAdviceRunRecordValidateRejectsUnsupportedPolicyValues(t *testing.T) {
	record := AdviceRunRecord{
		RunID:            "run-1",
		RequestedByAgent: "shiro",
		AdvisorID:        AdvisorCodex,
		ApprovalMode:     "auto_apply",
		Status:           AdviceStatus("running"),
	}
	if err := record.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestAdvisorAdoptionRecordValidateRejectsUnknownOutcome(t *testing.T) {
	record := AdvisorAdoptionRecord{
		AdoptionID:     "adoption-1",
		RunID:          "run-1",
		AdvisorID:      AdvisorCodex,
		AdoptedByAgent: "shiro",
		Outcome:        "unknown",
		CreatedAt:      time.Now().UTC(),
	}
	if err := record.Validate(); err == nil {
		t.Fatal("expected outcome validation error")
	}
}

func TestAdvisorScoreSnapshotValidateRejectsOutOfRangeScore(t *testing.T) {
	snapshot := AdvisorScoreSnapshot{
		SnapshotID: "snapshot-1",
		AdvisorID:  AdvisorCodex,
		Score:      1.1,
		CreatedAt:  time.Now().UTC(),
	}
	if err := snapshot.Validate(); err == nil {
		t.Fatal("expected score validation error")
	}
}
