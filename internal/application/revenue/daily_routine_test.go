package revenue

import (
	"context"
	"testing"
	"time"

	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
)

type memoryDailyRoutineStore struct {
	market    []domainrevenue.MarketResearchItem
	posts     []domainrevenue.SNSPostMetric
	products  []domainrevenue.Product
	voices    []domainrevenue.CustomerVoice
	events    []domainrevenue.RevenueEvent
	decisions []domainrevenue.HumanDecisionGateRecord
	reports   []domainrevenue.DailyRoutineReport
	lastLimit int
}

func (s *memoryDailyRoutineStore) ListMarketResearchItems(_ context.Context, limit int) ([]domainrevenue.MarketResearchItem, error) {
	s.lastLimit = limit
	return append([]domainrevenue.MarketResearchItem(nil), s.market...), nil
}

func (s *memoryDailyRoutineStore) ListSNSPostMetrics(_ context.Context, _ int) ([]domainrevenue.SNSPostMetric, error) {
	return append([]domainrevenue.SNSPostMetric(nil), s.posts...), nil
}

func (s *memoryDailyRoutineStore) ListProducts(_ context.Context, _ int) ([]domainrevenue.Product, error) {
	return append([]domainrevenue.Product(nil), s.products...), nil
}

func (s *memoryDailyRoutineStore) ListCustomerVoices(_ context.Context, _ int) ([]domainrevenue.CustomerVoice, error) {
	return append([]domainrevenue.CustomerVoice(nil), s.voices...), nil
}

func (s *memoryDailyRoutineStore) ListRevenueEvents(_ context.Context, _ int) ([]domainrevenue.RevenueEvent, error) {
	return append([]domainrevenue.RevenueEvent(nil), s.events...), nil
}

func (s *memoryDailyRoutineStore) ListHumanDecisionGateRecords(_ context.Context, _ int) ([]domainrevenue.HumanDecisionGateRecord, error) {
	return append([]domainrevenue.HumanDecisionGateRecord(nil), s.decisions...), nil
}

func (s *memoryDailyRoutineStore) SaveDailyRoutineReport(_ context.Context, item domainrevenue.DailyRoutineReport) error {
	if err := domainrevenue.ValidateDailyRoutineReport(item); err != nil {
		return err
	}
	s.reports = append(s.reports, item)
	return nil
}

func TestDailyRoutineServiceCreatesRevenueAgentDraftOnlyReport(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &memoryDailyRoutineStore{
		market:   []domainrevenue.MarketResearchItem{{ItemID: "mkt_1", SourcePlatform: "note"}},
		posts:    []domainrevenue.SNSPostMetric{{PostID: "post_1", Platform: "x"}},
		products: []domainrevenue.Product{{ProductID: "prod_1", ProductName: "商品設計シート", Status: "draft"}},
		voices:   []domainrevenue.CustomerVoice{{VoiceID: "voice_1", RawText: "ここがわからない", PermissionStatus: "unknown"}},
		events:   []domainrevenue.RevenueEvent{{EventID: "rev_1", EventType: "purchase", Amount: 980, CustomerID: "cust_1"}},
		decisions: []domainrevenue.HumanDecisionGateRecord{{
			DecisionID:       "dec_1",
			DecisionType:     "external_publish",
			ApprovalStatus:   "pending",
			GateStatus:       "needs_review",
			RequiresApproval: true,
		}},
	}
	result, err := NewDailyRoutineService(store).RunDailyRoutine(context.Background(), DailyRoutineRequest{
		ReportID:     "daily_1",
		WorkstreamID: "ws_revenue",
		Date:         "2026-05-18",
		Limit:        20,
		Now:          now,
	})
	if err != nil {
		t.Fatalf("RunDailyRoutine failed: %v", err)
	}
	if result.Agent != AgentRevenue || result.Mode != ModeDraftReportOnly {
		t.Fatalf("unexpected routine metadata: %#v", result)
	}
	if result.ExternalActionsApplied || !result.HumanApprovalRequiredForExternalActions {
		t.Fatalf("expected draft-only external action gate: %#v", result)
	}
	if len(store.reports) != 1 {
		t.Fatalf("expected one saved report, got %#v", store.reports)
	}
	report := store.reports[0]
	if report.ReportID != "daily_1" || report.WorkstreamID != "ws_revenue" || report.Status != "draft_report" || report.ExternalSendApplied {
		t.Fatalf("unexpected saved report: %#v", report)
	}
	if report.MarketResearch != 1 || report.SNSPosts != 1 || report.Products != 1 || report.CustomerVoices != 1 || report.RevenueEvents != 1 || report.PaidCustomers != 1 || report.PendingDecisions != 1 {
		t.Fatalf("unexpected counts: %#v", report)
	}
	if store.lastLimit != 20 {
		t.Fatalf("expected limit=20, got %d", store.lastLimit)
	}
}

func TestDailyRoutineServiceRejectsTooLargeLimit(t *testing.T) {
	_, err := NewDailyRoutineService(&memoryDailyRoutineStore{}).RunDailyRoutine(context.Background(), DailyRoutineRequest{Limit: 201})
	if err == nil {
		t.Fatal("expected limit error")
	}
}
