package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

type stubRevenueGoalStore struct{ goals []domainworkstream.Goal }

func (s *stubRevenueGoalStore) SaveGoal(_ context.Context, item domainworkstream.Goal) error {
	if err := domainworkstream.ValidateGoal(item); err != nil {
		return err
	}
	s.goals = append(s.goals, item)
	return nil
}

type stubRevenueStore struct {
	market        []domainrevenue.MarketResearchItem
	posts         []domainrevenue.SNSPostMetric
	products      []domainrevenue.Product
	voices        []domainrevenue.CustomerVoice
	events        []domainrevenue.RevenueEvent
	decisions     []domainrevenue.HumanDecisionGateRecord
	daily         []domainrevenue.DailyRoutineReport
	drafts        []domainrevenue.ChannelDraft
	applies       []domainrevenue.ExternalSendApplyRecord
	opportunities []domainrevenue.Opportunity
	economicTasks []domainrevenue.EconomicTask
	reflections   []domainrevenue.EconomicReflection
	limit         int
}

func (s *stubRevenueStore) ListOpportunities(_ context.Context, limit int) ([]domainrevenue.Opportunity, error) {
	s.limit = limit
	return s.opportunities, nil
}

func (s *stubRevenueStore) ListEconomicTasks(_ context.Context, limit int) ([]domainrevenue.EconomicTask, error) {
	s.limit = limit
	return s.economicTasks, nil
}

func (s *stubRevenueStore) ListEconomicReflections(_ context.Context, limit int) ([]domainrevenue.EconomicReflection, error) {
	s.limit = limit
	return s.reflections, nil
}

func (s *stubRevenueStore) ListMarketResearchItems(_ context.Context, limit int) ([]domainrevenue.MarketResearchItem, error) {
	s.limit = limit
	return s.market, nil
}

func (s *stubRevenueStore) ListSNSPostMetrics(_ context.Context, limit int) ([]domainrevenue.SNSPostMetric, error) {
	s.limit = limit
	return s.posts, nil
}

func (s *stubRevenueStore) ListProducts(_ context.Context, limit int) ([]domainrevenue.Product, error) {
	s.limit = limit
	return s.products, nil
}

func (s *stubRevenueStore) ListCustomerVoices(_ context.Context, limit int) ([]domainrevenue.CustomerVoice, error) {
	s.limit = limit
	return s.voices, nil
}

func (s *stubRevenueStore) ListRevenueEvents(_ context.Context, limit int) ([]domainrevenue.RevenueEvent, error) {
	s.limit = limit
	return s.events, nil
}

func (s *stubRevenueStore) ListHumanDecisionGateRecords(_ context.Context, limit int) ([]domainrevenue.HumanDecisionGateRecord, error) {
	s.limit = limit
	return s.decisions, nil
}

func (s *stubRevenueStore) ListDailyRoutineReports(_ context.Context, limit int) ([]domainrevenue.DailyRoutineReport, error) {
	s.limit = limit
	return s.daily, nil
}

func (s *stubRevenueStore) ListChannelDrafts(_ context.Context, limit int) ([]domainrevenue.ChannelDraft, error) {
	s.limit = limit
	return s.drafts, nil
}

func (s *stubRevenueStore) ListExternalSendApplyRecords(_ context.Context, limit int) ([]domainrevenue.ExternalSendApplyRecord, error) {
	s.limit = limit
	return s.applies, nil
}

func (s *stubRevenueStore) SaveMarketResearchItem(_ context.Context, item domainrevenue.MarketResearchItem) error {
	if err := domainrevenue.ValidateMarketResearchItem(item); err != nil {
		return err
	}
	s.market = append(s.market, item)
	return nil
}

func (s *stubRevenueStore) SaveSNSPostMetric(_ context.Context, item domainrevenue.SNSPostMetric) error {
	if err := domainrevenue.ValidateSNSPostMetric(item); err != nil {
		return err
	}
	s.posts = append(s.posts, item)
	return nil
}

func (s *stubRevenueStore) SaveProduct(_ context.Context, item domainrevenue.Product) error {
	if err := domainrevenue.ValidateProduct(item); err != nil {
		return err
	}
	s.products = append(s.products, item)
	return nil
}

func (s *stubRevenueStore) SaveCustomerVoice(_ context.Context, item domainrevenue.CustomerVoice) error {
	if err := domainrevenue.ValidateCustomerVoice(item); err != nil {
		return err
	}
	s.voices = append(s.voices, item)
	return nil
}

func (s *stubRevenueStore) SaveRevenueEvent(_ context.Context, item domainrevenue.RevenueEvent) error {
	if err := domainrevenue.ValidateRevenueEvent(item); err != nil {
		return err
	}
	s.events = append(s.events, item)
	return nil
}

func (s *stubRevenueStore) SaveHumanDecisionGateRecord(_ context.Context, item domainrevenue.HumanDecisionGateRecord) error {
	if err := domainrevenue.ValidateHumanDecisionGateRecord(item); err != nil {
		return err
	}
	s.decisions = append(s.decisions, item)
	return nil
}

func (s *stubRevenueStore) SaveDailyRoutineReport(_ context.Context, item domainrevenue.DailyRoutineReport) error {
	if err := domainrevenue.ValidateDailyRoutineReport(item); err != nil {
		return err
	}
	s.daily = append(s.daily, item)
	return nil
}

func (s *stubRevenueStore) SaveChannelDraft(_ context.Context, item domainrevenue.ChannelDraft) error {
	if err := domainrevenue.ValidateChannelDraft(item); err != nil {
		return err
	}
	s.drafts = append(s.drafts, item)
	return nil
}

func (s *stubRevenueStore) SaveExternalSendApplyRecord(_ context.Context, item domainrevenue.ExternalSendApplyRecord) error {
	if err := domainrevenue.ValidateExternalSendApplyRecord(item); err != nil {
		return err
	}
	s.applies = append(s.applies, item)
	return nil
}

func (s *stubRevenueStore) SaveOpportunity(_ context.Context, item domainrevenue.Opportunity) error {
	item = domainrevenue.NormalizeOpportunityEconomics(item)
	if err := domainrevenue.ValidateOpportunity(item); err != nil {
		return err
	}
	s.opportunities = append(s.opportunities, item)
	return nil
}

func (s *stubRevenueStore) SaveEconomicTask(_ context.Context, item domainrevenue.EconomicTask) error {
	if err := domainrevenue.ValidateEconomicTask(item); err != nil {
		return err
	}
	s.economicTasks = append(s.economicTasks, item)
	return nil
}

func (s *stubRevenueStore) SaveEconomicReflection(_ context.Context, item domainrevenue.EconomicReflection) error {
	if err := domainrevenue.ValidateEconomicReflection(item); err != nil {
		return err
	}
	s.reflections = append(s.reflections, item)
	return nil
}

func TestHandleRevenueStatus(t *testing.T) {
	day := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubRevenueStore{
		market:        []domainrevenue.MarketResearchItem{{ItemID: "mkt_1", SourcePlatform: "note"}},
		posts:         []domainrevenue.SNSPostMetric{{PostID: "post_1", Platform: "x", PostedAt: day}},
		products:      []domainrevenue.Product{{ProductID: "prod_1", ProductName: "商品設計シート", Status: "active"}},
		voices:        []domainrevenue.CustomerVoice{{VoiceID: "voice_1", VoiceType: "confusion", RawText: "good", UsableForMarketing: true, CreatedAt: day}},
		events:        []domainrevenue.RevenueEvent{{EventID: "rev_1", EventType: "purchase", ProductID: "prod_1", Amount: 980, CustomerID: "cust_1", CreatedAt: day}},
		decisions:     []domainrevenue.HumanDecisionGateRecord{{DecisionID: "dec_1", DecisionType: "external_publish", ApprovalStatus: "pending", GateStatus: "needs_review"}},
		daily:         []domainrevenue.DailyRoutineReport{{ReportID: "daily_1", Date: "2026-05-18", Status: "draft_report"}},
		drafts:        []domainrevenue.ChannelDraft{{DraftID: "draft_1", Channel: "email", Body: "本文", ApprovalStatus: "pending", CreatedAt: day}},
		applies:       []domainrevenue.ExternalSendApplyRecord{{ApplyID: "apply_1", DraftID: "draft_1", DecisionID: "dec_2", Channel: "email", ApprovalStatus: "approved", HumanApproved: true, ApplyStatus: "blocked", SendResult: "not_sent", FailureReason: "external channel adapter is not configured", CreatedAt: day}},
		opportunities: []domainrevenue.Opportunity{{OpportunityID: "opp_1", SourceKind: "market_research", Title: "Draft opportunity", ApprovalState: "draft", CreatedAt: day}},
		economicTasks: []domainrevenue.EconomicTask{{TaskID: "task_1", OpportunityID: "opp_1", AgentID: "shiro", TaskKind: "billing", Status: "draft", ApprovalMode: "human_required", CreatedAt: day}},
		reflections:   []domainrevenue.EconomicReflection{{ReflectionID: "reflection_1", OpportunityID: "opp_1", Outcome: "drafted", CreatedAt: day}},
	}
	req := httptest.NewRequest(http.MethodGet, "/viewer/revenue?limit=5", nil)
	rec := httptest.NewRecorder()

	HandleRevenueStatus(store, RevenueEconomicObjectiveSettings{Enabled: true, DraftOnly: true}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if store.limit != 5 {
		t.Fatalf("limit=%d", store.limit)
	}
	var body struct {
		MarketResearch                       []domainrevenue.MarketResearchItem      `json:"market_research"`
		Products                             []domainrevenue.Product                 `json:"products"`
		Voices                               []domainrevenue.CustomerVoice           `json:"customer_voices"`
		Events                               []domainrevenue.RevenueEvent            `json:"revenue_events"`
		Decisions                            []domainrevenue.HumanDecisionGateRecord `json:"human_decisions"`
		DailyReports                         []domainrevenue.DailyRoutineReport      `json:"daily_routine_reports"`
		ChannelDrafts                        []domainrevenue.ChannelDraft            `json:"channel_drafts"`
		Applies                              []domainrevenue.ExternalSendApplyRecord `json:"external_send_apply_records"`
		ExternalChannelAdapter               string                                  `json:"external_channel_adapter"`
		ExternalChannelAdapterConfigured     bool                                    `json:"external_channel_adapter_configured"`
		HumanApprovalRequiredForExternalSend bool                                    `json:"human_approval_required_for_external_send"`
		Summary                              RevenueDashboardSummary                 `json:"summary"`
		EconomicObjective                    RevenueEconomicObjectiveSummary         `json:"economic_objective"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.MarketResearch) != 1 || body.MarketResearch[0].ItemID != "mkt_1" {
		t.Fatalf("market research=%#v", body.MarketResearch)
	}
	if body.Products == nil || body.Voices == nil || body.Events == nil || body.Decisions == nil {
		t.Fatalf("expected response arrays: %#v", body)
	}
	if len(body.DailyReports) != 1 || body.DailyReports[0].ReportID != "daily_1" {
		t.Fatalf("daily reports=%#v", body.DailyReports)
	}
	if len(body.ChannelDrafts) != 1 || body.ChannelDrafts[0].DraftID != "draft_1" {
		t.Fatalf("channel drafts=%#v", body.ChannelDrafts)
	}
	if len(body.Applies) != 1 || body.Applies[0].ApplyID != "apply_1" {
		t.Fatalf("external send applies=%#v", body.Applies)
	}
	if body.ExternalChannelAdapter != "unconfigured" || body.ExternalChannelAdapterConfigured || !body.HumanApprovalRequiredForExternalSend {
		t.Fatalf("external channel readiness=%#v", body)
	}
	if body.Summary.MarketResearchCount != 1 ||
		body.Summary.SNSPostCount != 1 ||
		body.Summary.UsableVoiceCount != 1 ||
		body.Summary.PurchaseCount != 1 ||
		body.Summary.TotalRevenueAmount != 980 ||
		body.Summary.PaidCustomerCount != 1 ||
		body.Summary.PendingDecisionCount != 1 ||
		body.Summary.LatestDailyReportID != "daily_1" ||
		body.Summary.ChannelDraftCount != 1 ||
		body.Summary.LatestChannelDraftID != "draft_1" ||
		body.Summary.ExternalSendApplyCount != 1 {
		t.Fatalf("summary=%#v", body.Summary)
	}
	if !body.EconomicObjective.Enabled || !body.EconomicObjective.DraftOnly || !body.EconomicObjective.ExternalActionBlocked ||
		body.EconomicObjective.OpportunityCount != 1 || body.EconomicObjective.PendingApprovalTaskCount != 1 || body.EconomicObjective.ReflectionCount != 1 {
		t.Fatalf("economic objective summary=%#v", body.EconomicObjective)
	}
	if len(body.Summary.KPITrend) != 1 ||
		body.Summary.KPITrend[0].Date != "2026-05-18" ||
		body.Summary.KPITrend[0].RevenueAmount != 980 ||
		body.Summary.KPITrend[0].SNSPostCount != 1 ||
		body.Summary.KPITrend[0].VoiceCount != 1 {
		t.Fatalf("kpi trend=%#v", body.Summary.KPITrend)
	}
	if len(body.Summary.ProductSales) != 1 ||
		body.Summary.ProductSales[0].ProductID != "prod_1" ||
		body.Summary.ProductSales[0].ProductName != "商品設計シート" ||
		body.Summary.ProductSales[0].RevenueAmount != 980 ||
		body.Summary.ProductSales[0].SalesCount != 1 {
		t.Fatalf("product sales=%#v", body.Summary.ProductSales)
	}
	if len(body.Summary.CustomerVoiceTypes) != 1 ||
		body.Summary.CustomerVoiceTypes[0].VoiceType != "confusion" ||
		body.Summary.CustomerVoiceTypes[0].Count != 1 {
		t.Fatalf("voice types=%#v", body.Summary.CustomerVoiceTypes)
	}
}

func TestHandleRevenueCreateEndpoints(t *testing.T) {
	store := &stubRevenueStore{}
	cases := []struct {
		name    string
		handler http.HandlerFunc
		path    string
		body    string
		assert  func(*testing.T, *stubRevenueStore)
	}{
		{
			name:    "market",
			handler: HandleRevenueMarketResearchCreate(store),
			path:    "/viewer/revenue/market-research",
			body:    `{"item_id":"mkt_1","source_platform":"note"}`,
			assert: func(t *testing.T, store *stubRevenueStore) {
				if len(store.market) != 1 {
					t.Fatalf("market=%#v", store.market)
				}
			},
		},
		{
			name:    "product",
			handler: HandleRevenueProductCreate(store),
			path:    "/viewer/revenue/products",
			body:    `{"product_id":"prod_1","product_name":"商品設計シート"}`,
			assert: func(t *testing.T, store *stubRevenueStore) {
				if len(store.products) != 1 || store.products[0].Status != "draft" {
					t.Fatalf("products=%#v", store.products)
				}
			},
		},
		{
			name:    "voice",
			handler: HandleRevenueCustomerVoiceCreate(store),
			path:    "/viewer/revenue/customer-voices",
			body:    `{"voice_id":"voice_1","raw_text":"ここがわからない"}`,
			assert: func(t *testing.T, store *stubRevenueStore) {
				if len(store.voices) != 1 || store.voices[0].PermissionStatus != "unknown" {
					t.Fatalf("voices=%#v", store.voices)
				}
			},
		},
		{
			name:    "event",
			handler: HandleRevenueEventCreate(store),
			path:    "/viewer/revenue/events",
			body:    `{"event_id":"rev_1","event_type":"purchase","amount":980}`,
			assert: func(t *testing.T, store *stubRevenueStore) {
				if len(store.events) != 1 {
					t.Fatalf("events=%#v", store.events)
				}
			},
		},
		{
			name:    "sns",
			handler: HandleRevenueSNSPostMetricCreate(store),
			path:    "/viewer/revenue/sns-posts",
			body:    `{"post_id":"post_1","platform":"x","impressions":10}`,
			assert: func(t *testing.T, store *stubRevenueStore) {
				if len(store.posts) != 1 {
					t.Fatalf("posts=%#v", store.posts)
				}
			},
		},
		{
			name:    "channel draft",
			handler: HandleRevenueChannelDraftCreate(store),
			path:    "/viewer/revenue/channel-drafts",
			body:    `{"draft_id":"draft_1","channel":"email","subject":"案内","body":"下書き本文"}`,
			assert: func(t *testing.T, store *stubRevenueStore) {
				if len(store.drafts) != 1 || store.drafts[0].ApprovalStatus != "pending" || store.drafts[0].ExternalSendApplied {
					t.Fatalf("drafts=%#v", store.drafts)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(tc.body))
			rec := httptest.NewRecorder()
			tc.handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			tc.assert(t, store)
		})
	}
}

func TestHandleRevenueProductCreateRejectsSuccessGuarantee(t *testing.T) {
	store := &stubRevenueStore{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/revenue/products", bytes.NewBufferString(`{
		"product_id":"prod_1",
		"product_name":"商品設計シート",
		"promise":"必ず稼げる"
	}`))
	rec := httptest.NewRecorder()

	HandleRevenueProductCreate(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleRevenueEconomicObjectiveEndpoints(t *testing.T) {
	store := &stubRevenueStore{}
	oppReq := httptest.NewRequest(http.MethodPost, "/viewer/revenue/opportunities", bytes.NewBufferString(`{
		"opportunity_id":"opp-1","source_kind":"note_archive","title":"ローカルLLM技術資料","expected_revenue":3000,"expected_cost":800,"approval_state":"draft"
	}`))
	oppRec := httptest.NewRecorder()
	HandleRevenueOpportunities(store).ServeHTTP(oppRec, oppReq)
	if oppRec.Code != http.StatusCreated || len(store.opportunities) != 1 || store.opportunities[0].ExpectedProfit != 2200 {
		t.Fatalf("opportunity status=%d body=%s items=%#v", oppRec.Code, oppRec.Body.String(), store.opportunities)
	}

	taskReq := httptest.NewRequest(http.MethodPost, "/viewer/revenue/economic-tasks", bytes.NewBufferString(`{
		"task_id":"task-1","opportunity_id":"opp-1","agent_id":"shiro","task_kind":"billing","status":"draft","approval_mode":"none"
	}`))
	taskRec := httptest.NewRecorder()
	HandleRevenueEconomicTasks(store).ServeHTTP(taskRec, taskReq)
	if taskRec.Code != http.StatusBadRequest || len(store.economicTasks) != 0 {
		t.Fatalf("approval mismatch status=%d body=%s tasks=%#v", taskRec.Code, taskRec.Body.String(), store.economicTasks)
	}

	store.events = []domainrevenue.RevenueEvent{{EventID: "rev-1", EventType: "sold", Amount: 3000, CreatedAt: time.Now().UTC()}}
	reflectionReq := httptest.NewRequest(http.MethodPost, "/viewer/revenue/economic-reflections/from-revenue-event", bytes.NewBufferString(`{
		"reflection_id":"reflection-1","opportunity_id":"opp-1","revenue_event_id":"rev-1","outcome":"sold","lessons":["再利用価値が高い"]
	}`))
	reflectionRec := httptest.NewRecorder()
	HandleRevenueReflectionFromEvent(store).ServeHTTP(reflectionRec, reflectionReq)
	if reflectionRec.Code != http.StatusCreated || len(store.reflections) != 1 || store.reflections[0].NetProfit != 2200 {
		t.Fatalf("reflection status=%d body=%s items=%#v", reflectionRec.Code, reflectionRec.Body.String(), store.reflections)
	}

	goals := &stubRevenueGoalStore{}
	goalReq := httptest.NewRequest(http.MethodPost, "/viewer/revenue/opportunities/workstream-goal", bytes.NewBufferString(`{"opportunity_id":"opp-1","workstream_id":"ws-revenue"}`))
	goalRec := httptest.NewRecorder()
	HandleRevenueOpportunityWorkstreamGoal(store, goals).ServeHTTP(goalRec, goalReq)
	if goalRec.Code != http.StatusCreated || len(goals.goals) != 1 || goals.goals[0].Status != domainworkstream.StatusDraft {
		t.Fatalf("goal status=%d body=%s goals=%#v", goalRec.Code, goalRec.Body.String(), goals.goals)
	}
	missingReq := httptest.NewRequest(http.MethodPost, "/viewer/revenue/opportunities/workstream-goal", bytes.NewBufferString(`{"opportunity_id":"missing","workstream_id":"ws-revenue"}`))
	missingRec := httptest.NewRecorder()
	HandleRevenueOpportunityWorkstreamGoal(store, goals).ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", missingRec.Code, missingRec.Body.String())
	}
}

func TestHandleRevenueEconomicObjectiveReadEndpointsReturnUnavailableWarnings(t *testing.T) {
	cases := []struct {
		path    string
		handler http.HandlerFunc
	}{
		{path: "/viewer/revenue/opportunities", handler: HandleRevenueOpportunities(nil)},
		{path: "/viewer/revenue/economic-tasks", handler: HandleRevenueEconomicTasks(nil)},
		{path: "/viewer/revenue/economic-reflections", handler: HandleRevenueEconomicReflections(nil)},
	}
	for _, tt := range cases {
		t.Run(tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tt.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"status":"unavailable"`)) || !bytes.Contains(rec.Body.Bytes(), []byte(`"warnings"`)) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleRevenueHumanDecisionGateNeedsReview(t *testing.T) {
	store := &stubRevenueStore{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/revenue/human-decision-gate", bytes.NewBufferString(`{
		"decision_id":"dec_1",
		"decision_type":"high_ticket_offer",
		"description":"30万円の導入支援を案内する"
	}`))
	rec := httptest.NewRecorder()

	HandleRevenueHumanDecisionGate(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Result domainrevenue.HumanDecisionGateResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Result.Status != "needs_review" || !body.Result.RequiresApproval {
		t.Fatalf("unexpected result: %#v", body.Result)
	}
	if len(store.decisions) != 1 || store.decisions[0].ApprovalStatus != "pending" {
		t.Fatalf("decisions=%#v", store.decisions)
	}
}

func TestHandleRevenueHumanDecisionGateBlocksProhibitedClaim(t *testing.T) {
	store := &stubRevenueStore{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/revenue/human-decision-gate", bytes.NewBufferString(`{
		"decision_id":"dec_1",
		"decision_type":"external_publish",
		"description":"必ず稼げますと投稿する",
		"approval_status":"approved"
	}`))
	rec := httptest.NewRecorder()

	HandleRevenueHumanDecisionGate(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Result domainrevenue.HumanDecisionGateResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Result.Status != "blocked" {
		t.Fatalf("unexpected result: %#v", body.Result)
	}
}

func TestHandleRevenueHumanDecisionGateRejectsMissingDecisionID(t *testing.T) {
	store := &stubRevenueStore{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/revenue/human-decision-gate", bytes.NewBufferString(`{
		"decision_type":"high_ticket_offer",
		"description":"30万円の導入支援を案内する"
	}`))
	rec := httptest.NewRecorder()

	HandleRevenueHumanDecisionGate(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleRevenueHumanDecisionGateReviewApprovesExistingDecision(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 30, 0, 0, time.UTC)
	store := &stubRevenueStore{
		decisions: []domainrevenue.HumanDecisionGateRecord{{
			DecisionID:       "dec_1",
			DecisionType:     "high_ticket_offer",
			Description:      "30万円の導入支援を案内する",
			ApprovalStatus:   "pending",
			GateStatus:       "needs_review",
			RequiresApproval: true,
			CreatedAt:        now,
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/revenue/human-decision-gate/review", bytes.NewBufferString(`{
		"decision_id":"dec_1",
		"approval_status":"approved"
	}`))
	rec := httptest.NewRecorder()

	HandleRevenueHumanDecisionGateReview(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Record domainrevenue.HumanDecisionGateRecord `json:"record"`
		Result domainrevenue.HumanDecisionGateResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Record.DecisionID != "dec_1" || body.Record.ApprovalStatus != "approved" || body.Result.Status != "approved" {
		t.Fatalf("unexpected review response: %#v", body)
	}
	if len(store.decisions) != 2 || store.decisions[1].ApprovalStatus != "approved" {
		t.Fatalf("decisions=%#v", store.decisions)
	}
}

func TestHandleRevenueHumanDecisionGateReviewRejectsExistingDecision(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 30, 0, 0, time.UTC)
	store := &stubRevenueStore{
		decisions: []domainrevenue.HumanDecisionGateRecord{{
			DecisionID:       "dec_1",
			DecisionType:     "external_publish",
			Description:      "投稿下書きを公開する",
			ApprovalStatus:   "pending",
			GateStatus:       "needs_review",
			RequiresApproval: true,
			CreatedAt:        now,
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/revenue/human-decision-gate/review", bytes.NewBufferString(`{
		"decision_id":"dec_1",
		"approval_status":"rejected"
	}`))
	rec := httptest.NewRecorder()

	HandleRevenueHumanDecisionGateReview(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Result domainrevenue.HumanDecisionGateResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Result.Status != "blocked" || len(body.Result.Reasons) == 0 {
		t.Fatalf("unexpected result: %#v", body.Result)
	}
	if len(store.decisions) != 2 || store.decisions[1].ApprovalStatus != "rejected" {
		t.Fatalf("decisions=%#v", store.decisions)
	}
}

func TestHandleRevenueDailyRoutineReportCreatesDraftOnlyReport(t *testing.T) {
	store := &stubRevenueStore{
		market: []domainrevenue.MarketResearchItem{{ItemID: "mkt_1", SourcePlatform: "note"}},
		posts:  []domainrevenue.SNSPostMetric{{PostID: "post_1", Platform: "x"}},
		products: []domainrevenue.Product{{
			ProductID:   "prod_1",
			ProductName: "商品設計シート",
			Status:      "draft",
		}},
		voices: []domainrevenue.CustomerVoice{{VoiceID: "voice_1", RawText: "ここがわからない", PermissionStatus: "unknown"}},
		events: []domainrevenue.RevenueEvent{{EventID: "rev_1", EventType: "purchase", Amount: 980, CustomerID: "cust_1"}},
		decisions: []domainrevenue.HumanDecisionGateRecord{{
			DecisionID:       "dec_1",
			DecisionType:     "external_publish",
			ApprovalStatus:   "pending",
			GateStatus:       "needs_review",
			RequiresApproval: true,
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/revenue/daily-routine", bytes.NewBufferString(`{
		"report_id":"daily_1",
		"workstream_id":"ws_revenue",
		"date":"2026-05-18",
		"limit":20
	}`))
	rec := httptest.NewRecorder()

	HandleRevenueDailyRoutineReportCreate(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.daily) != 1 {
		t.Fatalf("daily=%#v", store.daily)
	}
	report := store.daily[0]
	if report.Status != "draft_report" || report.ExternalSendApplied {
		t.Fatalf("expected draft-only report: %#v", report)
	}
	if report.MarketResearch != 1 || report.SNSPosts != 1 || report.Products != 1 || report.CustomerVoices != 1 || report.RevenueEvents != 1 || report.PaidCustomers != 1 || report.PendingDecisions != 1 {
		t.Fatalf("unexpected counts: %#v", report)
	}
	var body struct {
		Report                                  domainrevenue.DailyRoutineReport `json:"daily_routine_report"`
		Agent                                   string                           `json:"agent"`
		Mode                                    string                           `json:"mode"`
		ExternalActionsApplied                  bool                             `json:"external_actions_applied"`
		HumanApprovalRequiredForExternalActions bool                             `json:"human_approval_required_for_external_actions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Agent != "RevenueAgent" || body.Mode != "draft_report_only" || body.Report.ReportID != "daily_1" || body.ExternalActionsApplied || !body.HumanApprovalRequiredForExternalActions {
		t.Fatalf("unexpected response: %#v", body)
	}
}

func TestHandleRevenueHumanDecisionGateReviewRejectsInvalidApprovalStatus(t *testing.T) {
	store := &stubRevenueStore{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/revenue/human-decision-gate/review", bytes.NewBufferString(`{
		"decision_id":"dec_1",
		"approval_status":"pending"
	}`))
	rec := httptest.NewRecorder()

	HandleRevenueHumanDecisionGateReview(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleRevenueExternalSendApplyRequiresHumanApproval(t *testing.T) {
	store := &stubRevenueStore{
		drafts: []domainrevenue.ChannelDraft{{DraftID: "draft_1", Channel: "email", Body: "下書き本文", ApprovalStatus: "pending"}},
		decisions: []domainrevenue.HumanDecisionGateRecord{{
			DecisionID:       "dec_1",
			DecisionType:     "closed_channel_send",
			SubjectID:        "draft_1",
			ApprovalStatus:   "approved",
			GateStatus:       "approved",
			RequiresApproval: true,
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/revenue/channel-drafts/external-send-apply", bytes.NewBufferString(`{
		"apply_id":"apply_1",
		"draft_id":"draft_1",
		"decision_id":"dec_1"
	}`))
	rec := httptest.NewRecorder()

	HandleRevenueExternalSendApply(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.applies) != 0 {
		t.Fatalf("applies=%#v", store.applies)
	}
}

func TestHandleRevenueExternalSendApplyRecordsBlockedAuditWhenAdapterUnavailable(t *testing.T) {
	store := &stubRevenueStore{
		drafts: []domainrevenue.ChannelDraft{{DraftID: "draft_1", Channel: "email", Body: "下書き本文", ApprovalStatus: "pending"}},
		decisions: []domainrevenue.HumanDecisionGateRecord{{
			DecisionID:       "dec_1",
			DecisionType:     "closed_channel_send",
			SubjectID:        "draft_1",
			ApprovalStatus:   "approved",
			GateStatus:       "approved",
			RequiresApproval: true,
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/revenue/channel-drafts/external-send-apply", bytes.NewBufferString(`{
		"apply_id":"apply_1",
		"draft_id":"draft_1",
		"decision_id":"dec_1",
		"destination":"customer@example.test",
		"channel_adapter":"slack",
		"human_approved":true
	}`))
	rec := httptest.NewRecorder()

	HandleRevenueExternalSendApply(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.applies) != 1 {
		t.Fatalf("applies=%#v", store.applies)
	}
	record := store.applies[0]
	if record.ApplyStatus != "blocked" || record.SendResult != "not_sent" || record.ExternalSendApplied || record.PostSendVerified || record.ChannelAdapter != "unconfigured" {
		t.Fatalf("unexpected apply record: %#v", record)
	}
	var body struct {
		Record                 domainrevenue.ExternalSendApplyRecord `json:"external_send_apply_record"`
		ExternalActionsApplied bool                                  `json:"external_actions_applied"`
		PostSendVerified       bool                                  `json:"post_send_verified"`
		FailureReason          string                                `json:"failure_reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Record.ApplyID != "apply_1" || body.ExternalActionsApplied || body.PostSendVerified || body.FailureReason == "" {
		t.Fatalf("unexpected response: %#v", body)
	}
}

func TestHandleRevenueExternalSendApplyRejectsUnapprovedDecision(t *testing.T) {
	store := &stubRevenueStore{
		drafts: []domainrevenue.ChannelDraft{{DraftID: "draft_1", Channel: "email", Body: "下書き本文", ApprovalStatus: "pending"}},
		decisions: []domainrevenue.HumanDecisionGateRecord{{
			DecisionID:       "dec_1",
			DecisionType:     "closed_channel_send",
			SubjectID:        "draft_1",
			ApprovalStatus:   "pending",
			GateStatus:       "needs_review",
			RequiresApproval: true,
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/revenue/channel-drafts/external-send-apply", bytes.NewBufferString(`{
		"apply_id":"apply_1",
		"draft_id":"draft_1",
		"decision_id":"dec_1",
		"human_approved":true
	}`))
	rec := httptest.NewRecorder()

	HandleRevenueExternalSendApply(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.applies) != 0 {
		t.Fatalf("applies=%#v", store.applies)
	}
}

func TestHandleRevenueExternalSendApplyRejectsDecisionSubjectMismatch(t *testing.T) {
	store := &stubRevenueStore{
		drafts: []domainrevenue.ChannelDraft{{DraftID: "draft_1", Channel: "email", Body: "下書き本文", ApprovalStatus: "pending"}},
		decisions: []domainrevenue.HumanDecisionGateRecord{{
			DecisionID:       "dec_1",
			DecisionType:     "closed_channel_send",
			SubjectID:        "draft_2",
			ApprovalStatus:   "approved",
			GateStatus:       "approved",
			RequiresApproval: true,
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/revenue/channel-drafts/external-send-apply", bytes.NewBufferString(`{
		"apply_id":"apply_1",
		"draft_id":"draft_1",
		"decision_id":"dec_1",
		"human_approved":true
	}`))
	rec := httptest.NewRecorder()

	HandleRevenueExternalSendApply(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.applies) != 0 {
		t.Fatalf("applies=%#v", store.applies)
	}
}
