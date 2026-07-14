package revenue

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var prohibitedClaims = []string{
	"必ず稼げる",
	"必ず稼げます",
	"確実に稼げる",
	"確実に稼げます",
	"成功率100%",
	"再現性100%",
	"誰でも必ず",
	"この通りやれば確実",
	"失敗しない",
}

var approvalRequiredDecisionTypes = map[string]struct{}{
	"market_selection":            {},
	"product_price":               {},
	"high_ticket_offer":           {},
	"customer_voice_publication":  {},
	"paid_ads":                    {},
	"paid_api_use":                {},
	"refund_policy":               {},
	"medical_expression":          {},
	"financial_expression":        {},
	"legal_expression":            {},
	"external_publish":            {},
	"closed_channel_send":         {},
	"customer_individual_message": {},
	"contract":                    {},
	"billing":                     {},
	"github_publication":          {},
	"personal_data_use":           {},
}

func ValidateMarketResearchItem(item MarketResearchItem) error {
	if strings.TrimSpace(item.ItemID) == "" {
		return errors.New("item_id is required")
	}
	if strings.TrimSpace(item.SourcePlatform) == "" {
		return errors.New("source_platform is required")
	}
	if item.Price < 0 {
		return errors.New("price must be >= 0")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateSNSPostMetric(item SNSPostMetric) error {
	if strings.TrimSpace(item.PostID) == "" {
		return errors.New("post_id is required")
	}
	if strings.TrimSpace(item.Platform) == "" {
		return errors.New("platform is required")
	}
	if item.Impressions < 0 || item.Likes < 0 || item.Reposts < 0 || item.Comments < 0 || item.Saves < 0 || item.ProfileClicks < 0 || item.LinkClicks < 0 || item.SalesCount < 0 {
		return errors.New("metrics must be >= 0")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateProduct(item Product) error {
	if strings.TrimSpace(item.ProductID) == "" {
		return errors.New("product_id is required")
	}
	if strings.TrimSpace(item.ProductName) == "" {
		return errors.New("product_name is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return errors.New("status is required")
	}
	if item.Price < 0 {
		return errors.New("price must be >= 0")
	}
	if check := CheckEthics(strings.Join([]string{item.ProductName, item.Target, item.Pain, item.Promise, item.Deliverables}, "\n")); !check.Allowed {
		return errors.New(strings.Join(check.Reasons, "; "))
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateCustomerVoice(item CustomerVoice) error {
	if strings.TrimSpace(item.VoiceID) == "" {
		return errors.New("voice_id is required")
	}
	if strings.TrimSpace(item.RawText) == "" {
		return errors.New("raw_text is required")
	}
	if strings.TrimSpace(item.PermissionStatus) == "" {
		return errors.New("permission_status is required")
	}
	if item.UsableForMarketing && item.PermissionStatus != "granted" {
		return errors.New("customer voice requires permission_status=granted when usable_for_marketing=true")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateRevenueEvent(item RevenueEvent) error {
	if strings.TrimSpace(item.EventID) == "" {
		return errors.New("event_id is required")
	}
	if strings.TrimSpace(item.EventType) == "" {
		return errors.New("event_type is required")
	}
	if item.Amount < 0 {
		return errors.New("amount must be >= 0")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func NormalizeOpportunityEconomics(item Opportunity) Opportunity {
	item.ExpectedProfit = item.ExpectedRevenue - item.ExpectedCost
	if item.ExpectedRevenue > 0 {
		item.ProfitMargin = float64(item.ExpectedProfit) / float64(item.ExpectedRevenue)
	} else {
		item.ProfitMargin = 0
	}
	return item
}

func ValidateOpportunity(item Opportunity) error {
	if strings.TrimSpace(item.OpportunityID) == "" {
		return errors.New("opportunity_id is required")
	}
	if strings.TrimSpace(item.SourceKind) == "" {
		return errors.New("source_kind is required")
	}
	if strings.TrimSpace(item.Title) == "" {
		return errors.New("title is required")
	}
	if item.ExpectedRevenue < 0 || item.ExpectedCost < 0 {
		return errors.New("expected revenue/cost must be >= 0")
	}
	if item.RiskScore < 0 || item.RiskScore > 1 {
		return errors.New("risk_score must be between 0 and 1")
	}
	switch strings.TrimSpace(item.ApprovalState) {
	case "", "draft", "pending", "approved", "rejected":
	default:
		return errors.New("approval_state must be draft, pending, approved, or rejected")
	}
	if check := CheckEthics(strings.Join([]string{item.Title, item.Summary, item.TargetCustomer}, "\n")); !check.Allowed {
		return errors.New(strings.Join(check.Reasons, "; "))
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateEconomicTask(item EconomicTask) error {
	if strings.TrimSpace(item.TaskID) == "" {
		return errors.New("task_id is required")
	}
	if strings.TrimSpace(item.OpportunityID) == "" {
		return errors.New("opportunity_id is required")
	}
	if strings.TrimSpace(item.AgentID) == "" {
		return errors.New("agent_id is required")
	}
	if strings.TrimSpace(item.TaskKind) == "" {
		return errors.New("task_kind is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return errors.New("status is required")
	}
	if item.Risk < 0 || item.Risk > 1 {
		return errors.New("risk must be between 0 and 1")
	}
	if item.Cost < 0 {
		return errors.New("cost must be >= 0")
	}
	if requiresHumanApproval(item.TaskKind) && item.ApprovalMode != "human_required" {
		return errors.New("approval_mode must be human_required for approval-required economic task")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateEconomicReflection(item EconomicReflection) error {
	if strings.TrimSpace(item.ReflectionID) == "" {
		return errors.New("reflection_id is required")
	}
	if strings.TrimSpace(item.OpportunityID) == "" {
		return errors.New("opportunity_id is required")
	}
	if strings.TrimSpace(item.Outcome) == "" {
		return errors.New("outcome is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateDailyRoutineReport(item DailyRoutineReport) error {
	if strings.TrimSpace(item.ReportID) == "" {
		return errors.New("report_id is required")
	}
	if strings.TrimSpace(item.Date) == "" {
		return errors.New("date is required")
	}
	if strings.TrimSpace(item.Status) != "draft_report" {
		return errors.New("status must be draft_report")
	}
	if item.ExternalSendApplied {
		return errors.New("daily routine report must not apply external send")
	}
	if item.MarketResearch < 0 || item.SNSPosts < 0 || item.Products < 0 || item.CustomerVoices < 0 || item.RevenueEvents < 0 || item.PaidCustomers < 0 || item.PendingDecisions < 0 {
		return errors.New("daily routine counts must be >= 0")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func requiresHumanApproval(decisionType string) bool {
	_, ok := approvalRequiredDecisionTypes[strings.TrimSpace(decisionType)]
	return ok
}

func RequiresHumanApproval(decisionType string) bool {
	return requiresHumanApproval(decisionType)
}

func ValidateChannelDraft(item ChannelDraft) error {
	if strings.TrimSpace(item.DraftID) == "" {
		return errors.New("draft_id is required")
	}
	if strings.TrimSpace(item.Channel) == "" {
		return errors.New("channel is required")
	}
	if strings.TrimSpace(item.Body) == "" {
		return errors.New("body is required")
	}
	switch strings.TrimSpace(item.ApprovalStatus) {
	case "", "pending", "approved", "rejected":
	default:
		return errors.New("approval_status must be pending, approved, or rejected")
	}
	if item.ExternalSendApplied {
		return errors.New("channel draft must not apply external send")
	}
	if check := CheckEthics(strings.Join([]string{item.Subject, item.Body}, "\n")); !check.Allowed {
		return errors.New(strings.Join(check.Reasons, "; "))
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateExternalSendApplyRecord(item ExternalSendApplyRecord) error {
	if strings.TrimSpace(item.ApplyID) == "" {
		return errors.New("apply_id is required")
	}
	if strings.TrimSpace(item.DraftID) == "" {
		return errors.New("draft_id is required")
	}
	if strings.TrimSpace(item.DecisionID) == "" {
		return errors.New("decision_id is required")
	}
	if strings.TrimSpace(item.Channel) == "" {
		return errors.New("channel is required")
	}
	if strings.TrimSpace(item.ApprovalStatus) != "approved" {
		return errors.New("approval_status must be approved")
	}
	if !item.HumanApproved {
		return errors.New("human_approved is required")
	}
	switch strings.TrimSpace(item.ApplyStatus) {
	case "blocked", "failed", "sent":
	default:
		return errors.New("apply_status must be blocked, failed, or sent")
	}
	if strings.TrimSpace(item.SendResult) == "" {
		return errors.New("send_result is required")
	}
	if item.ApplyStatus != "sent" && strings.TrimSpace(item.SendResult) == "sent" {
		return errors.New("send_result=sent requires apply_status=sent")
	}
	if item.ExternalSendApplied && item.ApplyStatus != "sent" {
		return errors.New("external_send_applied requires apply_status=sent")
	}
	if item.ApplyStatus == "sent" && !item.ExternalSendApplied {
		return errors.New("apply_status=sent requires external_send_applied=true")
	}
	if item.ApplyStatus == "sent" && !item.PostSendVerified {
		return errors.New("apply_status=sent requires post_send_verified=true")
	}
	if item.ApplyStatus != "sent" && item.PostSendVerified {
		return errors.New("post_send_verified requires apply_status=sent")
	}
	if item.PostSendVerified && strings.TrimSpace(item.PostSendEvidence) == "" {
		return errors.New("post_send_evidence is required when post_send_verified is true")
	}
	if item.ApplyStatus != "sent" && strings.TrimSpace(item.FailureReason) == "" {
		return errors.New("failure_reason is required unless apply_status=sent")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateHumanDecisionGateRecord(item HumanDecisionGateRecord) error {
	if strings.TrimSpace(item.DecisionID) == "" {
		return errors.New("decision_id is required")
	}
	if strings.TrimSpace(item.DecisionType) == "" {
		return errors.New("decision_type is required")
	}
	switch strings.TrimSpace(item.ApprovalStatus) {
	case "", "pending", "approved", "rejected":
	default:
		return errors.New("approval_status must be pending, approved, or rejected")
	}
	if strings.TrimSpace(item.GateStatus) == "" {
		return errors.New("gate_status is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func CheckEthics(text string) EthicsCheck {
	var reasons []string
	for _, phrase := range prohibitedClaims {
		if strings.Contains(text, phrase) {
			reasons = append(reasons, "prohibited revenue claim: "+phrase)
		}
	}
	return EthicsCheck{
		Allowed: len(reasons) == 0,
		Reasons: reasons,
	}
}

type DailyRoutineInput struct {
	ReportID       string
	WorkstreamID   string
	Date           string
	Now            time.Time
	MarketResearch []MarketResearchItem
	SNSPosts       []SNSPostMetric
	Products       []Product
	CustomerVoices []CustomerVoice
	RevenueEvents  []RevenueEvent
	Decisions      []HumanDecisionGateRecord
}

func BuildDailyRoutineReport(input DailyRoutineInput) DailyRoutineReport {
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	date := strings.TrimSpace(input.Date)
	if date == "" {
		date = now.Format("2006-01-02")
	}
	reportID := strings.TrimSpace(input.ReportID)
	if reportID == "" {
		reportID = "rev_daily_" + now.UTC().Format("20060102T150405Z")
	}
	paidCustomers := uniquePaidCustomerCount(input.RevenueEvents)
	pendingDecisions := 0
	for _, decision := range input.Decisions {
		if decision.ApprovalStatus == "pending" || decision.GateStatus == "needs_review" {
			pendingDecisions++
		}
	}
	report := DailyRoutineReport{
		ReportID:            reportID,
		WorkstreamID:        strings.TrimSpace(input.WorkstreamID),
		Date:                date,
		MarketResearch:      len(input.MarketResearch),
		SNSPosts:            len(input.SNSPosts),
		Products:            len(input.Products),
		CustomerVoices:      len(input.CustomerVoices),
		RevenueEvents:       len(input.RevenueEvents),
		PaidCustomers:       paidCustomers,
		PendingDecisions:    pendingDecisions,
		Status:              "draft_report",
		ExternalSendApplied: false,
		CreatedAt:           now.UTC(),
	}
	report.Summary = fmt.Sprintf("%s のRevenue日次ルーチン下書きです。市場調査%d件、SNS投稿%d件、商品%d件、顧客の声%d件、収益イベント%d件を確認しました。外部送信や公開は行っていません。",
		report.Date,
		report.MarketResearch,
		report.SNSPosts,
		report.Products,
		report.CustomerVoices,
		report.RevenueEvents,
	)
	report.SuggestedActions = buildDailyRoutineSuggestedActions(report)
	return report
}

func uniquePaidCustomerCount(events []RevenueEvent) int {
	seen := map[string]struct{}{}
	anonymous := 0
	for _, event := range events {
		if event.EventType != "purchase" || event.Amount <= 0 {
			continue
		}
		customerID := strings.TrimSpace(event.CustomerID)
		if customerID == "" {
			anonymous++
			continue
		}
		seen[customerID] = struct{}{}
	}
	return len(seen) + anonymous
}

func buildDailyRoutineSuggestedActions(report DailyRoutineReport) []string {
	actions := []string{}
	if report.MarketResearch == 0 {
		actions = append(actions, "市場調査を追加する")
	}
	if report.SNSPosts == 0 {
		actions = append(actions, "SNS投稿または反応指標を記録する")
	}
	if report.Products == 0 {
		actions = append(actions, "低単価商品の候補を1つ作る")
	}
	if report.CustomerVoices == 0 {
		actions = append(actions, "購入者または見込み顧客の声を記録する")
	}
	if report.PendingDecisions > 0 {
		actions = append(actions, "Human Decision Gateの保留判断を確認する")
	}
	if len(actions) == 0 {
		actions = append(actions, "反応が取れた投稿と顧客の声から次の商品改善案を作る")
	}
	return actions
}

func BuildHumanDecisionGateRecord(req HumanDecisionGateRequest) HumanDecisionGateRecord {
	result := EvaluateHumanDecisionGate(req)
	approvalStatus := strings.TrimSpace(req.ApprovalStatus)
	if approvalStatus == "" && result.RequiresApproval {
		approvalStatus = "pending"
	}
	return HumanDecisionGateRecord{
		DecisionID:       strings.TrimSpace(req.DecisionID),
		DecisionType:     strings.TrimSpace(req.DecisionType),
		SubjectID:        strings.TrimSpace(req.SubjectID),
		Description:      req.Description,
		ApprovalStatus:   approvalStatus,
		GateStatus:       result.Status,
		RequiresApproval: result.RequiresApproval,
		Reasons:          result.Reasons,
		CreatedAt:        req.CreatedAt,
	}
}

func EvaluateHumanDecisionGate(req HumanDecisionGateRequest) HumanDecisionGateResult {
	decisionType := strings.TrimSpace(req.DecisionType)
	if decisionType == "" {
		return HumanDecisionGateResult{
			Status:           "blocked",
			RequiresApproval: true,
			Reasons:          []string{"decision_type is required"},
		}
	}

	var reasons []string
	if check := CheckEthics(req.Description); !check.Allowed {
		reasons = append(reasons, check.Reasons...)
	}
	if len(reasons) > 0 {
		return HumanDecisionGateResult{
			Status:           "blocked",
			RequiresApproval: true,
			Reasons:          reasons,
		}
	}

	_, requiresApproval := approvalRequiredDecisionTypes[decisionType]
	if !requiresApproval {
		return HumanDecisionGateResult{
			Status:           "approved",
			RequiresApproval: false,
		}
	}

	switch strings.TrimSpace(req.ApprovalStatus) {
	case "approved":
		return HumanDecisionGateResult{
			Status:           "approved",
			RequiresApproval: true,
		}
	case "rejected":
		return HumanDecisionGateResult{
			Status:           "blocked",
			RequiresApproval: true,
			Reasons:          []string{"human approval was rejected"},
		}
	default:
		return HumanDecisionGateResult{
			Status:           "needs_review",
			RequiresApproval: true,
			Reasons:          []string{"human approval is required"},
		}
	}
}
