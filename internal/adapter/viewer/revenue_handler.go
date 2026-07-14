package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	revenueapp "github.com/Nyukimin/RenCrow_CORE/internal/application/revenue"
	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
)

type RevenueLister interface {
	ListMarketResearchItems(ctx context.Context, limit int) ([]domainrevenue.MarketResearchItem, error)
	ListSNSPostMetrics(ctx context.Context, limit int) ([]domainrevenue.SNSPostMetric, error)
	ListProducts(ctx context.Context, limit int) ([]domainrevenue.Product, error)
	ListCustomerVoices(ctx context.Context, limit int) ([]domainrevenue.CustomerVoice, error)
	ListRevenueEvents(ctx context.Context, limit int) ([]domainrevenue.RevenueEvent, error)
	ListHumanDecisionGateRecords(ctx context.Context, limit int) ([]domainrevenue.HumanDecisionGateRecord, error)
	ListDailyRoutineReports(ctx context.Context, limit int) ([]domainrevenue.DailyRoutineReport, error)
	ListChannelDrafts(ctx context.Context, limit int) ([]domainrevenue.ChannelDraft, error)
	ListExternalSendApplyRecords(ctx context.Context, limit int) ([]domainrevenue.ExternalSendApplyRecord, error)
	ListOpportunities(ctx context.Context, limit int) ([]domainrevenue.Opportunity, error)
	ListEconomicTasks(ctx context.Context, limit int) ([]domainrevenue.EconomicTask, error)
	ListEconomicReflections(ctx context.Context, limit int) ([]domainrevenue.EconomicReflection, error)
}

type RevenueStore interface {
	RevenueLister
	SaveMarketResearchItem(ctx context.Context, item domainrevenue.MarketResearchItem) error
	SaveSNSPostMetric(ctx context.Context, item domainrevenue.SNSPostMetric) error
	SaveProduct(ctx context.Context, item domainrevenue.Product) error
	SaveCustomerVoice(ctx context.Context, item domainrevenue.CustomerVoice) error
	SaveRevenueEvent(ctx context.Context, item domainrevenue.RevenueEvent) error
	SaveHumanDecisionGateRecord(ctx context.Context, item domainrevenue.HumanDecisionGateRecord) error
	SaveDailyRoutineReport(ctx context.Context, item domainrevenue.DailyRoutineReport) error
	SaveChannelDraft(ctx context.Context, item domainrevenue.ChannelDraft) error
	SaveExternalSendApplyRecord(ctx context.Context, item domainrevenue.ExternalSendApplyRecord) error
	SaveOpportunity(ctx context.Context, item domainrevenue.Opportunity) error
	SaveEconomicTask(ctx context.Context, item domainrevenue.EconomicTask) error
	SaveEconomicReflection(ctx context.Context, item domainrevenue.EconomicReflection) error
}

type RevenueHumanDecisionGateReviewRequest struct {
	DecisionID     string `json:"decision_id"`
	ApprovalStatus string `json:"approval_status"`
}

type RevenueDailyRoutineRequest struct {
	ReportID     string `json:"report_id,omitempty"`
	WorkstreamID string `json:"workstream_id,omitempty"`
	Date         string `json:"date,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

type RevenueExternalSendApplyRequest struct {
	ApplyID        string `json:"apply_id"`
	DraftID        string `json:"draft_id"`
	DecisionID     string `json:"decision_id"`
	Destination    string `json:"destination,omitempty"`
	ChannelAdapter string `json:"channel_adapter,omitempty"`
	HumanApproved  bool   `json:"human_approved"`
}

type RevenueDashboardSummary struct {
	MarketResearchCount    int                         `json:"market_research_count"`
	SNSPostCount           int                         `json:"sns_post_count"`
	ProductCount           int                         `json:"product_count"`
	CustomerVoiceCount     int                         `json:"customer_voice_count"`
	UsableVoiceCount       int                         `json:"usable_voice_count"`
	RevenueEventCount      int                         `json:"revenue_event_count"`
	PurchaseCount          int                         `json:"purchase_count"`
	PaidEventCount         int                         `json:"paid_event_count"`
	PaidCustomerCount      int                         `json:"paid_customer_count"`
	TotalRevenueAmount     int                         `json:"total_revenue_amount"`
	PendingDecisionCount   int                         `json:"pending_decision_count"`
	DailyReportCount       int                         `json:"daily_report_count"`
	LatestDailyReportID    string                      `json:"latest_daily_report_id,omitempty"`
	ChannelDraftCount      int                         `json:"channel_draft_count"`
	LatestChannelDraftID   string                      `json:"latest_channel_draft_id,omitempty"`
	ExternalSendApplyCount int                         `json:"external_send_apply_count"`
	KPITrend               []RevenueKPIDay             `json:"kpi_trend,omitempty"`
	ProductSales           []RevenueProductSales       `json:"product_sales,omitempty"`
	CustomerVoiceTypes     []RevenueCustomerVoiceCount `json:"customer_voice_types,omitempty"`
	ExternalActionsApplied bool                        `json:"external_actions_applied"`
}

type RevenueEconomicObjectiveSettings struct {
	Enabled   bool
	DraftOnly bool
}

type RevenueEconomicObjectiveSummary struct {
	Enabled                  bool `json:"enabled"`
	OpportunityCount         int  `json:"opportunity_count"`
	PendingApprovalTaskCount int  `json:"pending_approval_task_count"`
	ReflectionCount          int  `json:"reflection_count"`
	DraftOnly                bool `json:"draft_only"`
	ExternalActionBlocked    bool `json:"external_action_blocked"`
}

type RevenueKPIDay struct {
	Date          string `json:"date"`
	RevenueAmount int    `json:"revenue_amount"`
	PurchaseCount int    `json:"purchase_count"`
	PaidCustomers int    `json:"paid_customers"`
	SNSPostCount  int    `json:"sns_post_count"`
	VoiceCount    int    `json:"customer_voice_count"`
}

type RevenueProductSales struct {
	ProductID     string `json:"product_id"`
	ProductName   string `json:"product_name,omitempty"`
	SalesCount    int    `json:"sales_count"`
	RevenueAmount int    `json:"revenue_amount"`
}

type RevenueCustomerVoiceCount struct {
	VoiceType string `json:"voice_type"`
	Count     int    `json:"count"`
}

func HandleRevenueStatus(store RevenueLister, economicSettings ...RevenueEconomicObjectiveSettings) http.HandlerFunc {
	settings := RevenueEconomicObjectiveSettings{DraftOnly: true}
	if len(economicSettings) > 0 {
		settings = economicSettings[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "revenue store unavailable", http.StatusServiceUnavailable)
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 20, 100)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		market, err := store.ListMarketResearchItems(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load market research items", http.StatusInternalServerError)
			return
		}
		posts, err := store.ListSNSPostMetrics(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load sns post metrics", http.StatusInternalServerError)
			return
		}
		products, err := store.ListProducts(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load products", http.StatusInternalServerError)
			return
		}
		voices, err := store.ListCustomerVoices(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load customer voices", http.StatusInternalServerError)
			return
		}
		events, err := store.ListRevenueEvents(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load revenue events", http.StatusInternalServerError)
			return
		}
		decisions, err := store.ListHumanDecisionGateRecords(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load human decision gate records", http.StatusInternalServerError)
			return
		}
		dailyReports, err := store.ListDailyRoutineReports(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load daily routine reports", http.StatusInternalServerError)
			return
		}
		channelDrafts, err := store.ListChannelDrafts(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load channel drafts", http.StatusInternalServerError)
			return
		}
		externalSendApplyRecords, err := store.ListExternalSendApplyRecords(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load external send apply records", http.StatusInternalServerError)
			return
		}
		opportunities, err := store.ListOpportunities(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load opportunities", http.StatusInternalServerError)
			return
		}
		economicTasks, err := store.ListEconomicTasks(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load economic tasks", http.StatusInternalServerError)
			return
		}
		economicReflections, err := store.ListEconomicReflections(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load economic reflections", http.StatusInternalServerError)
			return
		}
		if market == nil {
			market = []domainrevenue.MarketResearchItem{}
		}
		if posts == nil {
			posts = []domainrevenue.SNSPostMetric{}
		}
		if products == nil {
			products = []domainrevenue.Product{}
		}
		if voices == nil {
			voices = []domainrevenue.CustomerVoice{}
		}
		if events == nil {
			events = []domainrevenue.RevenueEvent{}
		}
		if decisions == nil {
			decisions = []domainrevenue.HumanDecisionGateRecord{}
		}
		if dailyReports == nil {
			dailyReports = []domainrevenue.DailyRoutineReport{}
		}
		if channelDrafts == nil {
			channelDrafts = []domainrevenue.ChannelDraft{}
		}
		if externalSendApplyRecords == nil {
			externalSendApplyRecords = []domainrevenue.ExternalSendApplyRecord{}
		}
		if opportunities == nil {
			opportunities = []domainrevenue.Opportunity{}
		}
		if economicTasks == nil {
			economicTasks = []domainrevenue.EconomicTask{}
		}
		if economicReflections == nil {
			economicReflections = []domainrevenue.EconomicReflection{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"market_research":                           market,
			"sns_post_metrics":                          posts,
			"products":                                  products,
			"customer_voices":                           voices,
			"revenue_events":                            events,
			"human_decisions":                           decisions,
			"daily_routine_reports":                     dailyReports,
			"channel_drafts":                            channelDrafts,
			"external_send_apply_records":               externalSendApplyRecords,
			"opportunities":                             opportunities,
			"economic_tasks":                            economicTasks,
			"economic_reflections":                      economicReflections,
			"external_channel_adapter":                  "unconfigured",
			"external_channel_adapter_configured":       false,
			"human_approval_required_for_external_send": true,
			"summary":                                   buildRevenueDashboardSummary(market, posts, products, voices, events, decisions, dailyReports, channelDrafts, externalSendApplyRecords),
			"economic_objective":                        buildRevenueEconomicObjectiveSummary(settings, opportunities, economicTasks, economicReflections),
		})
	}
}

func buildRevenueEconomicObjectiveSummary(settings RevenueEconomicObjectiveSettings, opportunities []domainrevenue.Opportunity, tasks []domainrevenue.EconomicTask, reflections []domainrevenue.EconomicReflection) RevenueEconomicObjectiveSummary {
	summary := RevenueEconomicObjectiveSummary{
		Enabled: settings.Enabled, OpportunityCount: len(opportunities), ReflectionCount: len(reflections),
		DraftOnly: settings.DraftOnly, ExternalActionBlocked: true,
	}
	for _, item := range tasks {
		status := strings.TrimSpace(item.Status)
		if domainrevenue.RequiresHumanApproval(item.TaskKind) && item.ApprovalMode == "human_required" && status != "completed" && status != "rejected" {
			summary.PendingApprovalTaskCount++
		}
	}
	return summary
}

func buildRevenueDashboardSummary(market []domainrevenue.MarketResearchItem, posts []domainrevenue.SNSPostMetric, products []domainrevenue.Product, voices []domainrevenue.CustomerVoice, events []domainrevenue.RevenueEvent, decisions []domainrevenue.HumanDecisionGateRecord, reports []domainrevenue.DailyRoutineReport, channelDrafts []domainrevenue.ChannelDraft, externalSendApplyRecords []domainrevenue.ExternalSendApplyRecord) RevenueDashboardSummary {
	summary := RevenueDashboardSummary{
		MarketResearchCount:    len(market),
		SNSPostCount:           len(posts),
		ProductCount:           len(products),
		CustomerVoiceCount:     len(voices),
		RevenueEventCount:      len(events),
		DailyReportCount:       len(reports),
		ChannelDraftCount:      len(channelDrafts),
		ExternalSendApplyCount: len(externalSendApplyRecords),
	}
	for _, voice := range voices {
		if voice.UsableForMarketing {
			summary.UsableVoiceCount++
		}
	}
	paidCustomers := map[string]struct{}{}
	for _, event := range events {
		if strings.EqualFold(strings.TrimSpace(event.EventType), "purchase") {
			summary.PurchaseCount++
		}
		if event.Amount > 0 {
			summary.PaidEventCount++
			summary.TotalRevenueAmount += event.Amount
			if strings.TrimSpace(event.CustomerID) != "" {
				paidCustomers[event.CustomerID] = struct{}{}
			}
		}
	}
	summary.PaidCustomerCount = len(paidCustomers)
	latestDecisions := map[string]domainrevenue.HumanDecisionGateRecord{}
	for _, decision := range decisions {
		key := strings.TrimSpace(decision.DecisionID)
		if key == "" {
			key = decision.DecisionType + ":" + decision.SubjectID
		}
		latestDecisions[key] = decision
	}
	for _, decision := range latestDecisions {
		if decision.ApprovalStatus == "pending" || decision.GateStatus == "needs_review" {
			summary.PendingDecisionCount++
		}
	}
	if len(reports) > 0 {
		summary.LatestDailyReportID = reports[0].ReportID
	}
	if len(channelDrafts) > 0 {
		summary.LatestChannelDraftID = channelDrafts[0].DraftID
	}
	for _, record := range externalSendApplyRecords {
		if record.ExternalSendApplied {
			summary.ExternalActionsApplied = true
			break
		}
	}
	summary.KPITrend = buildRevenueKPITrend(posts, voices, events)
	summary.ProductSales = buildRevenueProductSales(products, events)
	summary.CustomerVoiceTypes = buildRevenueCustomerVoiceTypes(voices)
	return summary
}

func buildRevenueKPITrend(posts []domainrevenue.SNSPostMetric, voices []domainrevenue.CustomerVoice, events []domainrevenue.RevenueEvent) []RevenueKPIDay {
	type bucket struct {
		day       RevenueKPIDay
		customers map[string]struct{}
	}
	buckets := map[string]*bucket{}
	get := func(date string) *bucket {
		if date == "" {
			date = "unknown"
		}
		if buckets[date] == nil {
			buckets[date] = &bucket{day: RevenueKPIDay{Date: date}, customers: map[string]struct{}{}}
		}
		return buckets[date]
	}
	for _, post := range posts {
		day := get(revenueDayKey(post.PostedAt, post.CreatedAt))
		day.day.SNSPostCount++
	}
	for _, voice := range voices {
		day := get(revenueDayKey(voice.CreatedAt))
		day.day.VoiceCount++
	}
	for _, event := range events {
		day := get(revenueDayKey(event.CreatedAt))
		if event.Amount > 0 {
			day.day.RevenueAmount += event.Amount
			if strings.TrimSpace(event.CustomerID) != "" {
				day.customers[event.CustomerID] = struct{}{}
			}
		}
		if strings.EqualFold(strings.TrimSpace(event.EventType), "purchase") {
			day.day.PurchaseCount++
		}
	}
	dates := make([]string, 0, len(buckets))
	for date := range buckets {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	trend := make([]RevenueKPIDay, 0, len(dates))
	for _, date := range dates {
		day := buckets[date].day
		day.PaidCustomers = len(buckets[date].customers)
		trend = append(trend, day)
	}
	return trend
}

func buildRevenueProductSales(products []domainrevenue.Product, events []domainrevenue.RevenueEvent) []RevenueProductSales {
	names := map[string]string{}
	for _, product := range products {
		if strings.TrimSpace(product.ProductID) != "" {
			names[product.ProductID] = product.ProductName
		}
	}
	sales := map[string]*RevenueProductSales{}
	for _, event := range events {
		productID := strings.TrimSpace(event.ProductID)
		if productID == "" || event.Amount <= 0 {
			continue
		}
		if sales[productID] == nil {
			sales[productID] = &RevenueProductSales{ProductID: productID, ProductName: names[productID]}
		}
		sales[productID].RevenueAmount += event.Amount
		if strings.EqualFold(strings.TrimSpace(event.EventType), "purchase") {
			sales[productID].SalesCount++
		}
	}
	result := make([]RevenueProductSales, 0, len(sales))
	for _, item := range sales {
		result = append(result, *item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].RevenueAmount == result[j].RevenueAmount {
			return result[i].ProductID < result[j].ProductID
		}
		return result[i].RevenueAmount > result[j].RevenueAmount
	})
	return result
}

func buildRevenueCustomerVoiceTypes(voices []domainrevenue.CustomerVoice) []RevenueCustomerVoiceCount {
	counts := map[string]int{}
	for _, voice := range voices {
		voiceType := strings.TrimSpace(voice.VoiceType)
		if voiceType == "" {
			voiceType = "unknown"
		}
		counts[voiceType]++
	}
	types := make([]string, 0, len(counts))
	for voiceType := range counts {
		types = append(types, voiceType)
	}
	sort.Strings(types)
	result := make([]RevenueCustomerVoiceCount, 0, len(types))
	for _, voiceType := range types {
		result = append(result, RevenueCustomerVoiceCount{VoiceType: voiceType, Count: counts[voiceType]})
	}
	return result
}

func revenueDayKey(values ...time.Time) string {
	for _, value := range values {
		if !value.IsZero() {
			return value.UTC().Format("2006-01-02")
		}
	}
	return "unknown"
}

func HandleRevenueMarketResearchCreate(store RevenueStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var item domainrevenue.MarketResearchItem
		if !decodeRevenuePost(w, r, &item, store) {
			return
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := store.SaveMarketResearchItem(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"market_research": item})
	}
}

func HandleRevenueSNSPostMetricCreate(store RevenueStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var item domainrevenue.SNSPostMetric
		if !decodeRevenuePost(w, r, &item, store) {
			return
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := store.SaveSNSPostMetric(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"sns_post_metric": item})
	}
}

func HandleRevenueProductCreate(store RevenueStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var item domainrevenue.Product
		if !decodeRevenuePost(w, r, &item, store) {
			return
		}
		if item.Status == "" {
			item.Status = "draft"
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := store.SaveProduct(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"product": item})
	}
}

func HandleRevenueCustomerVoiceCreate(store RevenueStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var item domainrevenue.CustomerVoice
		if !decodeRevenuePost(w, r, &item, store) {
			return
		}
		if item.PermissionStatus == "" {
			item.PermissionStatus = "unknown"
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := store.SaveCustomerVoice(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"customer_voice": item})
	}
}

func HandleRevenueEventCreate(store RevenueStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var item domainrevenue.RevenueEvent
		if !decodeRevenuePost(w, r, &item, store) {
			return
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := store.SaveRevenueEvent(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"revenue_event": item})
	}
}

func HandleRevenueHumanDecisionGate(store RevenueStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "revenue store unavailable", http.StatusServiceUnavailable)
			return
		}
		var item domainrevenue.HumanDecisionGateRequest
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			http.Error(w, "invalid revenue decision payload", http.StatusBadRequest)
			return
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		record := domainrevenue.BuildHumanDecisionGateRecord(item)
		if err := store.SaveHumanDecisionGateRecord(r.Context(), record); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"decision": item,
			"record":   record,
			"result": domainrevenue.HumanDecisionGateResult{
				Status:           record.GateStatus,
				RequiresApproval: record.RequiresApproval,
				Reasons:          record.Reasons,
			},
		})
	}
}

func HandleRevenueHumanDecisionGateReview(store RevenueStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "revenue store unavailable", http.StatusServiceUnavailable)
			return
		}
		var req RevenueHumanDecisionGateReviewRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid revenue decision review payload", http.StatusBadRequest)
			return
		}
		if req.DecisionID == "" {
			http.Error(w, "decision_id is required", http.StatusBadRequest)
			return
		}
		if req.ApprovalStatus != "approved" && req.ApprovalStatus != "rejected" {
			http.Error(w, "approval_status must be approved or rejected", http.StatusBadRequest)
			return
		}
		records, err := store.ListHumanDecisionGateRecords(r.Context(), 500)
		if err != nil {
			http.Error(w, "failed to load human decision gate records", http.StatusInternalServerError)
			return
		}
		var current *domainrevenue.HumanDecisionGateRecord
		for i := range records {
			if records[i].DecisionID == req.DecisionID {
				current = &records[i]
				break
			}
		}
		if current == nil {
			http.Error(w, "human decision gate record not found", http.StatusNotFound)
			return
		}
		decision := domainrevenue.HumanDecisionGateRequest{
			DecisionID:     current.DecisionID,
			DecisionType:   current.DecisionType,
			SubjectID:      current.SubjectID,
			Description:    current.Description,
			ApprovalStatus: req.ApprovalStatus,
			CreatedAt:      current.CreatedAt,
		}
		record := domainrevenue.BuildHumanDecisionGateRecord(decision)
		if err := store.SaveHumanDecisionGateRecord(r.Context(), record); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"decision": decision,
			"record":   record,
			"result": domainrevenue.HumanDecisionGateResult{
				Status:           record.GateStatus,
				RequiresApproval: record.RequiresApproval,
				Reasons:          record.Reasons,
			},
		})
	}
}

func HandleRevenueDailyRoutineReportCreate(store RevenueStore) http.HandlerFunc {
	service := revenueapp.NewDailyRoutineService(store)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "revenue store unavailable", http.StatusServiceUnavailable)
			return
		}
		var req RevenueDailyRoutineRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid revenue daily routine payload", http.StatusBadRequest)
			return
		}
		result, err := service.RunDailyRoutine(r.Context(), revenueapp.DailyRoutineRequest{
			ReportID:     req.ReportID,
			WorkstreamID: req.WorkstreamID,
			Date:         req.Date,
			Limit:        req.Limit,
			Now:          time.Now().UTC(),
		})
		if err != nil {
			if strings.Contains(err.Error(), "limit must be <=") {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if !strings.HasPrefix(err.Error(), "failed to load ") {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"agent":                    result.Agent,
			"mode":                     result.Mode,
			"daily_routine_report":     result.Report,
			"external_actions_applied": result.ExternalActionsApplied,
			"human_approval_required_for_external_actions": result.HumanApprovalRequiredForExternalActions,
		})
	}
}

func HandleRevenueChannelDraftCreate(store RevenueStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var item domainrevenue.ChannelDraft
		if !decodeRevenuePost(w, r, &item, store) {
			return
		}
		if item.ApprovalStatus == "" {
			item.ApprovalStatus = "pending"
		}
		item.ExternalSendApplied = false
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		if err := store.SaveChannelDraft(r.Context(), item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"channel_draft":                                   item,
			"external_actions_applied":                        false,
			"human_approval_required_for_external_send_apply": true,
		})
	}
}

func HandleRevenueExternalSendApply(store RevenueStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req RevenueExternalSendApplyRequest
		if !decodeRevenuePost(w, r, &req, store) {
			return
		}
		if strings.TrimSpace(req.ApplyID) == "" {
			http.Error(w, "apply_id is required", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.DraftID) == "" {
			http.Error(w, "draft_id is required", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.DecisionID) == "" {
			http.Error(w, "decision_id is required", http.StatusBadRequest)
			return
		}
		if !req.HumanApproved {
			http.Error(w, "human_approved is required", http.StatusForbidden)
			return
		}

		drafts, err := store.ListChannelDrafts(r.Context(), 500)
		if err != nil {
			http.Error(w, "failed to load channel drafts", http.StatusInternalServerError)
			return
		}
		var draft *domainrevenue.ChannelDraft
		for i := range drafts {
			if drafts[i].DraftID == req.DraftID {
				draft = &drafts[i]
				break
			}
		}
		if draft == nil {
			http.Error(w, "channel draft not found", http.StatusNotFound)
			return
		}

		decisions, err := store.ListHumanDecisionGateRecords(r.Context(), 500)
		if err != nil {
			http.Error(w, "failed to load human decision gate records", http.StatusInternalServerError)
			return
		}
		var decision *domainrevenue.HumanDecisionGateRecord
		for i := range decisions {
			if decisions[i].DecisionID == req.DecisionID {
				decision = &decisions[i]
				break
			}
		}
		if decision == nil {
			http.Error(w, "human decision gate record not found", http.StatusNotFound)
			return
		}
		if decision.DecisionType != "closed_channel_send" || decision.SubjectID != draft.DraftID || decision.ApprovalStatus != "approved" || decision.GateStatus != "approved" {
			http.Error(w, "approved closed_channel_send decision for draft is required", http.StatusConflict)
			return
		}

		record := domainrevenue.ExternalSendApplyRecord{
			ApplyID:             strings.TrimSpace(req.ApplyID),
			DraftID:             draft.DraftID,
			DecisionID:          decision.DecisionID,
			Channel:             draft.Channel,
			Destination:         strings.TrimSpace(req.Destination),
			ChannelAdapter:      "unconfigured",
			ApprovalStatus:      decision.ApprovalStatus,
			HumanApproved:       req.HumanApproved,
			ApplyStatus:         "blocked",
			SendResult:          "not_sent",
			FailureReason:       "external channel adapter is not configured",
			PostSendVerified:    false,
			ExternalSendApplied: false,
			CreatedAt:           time.Now().UTC(),
		}
		if err := store.SaveExternalSendApplyRecord(r.Context(), record); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"external_send_apply_record":             record,
			"external_actions_applied":               false,
			"post_send_verified":                     false,
			"human_approval_required_for_retry":      true,
			"external_channel_adapter_configuration": "required",
			"failure_reason":                         record.FailureReason,
		})
	}
}

func decodeRevenuePost(w http.ResponseWriter, r *http.Request, out any, store RevenueStore) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if store == nil {
		http.Error(w, "revenue store unavailable", http.StatusServiceUnavailable)
		return false
	}
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		http.Error(w, "invalid revenue payload", http.StatusBadRequest)
		return false
	}
	return true
}
