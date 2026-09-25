package revenue

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestValidateProductRejectsSuccessGuarantee(t *testing.T) {
	err := ValidateProduct(Product{
		ProductID:   "prod_1",
		ProductName: "AI副業テンプレ",
		Promise:     "誰でも必ず稼げる",
		Status:      "draft",
	})
	if err == nil {
		t.Fatal("expected prohibited revenue claim to fail")
	}
}

func TestValidateCustomerVoiceRequiresPermissionForMarketing(t *testing.T) {
	err := ValidateCustomerVoice(CustomerVoice{
		VoiceID:            "voice_1",
		RawText:            "ここがわからない",
		UsableForMarketing: true,
		PermissionStatus:   "unknown",
	})
	if err == nil {
		t.Fatal("expected missing permission to fail")
	}
}

func TestValidateRevenueRecords(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 30, 0, 0, time.UTC)
	if err := ValidateMarketResearchItem(MarketResearchItem{ItemID: "mkt_1", SourcePlatform: "note", CreatedAt: now}); err != nil {
		t.Fatalf("market research should be valid: %v", err)
	}
	if err := ValidateSNSPostMetric(SNSPostMetric{PostID: "post_1", Platform: "x", Impressions: 1, CreatedAt: now}); err != nil {
		t.Fatalf("sns metric should be valid: %v", err)
	}
	if err := ValidateRevenueEvent(RevenueEvent{EventID: "rev_1", EventType: "purchase", Amount: 980, CreatedAt: now}); err != nil {
		t.Fatalf("revenue event should be valid: %v", err)
	}
	report := DailyRoutineReport{ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000001"), Kind: modulecore.ArtifactKindReport, Date: "2026-05-18", Status: "draft_report", CreatedAt: now}
	if got := string(DailyRoutineReportBodyBytes(report)); got != revenueReportMinimalBody {
		t.Fatalf("report digested body bytes = %s, want %s", got, revenueReportMinimalBody)
	}
	report.ContentHash = revenueTestContentHash(t, revenueReportMinimalBody)
	if err := ValidateDailyRoutineReport(report); err != nil {
		t.Fatalf("daily routine report should be valid: %v", err)
	}
}

func TestValidateRevenueRecordRequiredFieldsAndNumericBounds(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 30, 0, 0, time.UTC)
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "market research missing item id", err: ValidateMarketResearchItem(MarketResearchItem{SourcePlatform: "note", CreatedAt: now}), want: "item_id"},
		{name: "market research missing platform", err: ValidateMarketResearchItem(MarketResearchItem{ItemID: "mkt_1", CreatedAt: now}), want: "source_platform"},
		{name: "market research negative price", err: ValidateMarketResearchItem(MarketResearchItem{ItemID: "mkt_1", SourcePlatform: "note", Price: -1, CreatedAt: now}), want: "price"},
		{name: "sns missing post id", err: ValidateSNSPostMetric(SNSPostMetric{Platform: "x", CreatedAt: now}), want: "post_id"},
		{name: "sns missing platform", err: ValidateSNSPostMetric(SNSPostMetric{PostID: "post_1", CreatedAt: now}), want: "platform"},
		{name: "sns negative metric", err: ValidateSNSPostMetric(SNSPostMetric{PostID: "post_1", Platform: "x", Likes: -1, CreatedAt: now}), want: "metrics"},
		{name: "product missing id", err: ValidateProduct(Product{ProductName: "商品設計シート", Status: "draft", CreatedAt: now}), want: "product_id"},
		{name: "product missing name", err: ValidateProduct(Product{ProductID: "prod_1", Status: "draft", CreatedAt: now}), want: "product_name"},
		{name: "product missing status", err: ValidateProduct(Product{ProductID: "prod_1", ProductName: "商品設計シート", CreatedAt: now}), want: "status"},
		{name: "product negative price", err: ValidateProduct(Product{ProductID: "prod_1", ProductName: "商品設計シート", Status: "draft", Price: -1, CreatedAt: now}), want: "price"},
		{name: "customer voice missing id", err: ValidateCustomerVoice(CustomerVoice{RawText: "よかった", PermissionStatus: "unknown", CreatedAt: now}), want: "voice_id"},
		{name: "customer voice missing text", err: ValidateCustomerVoice(CustomerVoice{VoiceID: "voice_1", PermissionStatus: "unknown", CreatedAt: now}), want: "raw_text"},
		{name: "customer voice missing permission", err: ValidateCustomerVoice(CustomerVoice{VoiceID: "voice_1", RawText: "よかった", CreatedAt: now}), want: "permission_status"},
		{name: "revenue event missing id", err: ValidateRevenueEvent(RevenueEvent{EventType: "purchase", Amount: 980, CreatedAt: now}), want: "event_id"},
		{name: "revenue event missing type", err: ValidateRevenueEvent(RevenueEvent{EventID: "rev_1", Amount: 980, CreatedAt: now}), want: "event_type"},
		{name: "revenue event negative amount", err: ValidateRevenueEvent(RevenueEvent{EventID: "rev_1", EventType: "purchase", Amount: -1, CreatedAt: now}), want: "amount"},
		{name: "daily report missing id", err: ValidateDailyRoutineReport(DailyRoutineReport{Kind: modulecore.ArtifactKindReport, Date: "2026-05-20", Status: "draft_report", CreatedAt: now}), want: "artifact_id"},
		{name: "daily report missing date", err: ValidateDailyRoutineReport(DailyRoutineReport{ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000001"), Kind: modulecore.ArtifactKindReport, Status: "draft_report", CreatedAt: now}), want: "date"},
		{name: "daily report invalid status", err: ValidateDailyRoutineReport(DailyRoutineReport{ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000001"), Kind: modulecore.ArtifactKindReport, Date: "2026-05-20", Status: "sent", CreatedAt: now}), want: "status"},
		{name: "daily report negative count", err: ValidateDailyRoutineReport(DailyRoutineReport{ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000001"), Kind: modulecore.ArtifactKindReport, Date: "2026-05-20", Status: "draft_report", MarketResearch: -1, CreatedAt: now}), want: "counts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil || !strings.Contains(tc.err.Error(), tc.want) {
				t.Fatalf("err=%v, want containing %q", tc.err, tc.want)
			}
		})
	}
}

func TestValidateRevenueRejectsMissingCreatedAt(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 30, 0, 0, time.UTC)
	cases := []struct {
		name string
		err  error
	}{
		{name: "market research", err: ValidateMarketResearchItem(MarketResearchItem{ItemID: "mkt_1", SourcePlatform: "note"})},
		{name: "sns post metric", err: ValidateSNSPostMetric(SNSPostMetric{PostID: "post_1", Platform: "x"})},
		{name: "product", err: ValidateProduct(Product{ProductID: "prod_1", ProductName: "商品設計シート", Status: "draft"})},
		{name: "customer voice", err: ValidateCustomerVoice(CustomerVoice{VoiceID: "voice_1", RawText: "よかった", PermissionStatus: "unknown"})},
		{name: "revenue event", err: ValidateRevenueEvent(RevenueEvent{EventID: "rev_1", EventType: "purchase"})},
		{name: "daily routine", err: ValidateDailyRoutineReport(DailyRoutineReport{ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000001"), Kind: modulecore.ArtifactKindReport, Date: "2026-05-20", Status: "draft_report"})},
		{name: "channel draft", err: ValidateChannelDraft(ChannelDraft{ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000002"), Kind: modulecore.ArtifactKindDraft, Channel: "email", Body: "下書き本文"})},
		{name: "external send apply", err: ValidateExternalSendApplyRecord(ExternalSendApplyRecord{ActionID: modulecore.ActionID("act_00000000-0000-5000-8000-000000000001"), ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000002"), DecisionID: "dec_1", Channel: "email", ApplyStatus: "blocked", SendResult: "not_sent", FailureReason: "external channel adapter is not configured"})},
		{name: "policy decision", err: ValidatePolicyDecisionRecord(PolicyDecisionRecord{DecisionID: "dec_1", DecisionType: "external_publish", Status: "blocked"})},
		{name: "product updated_at optional", err: ValidateProduct(Product{ProductID: "prod_1", ProductName: "商品設計シート", Status: "draft", CreatedAt: now})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "product updated_at optional" {
				if tc.err != nil {
					t.Fatalf("expected valid product without updated_at: %v", tc.err)
				}
				return
			}
			if tc.err == nil || !strings.Contains(tc.err.Error(), "created_at") {
				t.Fatalf("err=%v, want created_at", tc.err)
			}
		})
	}
}

func TestValidateChannelDraft(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 30, 0, 0, time.UTC)
	draft := ChannelDraft{ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000002"), Kind: modulecore.ArtifactKindDraft, Channel: "email", Body: "下書き本文", CreatedAt: now}
	if got := string(ChannelDraftBodyBytes(draft)); got != revenueDraftMinimalBody {
		t.Fatalf("draft digested body bytes = %s, want %s", got, revenueDraftMinimalBody)
	}
	draft.ContentHash = revenueTestContentHash(t, revenueDraftMinimalBody)
	if err := ValidateChannelDraft(draft); err != nil {
		t.Fatalf("empty policy metadata should be accepted: %v", err)
	}

	cases := []struct {
		name string
		item ChannelDraft
		want string
	}{
		{name: "missing id", item: ChannelDraft{Channel: "email", Body: "下書き本文", CreatedAt: now}, want: "artifact_id"},
		{name: "missing channel", item: ChannelDraft{ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000002"), Kind: modulecore.ArtifactKindDraft, Body: "下書き本文", CreatedAt: now}, want: "channel"},
		{name: "missing body", item: ChannelDraft{ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000002"), Kind: modulecore.ArtifactKindDraft, Channel: "email", CreatedAt: now}, want: "body"},
		{name: "external send applied", item: ChannelDraft{ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000002"), Kind: modulecore.ArtifactKindDraft, Channel: "email", Body: "下書き本文", ExternalSendApplied: true, CreatedAt: now}, want: "external send"},
		{name: "prohibited claim", item: ChannelDraft{ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000002"), Kind: modulecore.ArtifactKindDraft, Channel: "email", Subject: "案内", Body: "誰でも必ず稼げる", CreatedAt: now}, want: "prohibited revenue claim"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateChannelDraft(tc.item)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestEvaluatePolicyDecisionAllowsHighTicketOffer(t *testing.T) {
	result := EvaluatePolicyDecision(PolicyDecisionRequest{
		DecisionType: "high_ticket_offer",
		Description:  "30万円の導入支援を案内する",
	})

	if result.Status != "allowed" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestEvaluatePolicyDecisionAllowsSafePublication(t *testing.T) {
	result := EvaluatePolicyDecision(PolicyDecisionRequest{
		DecisionType: "customer_voice_publication",
		Description:  "購入者の声を販売ページへ掲載する",
	})

	if result.Status != "allowed" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestEvaluatePolicyDecisionAllowsSafePriceDecision(t *testing.T) {
	result := EvaluatePolicyDecision(PolicyDecisionRequest{
		DecisionType: "product_price",
		Description:  "低単価商品の価格を980円にする",
	})

	if result.Status != "allowed" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestEvaluatePolicyDecisionBlocksProhibitedClaim(t *testing.T) {
	result := EvaluatePolicyDecision(PolicyDecisionRequest{
		DecisionType: "external_publish",
		Description:  "誰でも必ず稼げると投稿する",
	})

	if result.Status != "blocked" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestEvaluatePolicyDecisionBlocksMissingTypeAndAllowsSafeInternalDecision(t *testing.T) {
	missing := EvaluatePolicyDecision(PolicyDecisionRequest{Description: "通常の内部メモ"})
	if missing.Status != "blocked" || len(missing.Reasons) == 0 {
		t.Fatalf("unexpected missing type result: %#v", missing)
	}

	safe := EvaluatePolicyDecision(PolicyDecisionRequest{
		DecisionType: "internal_note",
		Description:  "次回の商品改善メモを作る",
	})
	if safe.Status != "allowed" {
		t.Fatalf("unexpected safe result: %#v", safe)
	}
}

func TestBuildPolicyDecisionRecordReturnsAllowed(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 30, 0, 0, time.UTC)
	record := BuildPolicyDecisionRecord(PolicyDecisionRequest{
		DecisionID:   "dec_1",
		DecisionType: "high_ticket_offer",
		Description:  "30万円の導入支援を案内する",
		CreatedAt:    now,
	})

	if record.Status != "allowed" {
		t.Fatalf("unexpected record: %#v", record)
	}
	if err := ValidatePolicyDecisionRecord(record); err != nil {
		t.Fatalf("record should be valid: %v", err)
	}
}

func TestValidatePolicyDecisionRecordRejectsInvalidStatus(t *testing.T) {
	err := ValidatePolicyDecisionRecord(PolicyDecisionRecord{
		DecisionID:   "dec_1",
		DecisionType: "external_publish",
		Status:       "adopted",
	})
	if err == nil {
		t.Fatal("expected invalid status to fail")
	}
}

func TestValidatePolicyDecisionRecordRequiredFields(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 30, 0, 0, time.UTC)
	cases := []struct {
		name string
		item PolicyDecisionRecord
		want string
	}{
		{name: "missing id", item: PolicyDecisionRecord{DecisionType: "external_publish", Status: "blocked", CreatedAt: now}, want: "decision_id"},
		{name: "missing type", item: PolicyDecisionRecord{DecisionID: "dec_1", Status: "blocked", CreatedAt: now}, want: "decision_type"},
		{name: "missing status", item: PolicyDecisionRecord{DecisionID: "dec_1", DecisionType: "external_publish", CreatedAt: now}, want: "status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePolicyDecisionRecord(tc.item)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateExternalSendApplyRecordUsesPolicyDecision(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 30, 0, 0, time.UTC)
	record := ExternalSendApplyRecord{
		ActionID:            modulecore.ActionID("act_00000000-0000-5000-8000-000000000001"),
		ArtifactID:          modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000002"),
		DecisionID:          "dec_1",
		Channel:             "email",
		ApplyStatus:         "blocked",
		SendResult:          "not_sent",
		FailureReason:       "external channel adapter is not configured",
		ExternalSendApplied: false,
		CreatedAt:           now,
	}
	if err := ValidateExternalSendApplyRecord(record); err != nil {
		t.Fatalf("record should be valid: %v", err)
	}

	record.ExternalSendApplied = true
	if err := ValidateExternalSendApplyRecord(record); err == nil {
		t.Fatal("expected externally applied non-sent record to fail")
	}
}

func TestValidateDeliveryRequiresStableTraceAndProtectsExternalCompletion(t *testing.T) {
	now := time.Now().UTC()
	valid := Delivery{
		DeliveryID:     "delivery_1",
		TraceID:        "trc_1",
		DeliveryKind:   "billing",
		Status:         "pending",
		Target:         "customer_1",
		ExternalAction: true,
		CreatedAt:      now,
	}
	if err := ValidateDelivery(valid); err != nil {
		t.Fatalf("valid pending delivery rejected: %v", err)
	}
	withoutTrace := valid
	withoutTrace.TraceID = ""
	if err := ValidateDelivery(withoutTrace); err == nil {
		t.Fatal("delivery without trace_id must be rejected")
	}
	completed := valid
	completed.Status = "completed"
	if err := ValidateDelivery(completed); err == nil {
		t.Fatal("completed external delivery without policy decision/evidence must be rejected")
	}
	completed.PolicyDecisionID = "policy_1"
	completed.Evidence = "receipt_1"
	if err := ValidateDelivery(completed); err != nil {
		t.Fatalf("policy-allowed and evidenced delivery rejected: %v", err)
	}
}

func TestValidateExternalSendApplyRecordRequiredFieldsAndStatuses(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 30, 0, 0, time.UTC)
	validBlocked := ExternalSendApplyRecord{
		ActionID:      modulecore.ActionID("act_00000000-0000-5000-8000-000000000001"),
		ArtifactID:    modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000002"),
		DecisionID:    "dec_1",
		Channel:       "email",
		ApplyStatus:   "blocked",
		SendResult:    "not_sent",
		FailureReason: "external channel adapter is not configured",
		CreatedAt:     now,
	}
	cases := []struct {
		name   string
		mutate func(*ExternalSendApplyRecord)
		want   string
	}{
		{name: "missing apply id", mutate: func(item *ExternalSendApplyRecord) { item.ActionID = "" }, want: "action_id"},
		{name: "missing draft id", mutate: func(item *ExternalSendApplyRecord) { item.ArtifactID = "" }, want: "artifact_id"},
		{name: "missing decision id", mutate: func(item *ExternalSendApplyRecord) { item.DecisionID = "" }, want: "decision_id"},
		{name: "missing channel", mutate: func(item *ExternalSendApplyRecord) { item.Channel = "" }, want: "channel"},
		{name: "invalid apply status", mutate: func(item *ExternalSendApplyRecord) { item.ApplyStatus = "queued" }, want: "apply_status"},
		{name: "missing send result", mutate: func(item *ExternalSendApplyRecord) { item.SendResult = "" }, want: "send_result"},
		{name: "missing failure reason", mutate: func(item *ExternalSendApplyRecord) { item.FailureReason = "" }, want: "failure_reason"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := validBlocked
			tc.mutate(&item)
			err := ValidateExternalSendApplyRecord(item)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateExternalSendApplyRecordRequiresSentStateForSuccessfulSend(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 30, 0, 0, time.UTC)
	record := ExternalSendApplyRecord{
		ActionID:            modulecore.ActionID("act_00000000-0000-5000-8000-000000000001"),
		ArtifactID:          modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000002"),
		DecisionID:          "dec_1",
		Channel:             "email",
		ApplyStatus:         "sent",
		SendResult:          "sent",
		ExternalSendApplied: false,
		PostSendVerified:    true,
		PostSendEvidence:    "delivery id msg_1 observed",
		CreatedAt:           now,
	}
	if err := ValidateExternalSendApplyRecord(record); err == nil {
		t.Fatal("expected sent status without external_send_applied to fail")
	}
	record.ExternalSendApplied = true
	record.PostSendVerified = false
	if err := ValidateExternalSendApplyRecord(record); err == nil {
		t.Fatal("expected sent status without post_send_verified to fail")
	}
	record.PostSendVerified = true
	record.PostSendEvidence = ""
	if err := ValidateExternalSendApplyRecord(record); err == nil {
		t.Fatal("expected sent status without post_send_evidence to fail")
	}
	record.PostSendEvidence = "delivery id msg_1 observed and status=delivered"
	if err := ValidateExternalSendApplyRecord(record); err != nil {
		t.Fatalf("record should be valid: %v", err)
	}
}

func TestValidateExternalSendApplyRecordRejectsVerificationWithoutSentStatus(t *testing.T) {
	record := ExternalSendApplyRecord{
		ActionID:            modulecore.ActionID("act_00000000-0000-5000-8000-000000000001"),
		ArtifactID:          modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000002"),
		DecisionID:          "dec_1",
		Channel:             "email",
		ApplyStatus:         "blocked",
		SendResult:          "not_sent",
		FailureReason:       "external channel adapter is not configured",
		ExternalSendApplied: false,
		PostSendVerified:    true,
	}
	if err := ValidateExternalSendApplyRecord(record); err == nil {
		t.Fatal("expected post_send_verified without sent status to fail")
	}
}

func TestValidateExternalSendApplyRecordRejectsSentResultWithoutSentStatus(t *testing.T) {
	record := ExternalSendApplyRecord{
		ActionID:            modulecore.ActionID("act_00000000-0000-5000-8000-000000000001"),
		ArtifactID:          modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000002"),
		DecisionID:          "dec_1",
		Channel:             "email",
		ApplyStatus:         "blocked",
		SendResult:          "sent",
		FailureReason:       "external channel adapter is not configured",
		ExternalSendApplied: false,
	}
	if err := ValidateExternalSendApplyRecord(record); err == nil || !strings.Contains(err.Error(), "send_result=sent") {
		t.Fatalf("err=%v", err)
	}
}

func TestBuildDailyRoutineReportIsDraftOnly(t *testing.T) {
	report := BuildDailyRoutineReport(DailyRoutineInput{
		ArtifactID:     modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000001"),
		WorkstreamID:   "ws_revenue",
		Date:           "2026-05-18",
		Now:            time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
		MarketResearch: []MarketResearchItem{{ItemID: "mkt_1", SourcePlatform: "note"}},
		SNSPosts:       []SNSPostMetric{{PostID: "post_1", Platform: "x"}},
		Products:       []Product{{ProductID: "prod_1", ProductName: "商品設計シート", Status: "draft"}},
		CustomerVoices: []CustomerVoice{{VoiceID: "voice_1", RawText: "ここがわからない", PermissionStatus: "unknown"}},
		RevenueEvents: []RevenueEvent{
			{EventID: "rev_1", EventType: "purchase", Amount: 980, CustomerID: "cust_1"},
			{EventID: "rev_2", EventType: "purchase", Amount: 1980, CustomerID: "cust_1"},
		},
		Decisions: []PolicyDecisionRecord{{DecisionID: "dec_1", DecisionType: "external_publish", Status: "blocked"}},
	})

	if report.Status != "draft_report" || report.ExternalSendApplied {
		t.Fatalf("expected draft-only report: %#v", report)
	}
	if report.PaidCustomers != 1 || report.BlockedDecisions != 1 {
		t.Fatalf("unexpected counts: %#v", report)
	}
	if err := ValidateDailyRoutineReport(report); err != nil {
		t.Fatalf("report should be valid: %v", err)
	}
}

func TestBuildDailyRoutineReportDefaultsAndSuggestedActions(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 30, 0, 0, time.UTC)
	report := BuildDailyRoutineReport(DailyRoutineInput{Now: now})

	if err := report.ArtifactID.Validate(); err != nil || report.Kind != modulecore.ArtifactKindReport || report.Date != "2026-05-20" || !report.CreatedAt.Equal(now) {
		t.Fatalf("unexpected defaults: %#v", report)
	}
	wantActions := []string{
		"市場調査を追加する",
		"SNS投稿または反応指標を記録する",
		"低単価商品の候補を1つ作る",
		"購入者または見込み顧客の声を記録する",
	}
	for _, want := range wantActions {
		if !containsString(report.SuggestedActions, want) {
			t.Fatalf("actions=%v, missing %q", report.SuggestedActions, want)
		}
	}
	if err := ValidateDailyRoutineReport(report); err != nil {
		t.Fatalf("report should be valid: %v", err)
	}
}

func TestBuildDailyRoutineReportCountsAnonymousPurchasesAndDefaultAction(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 30, 0, 0, time.UTC)
	report := BuildDailyRoutineReport(DailyRoutineInput{
		ArtifactID:     modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000001"),
		Date:           "2026-05-20",
		Now:            now,
		MarketResearch: []MarketResearchItem{{ItemID: "mkt_1"}},
		SNSPosts:       []SNSPostMetric{{PostID: "post_1"}},
		Products:       []Product{{ProductID: "prod_1"}},
		CustomerVoices: []CustomerVoice{{VoiceID: "voice_1"}},
		RevenueEvents: []RevenueEvent{
			{EventID: "rev_1", EventType: "purchase", Amount: 980, CustomerID: "cust_1"},
			{EventID: "rev_2", EventType: "purchase", Amount: 1980, CustomerID: "cust_1"},
			{EventID: "rev_3", EventType: "purchase", Amount: 500},
			{EventID: "rev_4", EventType: "refund", Amount: 500, CustomerID: "cust_2"},
			{EventID: "rev_5", EventType: "purchase", Amount: 0, CustomerID: "cust_3"},
		},
	})

	if report.PaidCustomers != 2 {
		t.Fatalf("paid customers=%d, want 2", report.PaidCustomers)
	}
	if len(report.SuggestedActions) != 1 || report.SuggestedActions[0] != "反応が取れた投稿と顧客の声から次の商品改善案を作る" {
		t.Fatalf("unexpected actions: %v", report.SuggestedActions)
	}
}

func TestValidateDailyRoutineReportRejectsExternalSend(t *testing.T) {
	err := ValidateDailyRoutineReport(DailyRoutineReport{
		ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000001"), Kind: modulecore.ArtifactKindReport,
		Date:                "2026-05-18",
		Status:              "draft_report",
		ExternalSendApplied: true,
	})
	if err == nil {
		t.Fatal("expected external send report to fail")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// IU-18c3 fixtures. The DailyRoutineReport and ChannelDraft content digest covers the
// body projection declared once by the revenue domain (compact JSON in this fixed key
// order): report = date, summary, the seven counts, suggested_actions; draft = channel,
// subject, body. Identity (artifact_id, artifact_kind, workstream_id, trace_id,
// opportunity_id, source_artifact_id), lifecycle and policy state (status,
// external_send_applied), created_at, content_hash itself and superseded_by stay out of
// the body, so a remint or an appended supersession never changes the digest. The
// expectations below are computed with crypto/sha256 so they do not depend on the
// producer helper.
const (
	revenueReportFixtureBody = `{"date":"2026-05-18","summary":"2026-05-18 のRevenue日次ルーチン下書きです。","market_research_count":1,"sns_post_count":2,"product_count":3,"customer_voice_count":4,"revenue_event_count":5,"paid_customer_count":1,"blocked_decision_count":0,"suggested_actions":["市場調査を追加する"]}`
	revenueDraftFixtureBody  = `{"channel":"x","subject":"Re: 納品のご確認","body":"本日17時までに納品いたします。"}`
	// revenueReportMinimalBody and revenueDraftMinimalBody are the body bytes of the
	// smallest accepted report and draft, so a test that only needs a valid digest does
	// not have to restate every count.
	revenueReportMinimalBody = `{"date":"2026-05-18","summary":"","market_research_count":0,"sns_post_count":0,"product_count":0,"customer_voice_count":0,"revenue_event_count":0,"paid_customer_count":0,"blocked_decision_count":0,"suggested_actions":[]}`
	revenueDraftMinimalBody  = `{"channel":"email","subject":"","body":"下書き本文"}`
)

func revenueTestContentHash(t *testing.T, content string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	return modulecore.ContentHashPrefix + hex.EncodeToString(sum[:])
}

func revenueReportPayload(extra string) string {
	return `{"artifact_id":"art_00000000-0000-5000-8000-000000000001","artifact_kind":"report","workstream_id":"ws_revenue","date":"2026-05-18","summary":"2026-05-18 のRevenue日次ルーチン下書きです。","market_research_count":1,"sns_post_count":2,"product_count":3,"customer_voice_count":4,"revenue_event_count":5,"paid_customer_count":1,"blocked_decision_count":0,"suggested_actions":["市場調査を追加する"],"status":"draft_report","external_send_applied":false,` + extra + `"created_at":"2026-05-18T12:00:00Z"}`
}

func revenueDraftPayload(extra string) string {
	return `{"artifact_id":"art_00000000-0000-5000-8000-000000000002","artifact_kind":"draft","trace_id":"trc_1","opportunity_id":"opp_1","workstream_id":"ws_revenue","channel":"x","subject":"Re: 納品のご確認","body":"本日17時までに納品いたします。","source_artifact_id":"art_00000000-0000-5000-8000-000000000001","external_send_applied":false,` + extra + `"created_at":"2026-05-18T12:00:00Z"}`
}

func TestDailyRoutineReportLinePreservesContentHashAndSupersededBy(t *testing.T) {
	digest := revenueTestContentHash(t, revenueReportFixtureBody)
	payload := revenueReportPayload(`"content_hash":"` + digest + `","superseded_by":"art_00000000-0000-5000-8000-00000000000b",`)
	var item DailyRoutineReport
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		t.Fatalf("unmarshal report payload: %v", err)
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if !strings.Contains(string(encoded), `"content_hash":"`+digest+`"`) {
		t.Fatalf("persisted content_hash lost after roundtrip: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"superseded_by":"art_00000000-0000-5000-8000-00000000000b"`) {
		t.Fatalf("persisted superseded_by lost after roundtrip: %s", encoded)
	}
}

func TestValidateDailyRoutineReportRejectsLineWithoutContentHash(t *testing.T) {
	var item DailyRoutineReport
	if err := json.Unmarshal([]byte(revenueReportPayload("")), &item); err != nil {
		t.Fatalf("unmarshal report payload: %v", err)
	}
	if err := ValidateDailyRoutineReport(item); err == nil || !strings.Contains(err.Error(), "content_hash") {
		t.Fatalf("expected content_hash rejection for a report persisted without a digest, got %v", err)
	}
}

func TestValidateDailyRoutineReportRejectsContentHashOfOtherBody(t *testing.T) {
	other := `{"date":"2026-05-18","summary":"2026-05-18 のRevenue日次ルーチン下書きです。","market_research_count":2,"sns_post_count":2,"product_count":3,"customer_voice_count":4,"revenue_event_count":5,"paid_customer_count":1,"blocked_decision_count":0,"suggested_actions":["市場調査を追加する"]}`
	var item DailyRoutineReport
	if err := json.Unmarshal([]byte(revenueReportPayload(`"content_hash":"`+revenueTestContentHash(t, other)+`",`)), &item); err != nil {
		t.Fatalf("unmarshal report payload: %v", err)
	}
	if err := ValidateDailyRoutineReport(item); err == nil || !strings.Contains(err.Error(), "does not match content") {
		t.Fatalf("expected content digest mismatch rejection, got %v", err)
	}
}

func TestValidateDailyRoutineReportRejectsSelfSupersession(t *testing.T) {
	payload := revenueReportPayload(`"content_hash":"` + revenueTestContentHash(t, revenueReportFixtureBody) + `","superseded_by":"art_00000000-0000-5000-8000-000000000001",`)
	var item DailyRoutineReport
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		t.Fatalf("unmarshal report payload: %v", err)
	}
	if err := ValidateDailyRoutineReport(item); err == nil || !strings.Contains(err.Error(), "must not reference the artifact itself") {
		t.Fatalf("expected self-referencing superseded_by rejection, got %v", err)
	}
}

func TestValidateDailyRoutineReportRejectsOpaqueSupersededBy(t *testing.T) {
	payload := revenueReportPayload(`"content_hash":"` + revenueTestContentHash(t, revenueReportFixtureBody) + `","superseded_by":"art_1",`)
	var item DailyRoutineReport
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		t.Fatalf("unmarshal report payload: %v", err)
	}
	if err := ValidateDailyRoutineReport(item); err == nil || !strings.Contains(err.Error(), "superseded_by") {
		t.Fatalf("expected opaque superseded_by rejection, got %v", err)
	}
}

func TestChannelDraftLinePreservesContentHashAndSupersededBy(t *testing.T) {
	digest := revenueTestContentHash(t, revenueDraftFixtureBody)
	payload := revenueDraftPayload(`"content_hash":"` + digest + `","superseded_by":"art_00000000-0000-5000-8000-00000000000b",`)
	var item ChannelDraft
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		t.Fatalf("unmarshal draft payload: %v", err)
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal draft: %v", err)
	}
	if !strings.Contains(string(encoded), `"content_hash":"`+digest+`"`) {
		t.Fatalf("persisted content_hash lost after roundtrip: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"superseded_by":"art_00000000-0000-5000-8000-00000000000b"`) {
		t.Fatalf("persisted superseded_by lost after roundtrip: %s", encoded)
	}
}

func TestValidateChannelDraftRejectsLineWithoutContentHash(t *testing.T) {
	var item ChannelDraft
	if err := json.Unmarshal([]byte(revenueDraftPayload("")), &item); err != nil {
		t.Fatalf("unmarshal draft payload: %v", err)
	}
	if err := ValidateChannelDraft(item); err == nil || !strings.Contains(err.Error(), "content_hash") {
		t.Fatalf("expected content_hash rejection for a draft persisted without a digest, got %v", err)
	}
}

func TestValidateChannelDraftRejectsContentHashOfOtherBody(t *testing.T) {
	other := `{"channel":"x","subject":"Re: 納品のご確認","body":"本日18時までに納品いたします。"}`
	var item ChannelDraft
	if err := json.Unmarshal([]byte(revenueDraftPayload(`"content_hash":"`+revenueTestContentHash(t, other)+`",`)), &item); err != nil {
		t.Fatalf("unmarshal draft payload: %v", err)
	}
	if err := ValidateChannelDraft(item); err == nil || !strings.Contains(err.Error(), "does not match content") {
		t.Fatalf("expected content digest mismatch rejection, got %v", err)
	}
}

func TestValidateChannelDraftRejectsSelfSupersession(t *testing.T) {
	payload := revenueDraftPayload(`"content_hash":"` + revenueTestContentHash(t, revenueDraftFixtureBody) + `","superseded_by":"art_00000000-0000-5000-8000-000000000002",`)
	var item ChannelDraft
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		t.Fatalf("unmarshal draft payload: %v", err)
	}
	if err := ValidateChannelDraft(item); err == nil || !strings.Contains(err.Error(), "must not reference the artifact itself") {
		t.Fatalf("expected self-referencing superseded_by rejection, got %v", err)
	}
}

// testReportFixture and testDraftFixture build the accepted report and draft whose body
// bytes are revenueReportFixtureBody and revenueDraftFixtureBody, so a digest test can
// change one field without restating the whole artifact.
func testReportFixture() DailyRoutineReport {
	return DailyRoutineReport{
		ArtifactID:       modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000001"),
		Kind:             modulecore.ArtifactKindReport,
		WorkstreamID:     "ws_revenue",
		Date:             "2026-05-18",
		Summary:          "2026-05-18 のRevenue日次ルーチン下書きです。",
		MarketResearch:   1,
		SNSPosts:         2,
		Products:         3,
		CustomerVoices:   4,
		RevenueEvents:    5,
		PaidCustomers:    1,
		BlockedDecisions: 0,
		SuggestedActions: []string{"市場調査を追加する"},
		Status:           "draft_report",
		CreatedAt:        time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	}
}

func testDraftFixture() ChannelDraft {
	return ChannelDraft{
		ArtifactID:       modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000002"),
		Kind:             modulecore.ArtifactKindDraft,
		TraceID:          "trc_1",
		OpportunityID:    "opp_1",
		WorkstreamID:     "ws_revenue",
		Channel:          "x",
		Subject:          "Re: 納品のご確認",
		Body:             "本日17時までに納品いたします。",
		SourceArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000001"),
		CreatedAt:        time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	}
}

// TestDailyRoutineReportDigestCoversBodyNotIdentityMetadata pins the report body bytes,
// so identity metadata, lifecycle and policy state, created_at and the supersession
// reference cannot drift into the digest, while a content change always shows up.
func TestDailyRoutineReportDigestCoversBodyNotIdentityMetadata(t *testing.T) {
	item := testReportFixture()
	if got := string(DailyRoutineReportBodyBytes(item)); got != revenueReportFixtureBody {
		t.Fatalf("report body bytes = %s, want %s", got, revenueReportFixtureBody)
	}
	digest := ComputeDailyRoutineReportContentHash(item)
	if want := revenueTestContentHash(t, revenueReportFixtureBody); digest != want {
		t.Fatalf("report digest = %s, want %s", digest, want)
	}

	reminted := testReportFixture()
	reminted.ArtifactID = modulecore.ArtifactID("art_00000000-0000-7000-8000-00000000000d")
	reminted.WorkstreamID = "ws_other"
	reminted.Status = "sent"
	reminted.ExternalSendApplied = true
	reminted.CreatedAt = reminted.CreatedAt.Add(48 * time.Hour)
	reminted.SupersededBy = modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000b")
	if got := ComputeDailyRoutineReportContentHash(reminted); got != digest {
		t.Fatalf("digest after identity, lifecycle and created_at changes = %s, want %s", got, digest)
	}
	if got := string(reminted.ArtifactID); got == digest {
		t.Fatalf("digest %s reuses the reminted ArtifactID as a hash", digest)
	}

	// A missing action list and an empty one are the same content.
	nilActions := testReportFixture()
	nilActions.SuggestedActions = nil
	emptyActions := testReportFixture()
	emptyActions.SuggestedActions = []string{}
	if got, want := string(DailyRoutineReportBodyBytes(nilActions)), string(DailyRoutineReportBodyBytes(emptyActions)); got != want {
		t.Fatalf("nil action list body bytes = %s, want %s", got, want)
	}
	if got, want := ComputeDailyRoutineReportContentHash(nilActions), ComputeDailyRoutineReportContentHash(emptyActions); got != want {
		t.Fatalf("nil action list digest = %s, want %s", got, want)
	}

	cases := []struct {
		name   string
		mutate func(*DailyRoutineReport)
	}{
		{"summary", func(i *DailyRoutineReport) { i.Summary += "外部送信しました。" }},
		{"count", func(i *DailyRoutineReport) { i.MarketResearch = 2 }},
		{"paid customers", func(i *DailyRoutineReport) { i.PaidCustomers = 0 }},
		{"appended action", func(i *DailyRoutineReport) { i.SuggestedActions = append(i.SuggestedActions, "商品を追加する") }},
	}
	for _, tc := range cases {
		changed := testReportFixture()
		tc.mutate(&changed)
		if got := ComputeDailyRoutineReportContentHash(changed); got == digest {
			t.Errorf("digest unchanged by a %s change: %s", tc.name, got)
		}
	}

	// Order is content: two reports listing the same actions in another order differ.
	first := testReportFixture()
	first.SuggestedActions = []string{"市場調査を追加する", "商品を追加する"}
	second := testReportFixture()
	second.SuggestedActions = []string{"商品を追加する", "市場調査を追加する"}
	if ComputeDailyRoutineReportContentHash(first) == ComputeDailyRoutineReportContentHash(second) {
		t.Error("reordering the suggested actions left the digest unchanged")
	}
}

// TestValidateDailyRoutineReportAcceptsCanonicalSuccessors pins that a migrated UUIDv5 and
// a newly minted UUIDv7 successor are both accepted with the correct body digest, and
// that a self reference, an opaque reference and a digest of other bytes are refused.
func TestValidateDailyRoutineReportAcceptsCanonicalSuccessors(t *testing.T) {
	for _, successor := range []string{
		"art_00000000-0000-5000-8000-00000000000b",
		"art_00000000-0000-7000-8000-00000000000d",
	} {
		item := testReportFixture()
		item.ContentHash = ComputeDailyRoutineReportContentHash(item)
		item.SupersededBy = modulecore.ArtifactID(successor)
		if err := ValidateDailyRoutineReport(item); err != nil {
			t.Errorf("ValidateDailyRoutineReport() successor %s error = %v", successor, err)
		}
	}

	self := testReportFixture()
	self.ContentHash = ComputeDailyRoutineReportContentHash(self)
	self.SupersededBy = self.ArtifactID
	if err := ValidateDailyRoutineReport(self); err == nil || !strings.Contains(err.Error(), "must not reference the artifact itself") {
		t.Errorf("error = %v, want self reference reason", err)
	}

	opaque := testReportFixture()
	opaque.ContentHash = ComputeDailyRoutineReportContentHash(opaque)
	opaque.SupersededBy = modulecore.ArtifactID("art_1")
	if err := ValidateDailyRoutineReport(opaque); err == nil || !strings.Contains(err.Error(), "superseded_by") {
		t.Errorf("error = %v, want opaque superseded_by rejection", err)
	}

	otherBody := testReportFixture()
	otherBody.ContentHash = revenueTestContentHash(t, revenueReportMinimalBody)
	if err := ValidateDailyRoutineReport(otherBody); err == nil || !strings.Contains(err.Error(), "does not match content") {
		t.Errorf("error = %v, want content digest mismatch rejection", err)
	}

	malformed := testReportFixture()
	malformed.ContentHash = "sha256:0000"
	if err := ValidateDailyRoutineReport(malformed); err == nil || !strings.Contains(err.Error(), "content_hash") {
		t.Errorf("error = %v, want malformed content_hash rejection", err)
	}
}

// TestChannelDraftDigestCoversBodyNotIdentityMetadata pins the draft body bytes and the
// escaping of quotes, newlines and non-ASCII text, and keeps the successor reference and
// identity metadata out of the digest.
func TestChannelDraftDigestCoversBodyNotIdentityMetadata(t *testing.T) {
	item := testDraftFixture()
	if got := string(ChannelDraftBodyBytes(item)); got != revenueDraftFixtureBody {
		t.Fatalf("draft body bytes = %s, want %s", got, revenueDraftFixtureBody)
	}
	digest := ComputeChannelDraftContentHash(item)
	if want := revenueTestContentHash(t, revenueDraftFixtureBody); digest != want {
		t.Fatalf("draft digest = %s, want %s", digest, want)
	}

	reminted := testDraftFixture()
	reminted.ArtifactID = modulecore.ArtifactID("art_00000000-0000-7000-8000-00000000000e")
	reminted.TraceID = "trc_2"
	reminted.OpportunityID = "opp_2"
	reminted.WorkstreamID = "ws_other"
	reminted.SourceArtifactID = modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000003")
	reminted.CreatedAt = reminted.CreatedAt.Add(48 * time.Hour)
	reminted.SupersededBy = modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000b")
	if got := ComputeChannelDraftContentHash(reminted); got != digest {
		t.Fatalf("digest after identity and created_at changes = %s, want %s", got, digest)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*ChannelDraft)
	}{
		{"channel", func(i *ChannelDraft) { i.Channel = "email" }},
		{"subject", func(i *ChannelDraft) { i.Subject = "Re: 納品のご確認（再送）" }},
		{"body", func(i *ChannelDraft) { i.Body = "本日18時までに納品いたします。" }},
	} {
		changed := testDraftFixture()
		tc.mutate(&changed)
		if got := ComputeChannelDraftContentHash(changed); got == digest {
			t.Errorf("digest unchanged by a %s change: %s", tc.name, got)
		}
	}

	// Quotes, newlines and non-ASCII text are digested in the persisted JSON form.
	punctuation := testDraftFixture()
	punctuation.Subject = `Re: "納品"のご確認`
	punctuation.Body = "本日17時までに納品いたします。\n（日本語と引用符と改行）"
	want := `{"channel":"x","subject":"Re: \"納品\"のご確認","body":"本日17時までに納品いたします。\n（日本語と引用符と改行）"}`
	if got := string(ChannelDraftBodyBytes(punctuation)); got != want {
		t.Fatalf("draft body bytes = %s, want %s", got, want)
	}
	if got, wantDigest := ComputeChannelDraftContentHash(punctuation), revenueTestContentHash(t, want); got != wantDigest {
		t.Fatalf("draft digest = %s, want %s", got, wantDigest)
	}
}

// TestValidateChannelDraftAcceptsCanonicalSuccessors pins the draft successor forms: the
// migrated UUIDv5 and the new UUIDv7 are accepted, a self reference and an opaque
// reference are refused, and a digest of other bytes never passes.
func TestValidateChannelDraftAcceptsCanonicalSuccessors(t *testing.T) {
	for _, successor := range []string{
		"art_00000000-0000-5000-8000-00000000000b",
		"art_00000000-0000-7000-8000-00000000000d",
	} {
		item := testDraftFixture()
		item.ContentHash = ComputeChannelDraftContentHash(item)
		item.SupersededBy = modulecore.ArtifactID(successor)
		if err := ValidateChannelDraft(item); err != nil {
			t.Errorf("ValidateChannelDraft() successor %s error = %v", successor, err)
		}
	}

	self := testDraftFixture()
	self.ContentHash = ComputeChannelDraftContentHash(self)
	self.SupersededBy = self.ArtifactID
	if err := ValidateChannelDraft(self); err == nil || !strings.Contains(err.Error(), "must not reference the artifact itself") {
		t.Errorf("error = %v, want self reference reason", err)
	}

	opaque := testDraftFixture()
	opaque.ContentHash = ComputeChannelDraftContentHash(opaque)
	opaque.SupersededBy = modulecore.ArtifactID("art_1")
	if err := ValidateChannelDraft(opaque); err == nil || !strings.Contains(err.Error(), "superseded_by") {
		t.Errorf("error = %v, want opaque superseded_by rejection", err)
	}

	otherBody := testDraftFixture()
	otherBody.ContentHash = revenueTestContentHash(t, revenueDraftMinimalBody)
	if err := ValidateChannelDraft(otherBody); err == nil || !strings.Contains(err.Error(), "does not match content") {
		t.Errorf("error = %v, want content digest mismatch rejection", err)
	}

	malformed := testDraftFixture()
	malformed.ContentHash = "sha512:" + strings.Repeat("a", 64)
	if err := ValidateChannelDraft(malformed); err == nil || !strings.Contains(err.Error(), "content_hash") {
		t.Errorf("error = %v, want malformed content_hash rejection", err)
	}
}
