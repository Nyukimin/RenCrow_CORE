package revenue

import (
	"strings"
	"testing"
	"time"
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
	if err := ValidateDailyRoutineReport(DailyRoutineReport{ReportID: "daily_1", Date: "2026-05-18", Status: "draft_report", CreatedAt: now}); err != nil {
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
		{name: "daily report missing id", err: ValidateDailyRoutineReport(DailyRoutineReport{Date: "2026-05-20", Status: "draft_report", CreatedAt: now}), want: "report_id"},
		{name: "daily report missing date", err: ValidateDailyRoutineReport(DailyRoutineReport{ReportID: "daily_1", Status: "draft_report", CreatedAt: now}), want: "date"},
		{name: "daily report invalid status", err: ValidateDailyRoutineReport(DailyRoutineReport{ReportID: "daily_1", Date: "2026-05-20", Status: "sent", CreatedAt: now}), want: "status"},
		{name: "daily report negative count", err: ValidateDailyRoutineReport(DailyRoutineReport{ReportID: "daily_1", Date: "2026-05-20", Status: "draft_report", MarketResearch: -1, CreatedAt: now}), want: "counts"},
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
		{name: "daily routine", err: ValidateDailyRoutineReport(DailyRoutineReport{ReportID: "daily_1", Date: "2026-05-20", Status: "draft_report"})},
		{name: "channel draft", err: ValidateChannelDraft(ChannelDraft{DraftID: "draft_1", Channel: "email", Body: "下書き本文", ApprovalStatus: "pending"})},
		{name: "external send apply", err: ValidateExternalSendApplyRecord(ExternalSendApplyRecord{ApplyID: "apply_1", DraftID: "draft_1", DecisionID: "dec_1", Channel: "email", ApprovalStatus: "approved", HumanApproved: true, ApplyStatus: "blocked", SendResult: "not_sent", FailureReason: "external channel adapter is not configured"})},
		{name: "human decision", err: ValidateHumanDecisionGateRecord(HumanDecisionGateRecord{DecisionID: "dec_1", DecisionType: "external_publish", ApprovalStatus: "pending", GateStatus: "needs_review"})},
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
	if err := ValidateChannelDraft(ChannelDraft{DraftID: "draft_1", Channel: "email", Body: "下書き本文", ApprovalStatus: "", CreatedAt: now}); err != nil {
		t.Fatalf("empty approval status should use pending semantics: %v", err)
	}

	cases := []struct {
		name string
		item ChannelDraft
		want string
	}{
		{name: "missing id", item: ChannelDraft{Channel: "email", Body: "下書き本文", ApprovalStatus: "pending", CreatedAt: now}, want: "draft_id"},
		{name: "missing channel", item: ChannelDraft{DraftID: "draft_1", Body: "下書き本文", ApprovalStatus: "pending", CreatedAt: now}, want: "channel"},
		{name: "missing body", item: ChannelDraft{DraftID: "draft_1", Channel: "email", ApprovalStatus: "pending", CreatedAt: now}, want: "body"},
		{name: "invalid approval status", item: ChannelDraft{DraftID: "draft_1", Channel: "email", Body: "下書き本文", ApprovalStatus: "granted", CreatedAt: now}, want: "approval_status"},
		{name: "external send applied", item: ChannelDraft{DraftID: "draft_1", Channel: "email", Body: "下書き本文", ApprovalStatus: "approved", ExternalSendApplied: true, CreatedAt: now}, want: "external send"},
		{name: "prohibited claim", item: ChannelDraft{DraftID: "draft_1", Channel: "email", Subject: "案内", Body: "誰でも必ず稼げる", ApprovalStatus: "pending", CreatedAt: now}, want: "prohibited revenue claim"},
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

func TestEvaluateHumanDecisionGateRequiresApprovalForHighTicketOffer(t *testing.T) {
	result := EvaluateHumanDecisionGate(HumanDecisionGateRequest{
		DecisionType: "high_ticket_offer",
		Description:  "30万円の導入支援を案内する",
	})

	if result.Status != "needs_review" || !result.RequiresApproval {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestEvaluateHumanDecisionGateBlocksRejectedApproval(t *testing.T) {
	result := EvaluateHumanDecisionGate(HumanDecisionGateRequest{
		DecisionType:   "customer_voice_publication",
		Description:    "購入者の声を販売ページへ掲載する",
		ApprovalStatus: "rejected",
	})

	if result.Status != "blocked" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestEvaluateHumanDecisionGateAllowsApprovedDecision(t *testing.T) {
	result := EvaluateHumanDecisionGate(HumanDecisionGateRequest{
		DecisionType:   "product_price",
		Description:    "低単価商品の価格を980円にする",
		ApprovalStatus: "approved",
	})

	if result.Status != "approved" || !result.RequiresApproval {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestEvaluateHumanDecisionGateBlocksProhibitedClaim(t *testing.T) {
	result := EvaluateHumanDecisionGate(HumanDecisionGateRequest{
		DecisionType: "external_publish",
		Description:  "誰でも必ず稼げると投稿する",
	})

	if result.Status != "blocked" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestEvaluateHumanDecisionGateBlocksMissingTypeAndApprovesSafeInternalDecision(t *testing.T) {
	missing := EvaluateHumanDecisionGate(HumanDecisionGateRequest{Description: "通常の内部メモ"})
	if missing.Status != "blocked" || !missing.RequiresApproval || len(missing.Reasons) == 0 {
		t.Fatalf("unexpected missing type result: %#v", missing)
	}

	safe := EvaluateHumanDecisionGate(HumanDecisionGateRequest{
		DecisionType: "internal_note",
		Description:  "次回の商品改善メモを作る",
	})
	if safe.Status != "approved" || safe.RequiresApproval {
		t.Fatalf("unexpected safe result: %#v", safe)
	}
}

func TestBuildHumanDecisionGateRecordDefaultsPendingApproval(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 30, 0, 0, time.UTC)
	record := BuildHumanDecisionGateRecord(HumanDecisionGateRequest{
		DecisionID:   "dec_1",
		DecisionType: "high_ticket_offer",
		Description:  "30万円の導入支援を案内する",
		CreatedAt:    now,
	})

	if record.ApprovalStatus != "pending" || record.GateStatus != "needs_review" || !record.RequiresApproval {
		t.Fatalf("unexpected record: %#v", record)
	}
	if err := ValidateHumanDecisionGateRecord(record); err != nil {
		t.Fatalf("record should be valid: %v", err)
	}
}

func TestValidateHumanDecisionGateRecordRejectsInvalidApprovalStatus(t *testing.T) {
	err := ValidateHumanDecisionGateRecord(HumanDecisionGateRecord{
		DecisionID:     "dec_1",
		DecisionType:   "external_publish",
		ApprovalStatus: "granted",
		GateStatus:     "approved",
	})
	if err == nil {
		t.Fatal("expected invalid approval_status to fail")
	}
}

func TestValidateHumanDecisionGateRecordRequiredFields(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 30, 0, 0, time.UTC)
	cases := []struct {
		name string
		item HumanDecisionGateRecord
		want string
	}{
		{name: "missing id", item: HumanDecisionGateRecord{DecisionType: "external_publish", ApprovalStatus: "pending", GateStatus: "needs_review", CreatedAt: now}, want: "decision_id"},
		{name: "missing type", item: HumanDecisionGateRecord{DecisionID: "dec_1", ApprovalStatus: "pending", GateStatus: "needs_review", CreatedAt: now}, want: "decision_type"},
		{name: "missing gate status", item: HumanDecisionGateRecord{DecisionID: "dec_1", DecisionType: "external_publish", ApprovalStatus: "pending", CreatedAt: now}, want: "gate_status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateHumanDecisionGateRecord(tc.item)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateExternalSendApplyRecordRequiresApprovedHumanDecision(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 30, 0, 0, time.UTC)
	record := ExternalSendApplyRecord{
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
	}
	if err := ValidateExternalSendApplyRecord(record); err != nil {
		t.Fatalf("record should be valid: %v", err)
	}

	record.HumanApproved = false
	if err := ValidateExternalSendApplyRecord(record); err == nil {
		t.Fatal("expected missing human approval to fail")
	}
	record.HumanApproved = true
	record.ApprovalStatus = "pending"
	if err := ValidateExternalSendApplyRecord(record); err == nil {
		t.Fatal("expected unapproved decision to fail")
	}
	record.ApprovalStatus = "approved"
	record.ExternalSendApplied = true
	if err := ValidateExternalSendApplyRecord(record); err == nil {
		t.Fatal("expected externally applied non-sent record to fail")
	}
}

func TestValidateExternalSendApplyRecordRequiredFieldsAndStatuses(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 30, 0, 0, time.UTC)
	validBlocked := ExternalSendApplyRecord{
		ApplyID:        "apply_1",
		DraftID:        "draft_1",
		DecisionID:     "dec_1",
		Channel:        "email",
		ApprovalStatus: "approved",
		HumanApproved:  true,
		ApplyStatus:    "blocked",
		SendResult:     "not_sent",
		FailureReason:  "external channel adapter is not configured",
		CreatedAt:      now,
	}
	cases := []struct {
		name   string
		mutate func(*ExternalSendApplyRecord)
		want   string
	}{
		{name: "missing apply id", mutate: func(item *ExternalSendApplyRecord) { item.ApplyID = "" }, want: "apply_id"},
		{name: "missing draft id", mutate: func(item *ExternalSendApplyRecord) { item.DraftID = "" }, want: "draft_id"},
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
		ApplyID:             "apply_1",
		DraftID:             "draft_1",
		DecisionID:          "dec_1",
		Channel:             "email",
		ApprovalStatus:      "approved",
		HumanApproved:       true,
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
		PostSendVerified:    true,
	}
	if err := ValidateExternalSendApplyRecord(record); err == nil {
		t.Fatal("expected post_send_verified without sent status to fail")
	}
}

func TestValidateExternalSendApplyRecordRejectsSentResultWithoutSentStatus(t *testing.T) {
	record := ExternalSendApplyRecord{
		ApplyID:             "apply_1",
		DraftID:             "draft_1",
		DecisionID:          "dec_1",
		Channel:             "email",
		ApprovalStatus:      "approved",
		HumanApproved:       true,
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
		ReportID:       "daily_1",
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
		Decisions: []HumanDecisionGateRecord{{DecisionID: "dec_1", DecisionType: "external_publish", ApprovalStatus: "pending", GateStatus: "needs_review"}},
	})

	if report.Status != "draft_report" || report.ExternalSendApplied {
		t.Fatalf("expected draft-only report: %#v", report)
	}
	if report.PaidCustomers != 1 || report.PendingDecisions != 1 {
		t.Fatalf("unexpected counts: %#v", report)
	}
	if err := ValidateDailyRoutineReport(report); err != nil {
		t.Fatalf("report should be valid: %v", err)
	}
}

func TestBuildDailyRoutineReportDefaultsAndSuggestedActions(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 30, 0, 0, time.UTC)
	report := BuildDailyRoutineReport(DailyRoutineInput{Now: now})

	if report.ReportID != "rev_daily_20260520T073000Z" || report.Date != "2026-05-20" || !report.CreatedAt.Equal(now) {
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
		ReportID:       "daily_1",
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
		ReportID:            "daily_1",
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
