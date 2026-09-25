package revenue

import (
	"context"
	"fmt"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
)

func TestSQLiteStoreConfiguresSerializedBusyTimeout(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "revenue.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
	var busyTimeout int
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("busy_timeout query failed: %v", err)
	}
	if busyTimeout != sqliteBusyTimeoutMilliseconds {
		t.Fatalf("busy_timeout = %d, want %d", busyTimeout, sqliteBusyTimeoutMilliseconds)
	}
}

func TestSQLiteStoreConcurrentMarketResearchWrites(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "revenue.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.SaveMarketResearchItem(context.Background(), domainrevenue.MarketResearchItem{
				ItemID:         fmt.Sprintf("concurrent-market-%d", i),
				SourcePlatform: "note",
				Theme:          "concurrent owner write",
				CreatedAt:      time.Now().UTC(),
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent SaveMarketResearchItem failed: %v", err)
		}
	}
	items, err := store.ListMarketResearchItems(context.Background(), workers)
	if err != nil || len(items) != workers {
		t.Fatalf("concurrent market research count = %d, err=%v; want %d", len(items), err, workers)
	}
}

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
	// fixtureReport/fixtureDraft carry the digest of their own body, computed
	// independently of the store, so the roundtrip cannot pass by recomputing it.
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
	opportunities, err := store.ListOpportunities(ctx, 10)
	assertOne("opportunities", err, len(opportunities))
	if opportunities[0].ExpectedProfit != 2200 {
		t.Fatalf("opportunity expected_profit = %d, want 2200", opportunities[0].ExpectedProfit)
	}
	tasks, err := store.ListEconomicTasks(ctx, 10)
	assertOne("economic tasks", err, len(tasks))
	reflections, err := store.ListEconomicReflections(ctx, 10)
	assertOne("economic reflections", err, len(reflections))
	decisions, err := store.ListPolicyDecisionRecords(ctx, 10)
	assertOne("policy decisions", err, len(decisions))
	daily, err := store.ListDailyRoutineReports(ctx, 10)
	assertOne("daily routine reports", err, len(daily))
	// The saved report has to come back field for field: every body value, the digest
	// above and the successor reference, plus identity and lifecycle state, so a roundtrip
	// cannot pass by dropping a field or refilling a digest on read.
	if diff := reportRoundtripMismatch(daily[0], now); diff != "" {
		t.Fatalf("daily routine report roundtrip = %#v: %s", daily[0], diff)
	}
	drafts, err := store.ListChannelDrafts(ctx, 10)
	assertOne("channel drafts", err, len(drafts))
	if diff := draftRoundtripMismatch(drafts[0], now); diff != "" {
		t.Fatalf("channel draft roundtrip = %#v: %s", drafts[0], diff)
	}
	applies, err := store.ListExternalSendApplyRecords(ctx, 10)
	assertOne("external send applies", err, len(applies))
	deliveries, err := store.ListDeliveries(ctx, 10)
	assertOne("deliveries", err, len(deliveries))
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

func TestSQLiteStoreAllowsEconomicTask(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "revenue.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	err = store.SaveEconomicTask(context.Background(), domainrevenue.EconomicTask{
		TaskID:        "task_1",
		OpportunityID: "opp_1",
		AgentID:       "shiro",
		TaskKind:      "external_publish",
		Status:        "planned",
		CreatedAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("external publish task should persist: %v", err)
	}
}

func TestSQLiteStoreFindOpportunityByIDUsesExactPrimaryKey(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "revenue.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	item := domainrevenue.Opportunity{
		OpportunityID: "opp_exact", SourceKind: "note", Title: "exact", ExpectedRevenue: 100, ExpectedCost: 40, CreatedAt: time.Now().UTC(),
	}
	if err := store.SaveOpportunity(ctx, item); err != nil {
		t.Fatalf("SaveOpportunity() failed: %v", err)
	}
	got, found, err := store.FindOpportunityByID(ctx, item.OpportunityID)
	if err != nil || !found || got.OpportunityID != item.OpportunityID || got.ExpectedProfit != 60 {
		t.Fatalf("FindOpportunityByID() = %#v, found=%v, err=%v", got, found, err)
	}
	if _, found, err := store.FindOpportunityByID(ctx, "missing"); err != nil || found {
		t.Fatalf("missing FindOpportunityByID() found=%v err=%v", found, err)
	}
}

// TestSQLiteStoreRejectsInvalidArtifactContentHash checks that the SQLite store refuses to
// persist a revenue artifact whose content_hash is missing, malformed or does not match
// the digest of the stored body, and that a superseded_by naming the artifact itself is
// refused. Nothing rejected here may show up in the later lists.
func TestSQLiteStoreRejectsInvalidArtifactContentHash(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "revenue.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
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
