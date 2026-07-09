package revenue

import (
	"context"
	"testing"
	"time"

	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
)

func TestJSONLStoreSaveAndListRevenueRecords(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
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

	markets, err := store.ListMarketResearchItems(ctx, 10)
	if err != nil || len(markets) != 1 || markets[0].ItemID != "mkt_1" {
		t.Fatalf("markets=%#v err=%v", markets, err)
	}
	posts, err := store.ListSNSPostMetrics(ctx, 10)
	if err != nil || len(posts) != 1 || posts[0].PostID != "post_1" {
		t.Fatalf("posts=%#v err=%v", posts, err)
	}
	products, err := store.ListProducts(ctx, 10)
	if err != nil || len(products) != 1 || products[0].ProductID != "prod_1" {
		t.Fatalf("products=%#v err=%v", products, err)
	}
	voices, err := store.ListCustomerVoices(ctx, 10)
	if err != nil || len(voices) != 1 || voices[0].VoiceID != "voice_1" {
		t.Fatalf("voices=%#v err=%v", voices, err)
	}
	events, err := store.ListRevenueEvents(ctx, 10)
	if err != nil || len(events) != 1 || events[0].EventID != "rev_1" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	decisions, err := store.ListHumanDecisionGateRecords(ctx, 10)
	if err != nil || len(decisions) != 1 || decisions[0].DecisionID != "dec_1" {
		t.Fatalf("decisions=%#v err=%v", decisions, err)
	}
	daily, err := store.ListDailyRoutineReports(ctx, 10)
	if err != nil || len(daily) != 1 || daily[0].ReportID != "daily_1" {
		t.Fatalf("daily=%#v err=%v", daily, err)
	}
	drafts, err := store.ListChannelDrafts(ctx, 10)
	if err != nil || len(drafts) != 1 || drafts[0].DraftID != "draft_1" {
		t.Fatalf("drafts=%#v err=%v", drafts, err)
	}
	applies, err := store.ListExternalSendApplyRecords(ctx, 10)
	if err != nil || len(applies) != 1 || applies[0].ApplyID != "apply_1" {
		t.Fatalf("applies=%#v err=%v", applies, err)
	}
}

func TestJSONLStoreRejectsInvalidRevenueRecords(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	if err := store.SaveMarketResearchItem(ctx, domainrevenue.MarketResearchItem{ItemID: "mkt_1"}); err == nil {
		t.Fatal("expected invalid market research to fail")
	}
	if err := store.SaveProduct(ctx, domainrevenue.Product{ProductID: "prod_1", ProductName: "必ず稼げる商品", Status: "draft"}); err == nil {
		t.Fatal("expected prohibited product claim to fail")
	}
	if err := store.SaveCustomerVoice(ctx, domainrevenue.CustomerVoice{VoiceID: "voice_1", RawText: "よかった", PermissionStatus: "unknown", UsableForMarketing: true}); err == nil {
		t.Fatal("expected marketing voice without permission to fail")
	}
	if err := store.SaveHumanDecisionGateRecord(ctx, domainrevenue.HumanDecisionGateRecord{DecisionID: "dec_1", DecisionType: "external_publish", ApprovalStatus: "granted", GateStatus: "approved"}); err == nil {
		t.Fatal("expected invalid decision approval status to fail")
	}
	if err := store.SaveDailyRoutineReport(ctx, domainrevenue.DailyRoutineReport{ReportID: "daily_1", Date: "2026-05-18", Status: "sent", ExternalSendApplied: true}); err == nil {
		t.Fatal("expected non-draft daily routine report to fail")
	}
	if err := store.SaveChannelDraft(ctx, domainrevenue.ChannelDraft{DraftID: "draft_1", Channel: "email", Body: "送信済み", ApprovalStatus: "approved", ExternalSendApplied: true}); err == nil {
		t.Fatal("expected externally applied channel draft to fail")
	}
	if err := store.SaveExternalSendApplyRecord(ctx, domainrevenue.ExternalSendApplyRecord{ApplyID: "apply_1", DraftID: "draft_1", DecisionID: "dec_1", Channel: "email", ApprovalStatus: "pending", HumanApproved: true, ApplyStatus: "blocked", SendResult: "not_sent", FailureReason: "no adapter"}); err == nil {
		t.Fatal("expected unapproved external send apply to fail")
	}
}

func TestJSONLStoreListHumanDecisionGateRecordsReturnsLatestStatePerDecision(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 19, 8, 47, 0, 0, time.UTC)
	pending := domainrevenue.HumanDecisionGateRecord{
		DecisionID:       "dec_1",
		DecisionType:     "closed_channel_send",
		ApprovalStatus:   "pending",
		GateStatus:       "needs_review",
		RequiresApproval: true,
		CreatedAt:        now,
	}
	approved := pending
	approved.ApprovalStatus = "approved"
	approved.GateStatus = "approved"
	approved.Reasons = nil
	if err := store.SaveHumanDecisionGateRecord(ctx, pending); err != nil {
		t.Fatalf("SaveHumanDecisionGateRecord(pending) failed: %v", err)
	}
	if err := store.SaveHumanDecisionGateRecord(ctx, approved); err != nil {
		t.Fatalf("SaveHumanDecisionGateRecord(approved) failed: %v", err)
	}

	decisions, err := store.ListHumanDecisionGateRecords(ctx, 10)
	if err != nil {
		t.Fatalf("ListHumanDecisionGateRecords failed: %v", err)
	}
	if len(decisions) != 1 || decisions[0].DecisionID != "dec_1" || decisions[0].ApprovalStatus != "approved" || decisions[0].GateStatus != "approved" {
		t.Fatalf("decisions=%#v", decisions)
	}
}
