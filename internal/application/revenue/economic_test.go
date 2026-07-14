package revenue

import (
	"context"
	"testing"
	"time"

	revenuedomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
)

type fakeOpportunityStore struct {
	item revenuedomain.Opportunity
}

func (f *fakeOpportunityStore) SaveOpportunity(_ context.Context, item revenuedomain.Opportunity) error {
	f.item = item
	return nil
}

func TestEconomicServiceDraftOpportunityCalculatesProfitAndStores(t *testing.T) {
	store := &fakeOpportunityStore{}
	now := time.Date(2026, 7, 14, 1, 2, 3, 0, time.UTC)
	service := NewEconomicService(store, func() time.Time { return now })
	item, err := service.DraftOpportunity(context.Background(), revenuedomain.Opportunity{
		OpportunityID:   "opp-1",
		SourceKind:      "market_research",
		Title:           "LLM調査レポート",
		ExpectedRevenue: 5000,
		ExpectedCost:    1000,
		RiskScore:       0.2,
	})
	if err != nil {
		t.Fatalf("DraftOpportunity failed: %v", err)
	}
	if item.ExpectedProfit != 4000 || store.item.ExpectedProfit != 4000 {
		t.Fatalf("expected profit not calculated: item=%#v stored=%#v", item, store.item)
	}
	if item.ApprovalState != "draft" || !item.CreatedAt.Equal(now) {
		t.Fatalf("defaults not applied: %#v", item)
	}
}

func TestGoalFromOpportunity(t *testing.T) {
	goal, err := GoalFromOpportunity(revenuedomain.Opportunity{
		OpportunityID:   "opp-1",
		SourceKind:      "market_research",
		Title:           "LLM調査レポート",
		Summary:         "下書きを作る",
		ExpectedRevenue: 5000,
		ExpectedCost:    1000,
		RiskScore:       0.2,
		ApprovalState:   "draft",
		CreatedAt:       time.Now(),
	}, "ws-1", time.Now())
	if err != nil {
		t.Fatalf("GoalFromOpportunity failed: %v", err)
	}
	if goal.WorkstreamID != "ws-1" || goal.Status != "draft" || len(goal.SuccessCriteria) == 0 {
		t.Fatalf("unexpected goal: %#v", goal)
	}
}
