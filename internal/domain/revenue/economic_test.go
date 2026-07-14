package revenue

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeOpportunityEconomics(t *testing.T) {
	got := NormalizeOpportunityEconomics(Opportunity{ExpectedRevenue: 10000, ExpectedCost: 2500})
	if got.ExpectedProfit != 7500 {
		t.Fatalf("ExpectedProfit = %d, want 7500", got.ExpectedProfit)
	}
	if got.ProfitMargin != 0.75 {
		t.Fatalf("ProfitMargin = %f, want 0.75", got.ProfitMargin)
	}
}

func TestValidateEconomicTaskRequiresHumanApprovalForPublish(t *testing.T) {
	err := ValidateEconomicTask(EconomicTask{
		TaskID:        "task-1",
		OpportunityID: "opp-1",
		AgentID:       "shiro",
		TaskKind:      "external_publish",
		Status:        "draft",
		ApprovalMode:  "auto",
		CreatedAt:     time.Now(),
	})
	if err == nil || !strings.Contains(err.Error(), "human_required") {
		t.Fatalf("expected human approval error, got %v", err)
	}
}

func TestValidateOpportunityRejectsProhibitedClaim(t *testing.T) {
	err := ValidateOpportunity(Opportunity{
		OpportunityID:   "opp-1",
		SourceKind:      "market_research",
		Title:           "誰でも必ず稼げるテンプレート",
		ExpectedRevenue: 1000,
		ApprovalState:   "draft",
		CreatedAt:       time.Now(),
	})
	if err == nil || !strings.Contains(err.Error(), "prohibited") {
		t.Fatalf("expected prohibited claim error, got %v", err)
	}
}
