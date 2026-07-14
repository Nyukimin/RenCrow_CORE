package heartbeat

import (
	"context"
	"testing"
	"time"

	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

type memoryEconomicDiscoveryStore struct {
	market        []domainrevenue.MarketResearchItem
	products      []domainrevenue.Product
	voices        []domainrevenue.CustomerVoice
	events        []domainrevenue.RevenueEvent
	opportunities []domainrevenue.Opportunity
	saved         []domainrevenue.Opportunity
	listCalls     int
}

func (s *memoryEconomicDiscoveryStore) ListMarketResearchItems(context.Context, int) ([]domainrevenue.MarketResearchItem, error) {
	s.listCalls++
	return append([]domainrevenue.MarketResearchItem(nil), s.market...), nil
}

func (s *memoryEconomicDiscoveryStore) ListProducts(context.Context, int) ([]domainrevenue.Product, error) {
	s.listCalls++
	return append([]domainrevenue.Product(nil), s.products...), nil
}

func (s *memoryEconomicDiscoveryStore) ListCustomerVoices(context.Context, int) ([]domainrevenue.CustomerVoice, error) {
	s.listCalls++
	return append([]domainrevenue.CustomerVoice(nil), s.voices...), nil
}

func (s *memoryEconomicDiscoveryStore) ListRevenueEvents(context.Context, int) ([]domainrevenue.RevenueEvent, error) {
	s.listCalls++
	return append([]domainrevenue.RevenueEvent(nil), s.events...), nil
}

func (s *memoryEconomicDiscoveryStore) ListOpportunities(context.Context, int) ([]domainrevenue.Opportunity, error) {
	s.listCalls++
	return append([]domainrevenue.Opportunity(nil), s.opportunities...), nil
}

func (s *memoryEconomicDiscoveryStore) SaveOpportunity(_ context.Context, item domainrevenue.Opportunity) error {
	s.saved = append(s.saved, item)
	s.opportunities = append([]domainrevenue.Opportunity{item}, s.opportunities...)
	return nil
}

type memoryEconomicGoalStore struct {
	goals []domainworkstream.Goal
}

func (s *memoryEconomicGoalStore) ListGoals(context.Context, int) ([]domainworkstream.Goal, error) {
	return append([]domainworkstream.Goal(nil), s.goals...), nil
}

func TestRunEconomicOpportunityDiscoveryRequiresDraftOnlyConditions(t *testing.T) {
	store := &memoryEconomicDiscoveryStore{market: []domainrevenue.MarketResearchItem{{ItemID: "m1", Theme: "Safe theme"}}}
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	tests := []EconomicObjectiveDiscoveryOptions{
		{},
		{Enabled: true, DraftOnly: false, HeartbeatDiscoveryEnabled: true, DailyOpportunityLimit: 5},
		{Enabled: true, DraftOnly: true, HeartbeatDiscoveryEnabled: false, DailyOpportunityLimit: 5},
	}
	for _, options := range tests {
		svc := NewHeartbeatService(&mockWorkerAgent{}, nil, t.TempDir(), 30).
			WithEconomicObjectiveDiscovery(store, nil, options)
		report, err := svc.RunEconomicOpportunityDiscovery(context.Background(), now)
		if err != nil {
			t.Fatalf("RunEconomicOpportunityDiscovery() error=%v", err)
		}
		if report.Created != 0 || report.Status != "skipped" {
			t.Fatalf("options=%+v report=%+v", options, report)
		}
	}
	if len(store.saved) != 0 || store.listCalls != 0 {
		t.Fatalf("disabled discovery touched store: saved=%d list_calls=%d", len(store.saved), store.listCalls)
	}
}

func TestRunEconomicOpportunityDiscoveryCreatesOnlyDraftsFromAllSources(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	store := &memoryEconomicDiscoveryStore{
		market:   []domainrevenue.MarketResearchItem{{ItemID: "m1", Theme: "Local LLM guide", ObservedSignal: "repeat questions", CreatedAt: now.Add(-time.Hour)}},
		products: []domainrevenue.Product{{ProductID: "p1", ProductName: "Template", Price: 3000, Target: "developers", Status: "draft", CreatedAt: now.Add(-2 * time.Hour)}},
		voices:   []domainrevenue.CustomerVoice{{VoiceID: "v1", Summary: "setup is difficult", RawText: "private raw voice", PermissionStatus: "pending", CreatedAt: now.Add(-3 * time.Hour)}},
		events:   []domainrevenue.RevenueEvent{{EventID: "r1", EventType: "purchase", Amount: 1200, Notes: "repeatable sale", CreatedAt: now.Add(-4 * time.Hour)}},
	}
	goals := &memoryEconomicGoalStore{goals: []domainworkstream.Goal{{GoalID: "g1", WorkstreamID: "ws1", Title: "Reusable report", Description: "draft report", Status: domainworkstream.StatusDraft, CreatedAt: now.Add(-5 * time.Hour)}}}
	svc := NewHeartbeatService(&mockWorkerAgent{}, nil, t.TempDir(), 30).
		WithEconomicObjectiveDiscovery(store, goals, EconomicObjectiveDiscoveryOptions{
			Enabled: true, DraftOnly: true, HeartbeatDiscoveryEnabled: true, DailyOpportunityLimit: 5,
		})

	report, err := svc.RunEconomicOpportunityDiscovery(context.Background(), now)
	if err != nil {
		t.Fatalf("RunEconomicOpportunityDiscovery() error=%v", err)
	}
	if report.Status != "ok" || report.Created != 5 || report.ExternalActionsApplied {
		t.Fatalf("report=%+v", report)
	}
	if len(store.saved) != 5 {
		t.Fatalf("saved=%d, want 5", len(store.saved))
	}
	for _, item := range store.saved {
		if item.ApprovalState != "draft" || item.CreatedAt.IsZero() {
			t.Fatalf("non-draft opportunity=%+v", item)
		}
		if item.Summary == "private raw voice" {
			t.Fatal("customer voice raw text leaked into opportunity")
		}
		if item.ExpectedProfit != item.ExpectedRevenue-item.ExpectedCost {
			t.Fatalf("economics not normalized: %+v", item)
		}
	}
}

func TestRunEconomicOpportunityDiscoveryIsIdempotent(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	store := &memoryEconomicDiscoveryStore{market: []domainrevenue.MarketResearchItem{{ItemID: "m1", Theme: "Safe theme", CreatedAt: now}}}
	svc := NewHeartbeatService(&mockWorkerAgent{}, nil, t.TempDir(), 30).
		WithEconomicObjectiveDiscovery(store, nil, EconomicObjectiveDiscoveryOptions{
			Enabled: true, DraftOnly: true, HeartbeatDiscoveryEnabled: true, DailyOpportunityLimit: 5,
		})
	if _, err := svc.RunEconomicOpportunityDiscovery(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	report, err := svc.RunEconomicOpportunityDiscovery(context.Background(), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(store.saved) != 1 || report.Created != 0 || report.DuplicateSkipped != 1 {
		t.Fatalf("saved=%d report=%+v", len(store.saved), report)
	}
}
