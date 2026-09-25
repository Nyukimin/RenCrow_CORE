package revenue

import (
	"context"
	"fmt"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	"slices"
	"strings"
	"testing"
	"time"

	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
)

// The revenue artifact fixtures below are written by both the JSONL and the SQLite store
// tests, and the digests are the values of those exact bodies, computed independently of
// the store and of the domain projection, so a roundtrip cannot pass by recomputing a
// digest that was lost on the way out.
const (
	fixtureReportContentHash  = "sha256:83293ec3d95115bb4c7da0f72654fd482e547a9fc2efd886d5ba4cb88fa38122"
	fixtureDraftContentHash   = "sha256:e974587f1014c872c2052f60d9e6e3516163bf8dda14460d5888f0252844fe40"
	fixtureReportSupersededBy = modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000b")
	// fixtureDraftSupersededBy is a canonical successor that is not the draft itself, so
	// a non-empty supersession edge is carried through both stores instead of only the
	// empty case. It is identity metadata, so the draft digest above does not cover it.
	fixtureDraftSupersededBy = modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000a")
)

func fixtureReport(now time.Time) domainrevenue.DailyRoutineReport {
	return domainrevenue.DailyRoutineReport{
		ArtifactID:       modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000001"),
		Kind:             modulecore.ArtifactKindReport,
		Date:             "2026-05-18",
		Summary:          "日次摘要",
		MarketResearch:   1,
		SNSPosts:         2,
		Products:         3,
		CustomerVoices:   4,
		RevenueEvents:    5,
		PaidCustomers:    1,
		BlockedDecisions: 0,
		SuggestedActions: []string{"市場調査を追加する"},
		Status:           "draft_report",
		ContentHash:      fixtureReportContentHash,
		SupersededBy:     fixtureReportSupersededBy,
		CreatedAt:        now,
	}
}

func fixtureDraft(now time.Time) domainrevenue.ChannelDraft {
	return domainrevenue.ChannelDraft{
		ArtifactID:   modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000002"),
		Kind:         modulecore.ArtifactKindDraft,
		Channel:      "email",
		Subject:      "購入者向け案内",
		Body:         "下書き本文",
		ContentHash:  fixtureDraftContentHash,
		SupersededBy: fixtureDraftSupersededBy,
		CreatedAt:    now,
	}
}

// reportRoundtripMismatch lists every field of a report that came back from a store and
// differs from the written one. The body values, the digest and the successor are compared
// next to identity and lifecycle state, so a store that dropped a body field, lost the
// digest or refilled it on read fails instead of passing.
func reportRoundtripMismatch(got domainrevenue.DailyRoutineReport, now time.Time) string {
	want := fixtureReport(now)
	var problems []string
	check := func(name string, gotValue, wantValue any) {
		if gotValue != wantValue {
			problems = append(problems, fmt.Sprintf("%s=%v want %v", name, gotValue, wantValue))
		}
	}
	check("artifact_id", got.ArtifactID, want.ArtifactID)
	check("artifact_kind", got.Kind, want.Kind)
	check("date", got.Date, want.Date)
	check("summary", got.Summary, want.Summary)
	check("market_research_count", got.MarketResearch, want.MarketResearch)
	check("sns_post_count", got.SNSPosts, want.SNSPosts)
	check("product_count", got.Products, want.Products)
	check("customer_voice_count", got.CustomerVoices, want.CustomerVoices)
	check("revenue_event_count", got.RevenueEvents, want.RevenueEvents)
	check("paid_customer_count", got.PaidCustomers, want.PaidCustomers)
	check("blocked_decision_count", got.BlockedDecisions, want.BlockedDecisions)
	// Element-wise so a stored list that merged or split elements on one separator is not
	// read back as the same content.
	if !slices.Equal(got.SuggestedActions, want.SuggestedActions) {
		problems = append(problems, fmt.Sprintf("suggested_actions=%v want %v", got.SuggestedActions, want.SuggestedActions))
	}
	check("status", got.Status, want.Status)
	check("external_send_applied", got.ExternalSendApplied, want.ExternalSendApplied)
	check("content_hash", got.ContentHash, want.ContentHash)
	check("superseded_by", got.SupersededBy, want.SupersededBy)
	if !got.CreatedAt.Equal(now) {
		problems = append(problems, fmt.Sprintf("created_at=%s want %s", got.CreatedAt, now))
	}
	return strings.Join(problems, "; ")
}

func draftRoundtripMismatch(got domainrevenue.ChannelDraft, now time.Time) string {
	want := fixtureDraft(now)
	var problems []string
	check := func(name string, gotValue, wantValue any) {
		if gotValue != wantValue {
			problems = append(problems, fmt.Sprintf("%s=%v want %v", name, gotValue, wantValue))
		}
	}
	check("artifact_id", got.ArtifactID, want.ArtifactID)
	check("artifact_kind", got.Kind, want.Kind)
	check("channel", got.Channel, want.Channel)
	check("subject", got.Subject, want.Subject)
	check("body", got.Body, want.Body)
	check("external_send_applied", got.ExternalSendApplied, want.ExternalSendApplied)
	check("content_hash", got.ContentHash, want.ContentHash)
	check("superseded_by", got.SupersededBy, want.SupersededBy)
	if !got.CreatedAt.Equal(now) {
		problems = append(problems, fmt.Sprintf("created_at=%s want %s", got.CreatedAt, now))
	}
	return strings.Join(problems, "; ")
}

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
	if err := store.SaveOpportunity(ctx, domainrevenue.Opportunity{
		OpportunityID:   "opp_1",
		SourceKind:      "note_archive",
		Title:           "ローカルLLM技術資料",
		ExpectedRevenue: 3000,
		ExpectedCost:    800,
		RiskScore:       0.2,
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("SaveOpportunity failed: %v", err)
	}
	if err := store.SaveEconomicTask(ctx, domainrevenue.EconomicTask{
		TaskID:        "task_1",
		OpportunityID: "opp_1",
		AgentID:       "shiro",
		TaskKind:      "draft_report",
		Status:        "draft",
		ExpectedValue: 0.7,
		Risk:          0.1,
		Cost:          0.2,
		CreatedAt:     now,
	}); err != nil {
		t.Fatalf("SaveEconomicTask failed: %v", err)
	}
	if err := store.SaveEconomicReflection(ctx, domainrevenue.EconomicReflection{
		ReflectionID:  "reflection_1",
		OpportunityID: "opp_1",
		Outcome:       "produced",
		NetProfit:     2200,
		Lessons:       []string{"再利用価値が高い"},
		CreatedAt:     now,
	}); err != nil {
		t.Fatalf("SaveEconomicReflection failed: %v", err)
	}
	if err := store.SavePolicyDecisionRecord(ctx, domainrevenue.PolicyDecisionRecord{
		DecisionID:   "dec_1",
		DecisionType: "high_ticket_offer",
		Status:       "blocked",
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SavePolicyDecisionRecord failed: %v", err)
	}
	if err := store.SaveDailyRoutineReport(ctx, fixtureReport(now)); err != nil {
		t.Fatalf("SaveDailyRoutineReport failed: %v", err)
	}
	if err := store.SaveChannelDraft(ctx, fixtureDraft(now)); err != nil {
		t.Fatalf("SaveChannelDraft failed: %v", err)
	}
	if err := store.SaveExternalSendApplyRecord(ctx, domainrevenue.ExternalSendApplyRecord{
		ActionID:            modulecore.ActionID("act_00000000-0000-5000-8000-000000000001"),
		ArtifactID:          modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000002"),
		DecisionID:          "dec_1",
		Channel:             "email",
		ApplyStatus:         "blocked",
		SendResult:          "not_sent",
		FailureReason:       "external channel adapter is not configured",
		ExternalSendApplied: false,
		CreatedAt:           now,
	}); err != nil {
		t.Fatalf("SaveExternalSendApplyRecord failed: %v", err)
	}
	if err := store.SaveDelivery(ctx, domainrevenue.Delivery{
		DeliveryID: "delivery_1", TraceID: "trc_1", OpportunityID: "opp_1",
		DeliveryKind: "handoff", Status: "completed", Target: "internal-review", CreatedAt: now,
	}); err != nil {
		t.Fatalf("SaveDelivery failed: %v", err)
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
	opportunities, err := store.ListOpportunities(ctx, 10)
	if err != nil || len(opportunities) != 1 || opportunities[0].OpportunityID != "opp_1" || opportunities[0].ExpectedProfit != 2200 {
		t.Fatalf("opportunities=%#v err=%v", opportunities, err)
	}
	tasks, err := store.ListEconomicTasks(ctx, 10)
	if err != nil || len(tasks) != 1 || tasks[0].TaskID != "task_1" {
		t.Fatalf("tasks=%#v err=%v", tasks, err)
	}
	reflections, err := store.ListEconomicReflections(ctx, 10)
	if err != nil || len(reflections) != 1 || reflections[0].ReflectionID != "reflection_1" {
		t.Fatalf("reflections=%#v err=%v", reflections, err)
	}
	decisions, err := store.ListPolicyDecisionRecords(ctx, 10)
	if err != nil || len(decisions) != 1 || decisions[0].DecisionID != "dec_1" {
		t.Fatalf("decisions=%#v err=%v", decisions, err)
	}
	daily, err := store.ListDailyRoutineReports(ctx, 10)
	if err != nil || len(daily) != 1 || daily[0].ArtifactID != modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000001") {
		t.Fatalf("daily=%#v err=%v", daily, err)
	}
	if diff := reportRoundtripMismatch(daily[0], now); diff != "" {
		t.Fatalf("daily routine report roundtrip = %#v: %s", daily[0], diff)
	}
	drafts, err := store.ListChannelDrafts(ctx, 10)
	if err != nil || len(drafts) != 1 || drafts[0].ArtifactID != modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000002") {
		t.Fatalf("drafts=%#v err=%v", drafts, err)
	}
	if diff := draftRoundtripMismatch(drafts[0], now); diff != "" {
		t.Fatalf("channel draft roundtrip = %#v: %s", drafts[0], diff)
	}
	applies, err := store.ListExternalSendApplyRecords(ctx, 10)
	if err != nil || len(applies) != 1 || applies[0].ActionID != modulecore.ActionID("act_00000000-0000-5000-8000-000000000001") {
		t.Fatalf("applies=%#v err=%v", applies, err)
	}
	deliveries, err := store.ListDeliveries(ctx, 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].DeliveryID != "delivery_1" {
		t.Fatalf("deliveries=%#v err=%v", deliveries, err)
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
	if err := store.SavePolicyDecisionRecord(ctx, domainrevenue.PolicyDecisionRecord{DecisionID: "dec_1", DecisionType: "external_publish", Status: "adopted"}); err == nil {
		t.Fatal("expected invalid decision status to fail")
	}
	if err := store.SaveDailyRoutineReport(ctx, domainrevenue.DailyRoutineReport{ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000001"), Kind: modulecore.ArtifactKindReport, Date: "2026-05-18", Status: "sent", ExternalSendApplied: true}); err == nil {
		t.Fatal("expected non-draft daily routine report to fail")
	}
	if err := store.SaveChannelDraft(ctx, domainrevenue.ChannelDraft{ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000002"), Kind: modulecore.ArtifactKindDraft, Channel: "email", Body: "送信済み", ExternalSendApplied: true}); err == nil {
		t.Fatal("expected externally applied channel draft to fail")
	}
	if err := store.SaveExternalSendApplyRecord(ctx, domainrevenue.ExternalSendApplyRecord{ActionID: modulecore.ActionID("act_00000000-0000-5000-8000-000000000001"), ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000002"), DecisionID: "dec_1", Channel: "email", ApplyStatus: "blocked", SendResult: "not_sent", FailureReason: "no adapter", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("external send audit failed: %v", err)
	}
	if err := store.SaveOpportunity(ctx, domainrevenue.Opportunity{OpportunityID: "opp_1", SourceKind: "note", Title: "必ず稼げる資料", CreatedAt: time.Now()}); err == nil {
		t.Fatal("expected prohibited opportunity claim to fail")
	}
	if err := store.SaveEconomicTask(ctx, domainrevenue.EconomicTask{TaskID: "task_1", OpportunityID: "opp_1", AgentID: "shiro", TaskKind: "billing", Status: "planned", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("billing task failed: %v", err)
	}
}

func TestJSONLStoreFindOpportunityByIDReturnsLatestExactRecord(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	first := domainrevenue.Opportunity{
		OpportunityID: "opp_exact", SourceKind: "note", Title: "first", ExpectedRevenue: 100, ExpectedCost: 40, CreatedAt: now,
	}
	second := first
	second.Title = "latest"
	second.CreatedAt = now.Add(time.Second)
	if err := store.SaveOpportunity(ctx, first); err != nil {
		t.Fatalf("SaveOpportunity(first) failed: %v", err)
	}
	if err := store.SaveOpportunity(ctx, second); err != nil {
		t.Fatalf("SaveOpportunity(second) failed: %v", err)
	}
	got, found, err := store.FindOpportunityByID(ctx, "opp_exact")
	if err != nil || !found || got.Title != "latest" || got.ExpectedProfit != 60 {
		t.Fatalf("FindOpportunityByID() = %#v, found=%v, err=%v", got, found, err)
	}
	if _, found, err := store.FindOpportunityByID(ctx, "missing"); err != nil || found {
		t.Fatalf("missing FindOpportunityByID() found=%v err=%v", found, err)
	}
}

func TestJSONLStoreListPolicyDecisionRecordsReturnsLatestStatePerDecision(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 19, 8, 47, 0, 0, time.UTC)
	blocked := domainrevenue.PolicyDecisionRecord{
		DecisionID:   "dec_1",
		DecisionType: "closed_channel_send",
		Status:       "blocked",
		Reasons:      []string{"target is outside policy scope"},
		CreatedAt:    now,
	}
	allowed := blocked
	allowed.Status = "allowed"
	allowed.Reasons = nil
	if err := store.SavePolicyDecisionRecord(ctx, blocked); err != nil {
		t.Fatalf("SavePolicyDecisionRecord(blocked) failed: %v", err)
	}
	if err := store.SavePolicyDecisionRecord(ctx, allowed); err != nil {
		t.Fatalf("SavePolicyDecisionRecord(allowed) failed: %v", err)
	}

	decisions, err := store.ListPolicyDecisionRecords(ctx, 10)
	if err != nil {
		t.Fatalf("ListPolicyDecisionRecords failed: %v", err)
	}
	if len(decisions) != 1 || decisions[0].DecisionID != "dec_1" || decisions[0].Status != "allowed" {
		t.Fatalf("decisions=%#v", decisions)
	}
}

// TestJSONLStoreRejectsInvalidArtifactContentHash checks that the JSONL store refuses to
// persist a revenue artifact whose content_hash is missing, malformed or does not match
// the digest of the stored body, and that a superseded_by naming the artifact itself is
// refused. Nothing rejected here may show up in the later lists.
func TestJSONLStoreRejectsInvalidArtifactContentHash(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	missingHash := fixtureReport(now)
	missingHash.ContentHash = ""
	if err := store.SaveDailyRoutineReport(ctx, missingHash); err == nil || !strings.Contains(err.Error(), "content_hash") {
		t.Fatalf("report without content_hash: err=%v, want content_hash rejection", err)
	}

	foreignDigest := fixtureReport(now)
	foreignDigest.ContentHash = "sha256:cd3668ffb26f36cd58f99b1253c07f7eb594d43b7f23e58b720889729499db2d"
	if err := store.SaveDailyRoutineReport(ctx, foreignDigest); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("report carrying another body's digest: err=%v, want digest mismatch rejection", err)
	}

	selfSuperseded := fixtureReport(now)
	selfSuperseded.SupersededBy = selfSuperseded.ArtifactID
	if err := store.SaveDailyRoutineReport(ctx, selfSuperseded); err == nil || !strings.Contains(err.Error(), "superseded_by") {
		t.Fatalf("report superseding itself: err=%v, want superseded_by rejection", err)
	}

	if reports, err := store.ListDailyRoutineReports(ctx, 10); err != nil || len(reports) != 0 {
		t.Fatalf("reports after rejected writes = %#v err=%v, want none stored", reports, err)
	}

	malformedHash := fixtureDraft(now)
	malformedHash.ContentHash = "sha256:not-a-digest"
	if err := store.SaveChannelDraft(ctx, malformedHash); err == nil || !strings.Contains(err.Error(), "content_hash") {
		t.Fatalf("draft with malformed content_hash: err=%v, want content_hash rejection", err)
	}

	editedBody := fixtureDraft(now)
	editedBody.Body = "書き換えた後の本文"
	if err := store.SaveChannelDraft(ctx, editedBody); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("draft with edited body: err=%v, want digest mismatch rejection", err)
	}

	if drafts, err := store.ListChannelDrafts(ctx, 10); err != nil || len(drafts) != 0 {
		t.Fatalf("drafts after rejected writes = %#v err=%v, want none stored", drafts, err)
	}
}
