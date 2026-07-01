package revenue

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domainrevenue "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/revenue"
)

func TestSQLiteStoreSaveAndListRevenueRecords(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "revenue.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	if err := store.SaveMarketResearchItem(ctx, domainrevenue.MarketResearchItem{
		ItemID:         "mkt_1",
		SourcePlatform: "note",
		Theme:          "AI商品設計",
		CreatedAt:      now,
	}); err != nil {
		t.Fatalf("SaveMarketResearchItem failed: %v", err)
	}
	if err := store.SaveSNSPostMetric(ctx, domainrevenue.SNSPostMetric{
		PostID:      "post_1",
		Platform:    "x",
		Impressions: 100,
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("SaveSNSPostMetric failed: %v", err)
	}
	if err := store.SaveProduct(ctx, domainrevenue.Product{
		ProductID:   "prod_1",
		ProductName: "商品設計シート",
		Status:      "draft",
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("SaveProduct failed: %v", err)
	}
	if err := store.SaveCustomerVoice(ctx, domainrevenue.CustomerVoice{
		VoiceID:          "voice_1",
		RawText:          "ここがわからない",
		PermissionStatus: "unknown",
		CreatedAt:        now,
	}); err != nil {
		t.Fatalf("SaveCustomerVoice failed: %v", err)
	}
	if err := store.SaveRevenueEvent(ctx, domainrevenue.RevenueEvent{
		EventID:   "rev_1",
		EventType: "purchase",
		Amount:    980,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("SaveRevenueEvent failed: %v", err)
	}
	if err := store.SaveHumanDecisionGateRecord(ctx, domainrevenue.HumanDecisionGateRecord{
		DecisionID:       "dec_1",
		DecisionType:     "high_ticket_offer",
		ApprovalStatus:   "pending",
		GateStatus:       "needs_review",
		RequiresApproval: true,
		CreatedAt:        now,
	}); err != nil {
		t.Fatalf("SaveHumanDecisionGateRecord failed: %v", err)
	}
	if err := store.SaveDailyRoutineReport(ctx, domainrevenue.DailyRoutineReport{
		ReportID:            "daily_1",
		Date:                "2026-05-18",
		Status:              "draft_report",
		ExternalSendApplied: false,
		CreatedAt:           now,
	}); err != nil {
		t.Fatalf("SaveDailyRoutineReport failed: %v", err)
	}
	if err := store.SaveChannelDraft(ctx, domainrevenue.ChannelDraft{
		DraftID:        "draft_1",
		Channel:        "email",
		Subject:        "購入者向け案内",
		Body:           "下書き本文",
		ApprovalStatus: "pending",
		CreatedAt:      now,
	}); err != nil {
		t.Fatalf("SaveChannelDraft failed: %v", err)
	}
	if err := store.SaveExternalSendApplyRecord(ctx, domainrevenue.ExternalSendApplyRecord{
		ApplyID:             "apply_1",
		DraftID:             "draft_1",
		DecisionID:          "dec_1",
		Channel:             "email",
		ApprovalStatus:      "approved",
		HumanApproved:       true,
		ApplyStatus:         "blocked",
		SendResult:          "not_sent",
		FailureReason:       "external channel adapter is not configured",
		ExternalSendApplied: false,
		CreatedAt:           now,
	}); err != nil {
		t.Fatalf("SaveExternalSendApplyRecord failed: %v", err)
	}
	assertOne := func(name string, err error, got int) {
		t.Helper()
		if err != nil || got != 1 {
			t.Fatalf("%s count = %d, err = %v", name, got, err)
		}
	}
	market, err := store.ListMarketResearchItems(ctx, 10)
	assertOne("market", err, len(market))
	posts, err := store.ListSNSPostMetrics(ctx, 10)
	assertOne("posts", err, len(posts))
	products, err := store.ListProducts(ctx, 10)
	assertOne("products", err, len(products))
	voices, err := store.ListCustomerVoices(ctx, 10)
	assertOne("voices", err, len(voices))
	events, err := store.ListRevenueEvents(ctx, 10)
	assertOne("events", err, len(events))
	decisions, err := store.ListHumanDecisionGateRecords(ctx, 10)
	assertOne("human decisions", err, len(decisions))
	daily, err := store.ListDailyRoutineReports(ctx, 10)
	assertOne("daily routine reports", err, len(daily))
	drafts, err := store.ListChannelDrafts(ctx, 10)
	assertOne("channel drafts", err, len(drafts))
	applies, err := store.ListExternalSendApplyRecords(ctx, 10)
	assertOne("external send applies", err, len(applies))
}

func TestSQLiteStoreRejectsSuccessGuaranteeProduct(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "revenue.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	err = store.SaveProduct(context.Background(), domainrevenue.Product{
		ProductID:   "prod_1",
		ProductName: "AI副業テンプレ",
		Promise:     "誰でも必ず稼げる",
		Status:      "draft",
		CreatedAt:   time.Now(),
	})
	if err == nil {
		t.Fatal("expected success guarantee product to fail")
	}
}
