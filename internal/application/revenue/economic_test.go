package revenue

import (
	"context"
	"errors"
	"testing"
	"time"

	revenuedomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
	workstreamdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

type fakeOpportunityStore struct {
	item          revenuedomain.Opportunity
	opportunities []revenuedomain.Opportunity
	tasks         []revenuedomain.EconomicTask
	reflections   []revenuedomain.EconomicReflection
	events        []revenuedomain.RevenueEvent
}

func (f *fakeOpportunityStore) SaveOpportunity(_ context.Context, item revenuedomain.Opportunity) error {
	f.item = item
	return nil
}

func (f *fakeOpportunityStore) ListOpportunities(context.Context, int) ([]revenuedomain.Opportunity, error) {
	return append([]revenuedomain.Opportunity(nil), f.opportunities...), nil
}

func (f *fakeOpportunityStore) SaveEconomicTask(_ context.Context, item revenuedomain.EconomicTask) error {
	f.tasks = append(f.tasks, item)
	return nil
}

func (f *fakeOpportunityStore) ListEconomicTasks(context.Context, int) ([]revenuedomain.EconomicTask, error) {
	return append([]revenuedomain.EconomicTask(nil), f.tasks...), nil
}

func (f *fakeOpportunityStore) SaveEconomicReflection(_ context.Context, item revenuedomain.EconomicReflection) error {
	f.reflections = append(f.reflections, item)
	return nil
}

func (f *fakeOpportunityStore) ListEconomicReflections(context.Context, int) ([]revenuedomain.EconomicReflection, error) {
	return append([]revenuedomain.EconomicReflection(nil), f.reflections...), nil
}

func (f *fakeOpportunityStore) ListRevenueEvents(context.Context, int) ([]revenuedomain.RevenueEvent, error) {
	return append([]revenuedomain.RevenueEvent(nil), f.events...), nil
}

type fakeGoalStore struct{ goals []workstreamdomain.Goal }

func (f *fakeGoalStore) SaveGoal(_ context.Context, item workstreamdomain.Goal) error {
	f.goals = append(f.goals, item)
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
	zeroRevenue, err := service.DraftOpportunity(context.Background(), revenuedomain.Opportunity{
		OpportunityID: "opp-zero", SourceKind: "market_research", Title: "Zero revenue draft", ProfitMargin: 0.9,
	})
	if err != nil {
		t.Fatalf("DraftOpportunity zero revenue failed: %v", err)
	}
	if zeroRevenue.ProfitMargin != 0 || zeroRevenue.ExpectedProfit != 0 {
		t.Fatalf("zero revenue economics not normalized: %#v", zeroRevenue)
	}
}

func TestEconomicServiceRejectsApprovalMismatchAndCreatesDraftGoal(t *testing.T) {
	now := time.Date(2026, 7, 14, 1, 2, 3, 0, time.UTC)
	store := &fakeOpportunityStore{opportunities: []revenuedomain.Opportunity{{
		OpportunityID: "opp-negative", SourceKind: "note", Title: "Internal draft", ExpectedRevenue: 100,
		ExpectedCost: 200, ApprovalState: "draft", CreatedAt: now,
	}}}
	goals := &fakeGoalStore{}
	service := NewEconomicService(store, func() time.Time { return now }).WithWorkstreamGoalStore(goals)
	if _, err := service.DraftEconomicTask(context.Background(), revenuedomain.EconomicTask{
		TaskID: "task-1", OpportunityID: "opp-negative", AgentID: "shiro", TaskKind: "billing", ApprovalMode: "none",
	}); err == nil {
		t.Fatal("billing without human approval must be rejected")
	}
	goal, err := service.CreateWorkstreamGoal(context.Background(), "opp-negative", "ws-revenue")
	if err != nil {
		t.Fatalf("CreateWorkstreamGoal failed: %v", err)
	}
	if goal.Status != "draft" || len(goals.goals) != 1 {
		t.Fatalf("goal=%#v saved=%#v", goal, goals.goals)
	}
	if _, err := service.CreateWorkstreamGoal(context.Background(), "missing", "ws-revenue"); !errors.Is(err, ErrOpportunityNotFound) {
		t.Fatalf("missing opportunity error=%v", err)
	}
}

func TestEconomicServiceReflectRevenueEvent(t *testing.T) {
	now := time.Date(2026, 7, 14, 1, 2, 3, 0, time.UTC)
	store := &fakeOpportunityStore{
		opportunities: []revenuedomain.Opportunity{{OpportunityID: "opp-1", SourceKind: "note", Title: "Draft", ExpectedCost: 300, ApprovalState: "draft", CreatedAt: now}},
		events:        []revenuedomain.RevenueEvent{{EventID: "rev-1", EventType: "sold", Amount: 1000, CreatedAt: now}},
	}
	service := NewEconomicService(store, func() time.Time { return now })
	reflection, err := service.ReflectRevenueEvent(context.Background(), ReflectionFromRevenueEventRequest{
		ReflectionID: "reflection-1", OpportunityID: "opp-1", RevenueEventID: "rev-1", Outcome: "sold", Lessons: []string{"reuse"},
	})
	if err != nil {
		t.Fatalf("ReflectRevenueEvent failed: %v", err)
	}
	if reflection.NetProfit != 700 || len(store.reflections) != 1 {
		t.Fatalf("reflection=%#v", reflection)
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
