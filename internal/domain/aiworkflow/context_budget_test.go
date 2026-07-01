package aiworkflow

import (
	"testing"
	"time"
)

func TestEvaluateContextBudgetDisabled(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 15, 0, 0, time.UTC)
	decision, err := EvaluateContextBudget(ContextUsage{EventID: "ctx_1", Agent: "Coder", ContextTokens: 9000, CreatedAt: now}, ContextBudgetPolicy{})
	if err != nil {
		t.Fatalf("EvaluateContextBudget failed: %v", err)
	}
	if decision.Status != ContextBudgetStatusOK || decision.Reason != "context budget disabled" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestEvaluateContextBudgetWarnsAndStops(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 15, 0, 0, time.UTC)
	policy := ContextBudgetPolicy{MaxContextTokens: 1000, WarnAtRatio: 0.8, StopAtRatio: 0.95}
	warn, err := EvaluateContextBudget(ContextUsage{EventID: "ctx_warn", Agent: "Coder", ContextTokens: 850, CreatedAt: now}, policy)
	if err != nil {
		t.Fatalf("warn failed: %v", err)
	}
	if warn.Status != ContextBudgetStatusWarn {
		t.Fatalf("warn decision = %#v", warn)
	}
	stop, err := EvaluateContextBudget(ContextUsage{EventID: "ctx_stop", Agent: "Coder", ContextTokens: 950, CreatedAt: now}, policy)
	if err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	if stop.Status != ContextBudgetStatusStop {
		t.Fatalf("stop decision = %#v", stop)
	}
}

func TestEvaluateContextBudgetRejectsInvalidUsage(t *testing.T) {
	_, err := EvaluateContextBudget(ContextUsage{EventID: "ctx_1", Agent: "Coder", ContextTokens: -1}, ContextBudgetPolicy{MaxContextTokens: 1000})
	if err == nil {
		t.Fatal("expected invalid usage error")
	}
}
