package storagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
	persistrevenue "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/revenue"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var _ RevenueGroupOwner = (*persistrevenue.SQLiteStore)(nil)

func TestRevenueGroupRegistersCompleteClosedInterface(t *testing.T) {
	f := newRevenueGroupFixture(t, t.TempDir())
	want := map[string]bool{
		revenueOpListMarketResearch: false, revenueOpListSNSMetrics: false, revenueOpListProducts: false,
		revenueOpListCustomerVoices: false, revenueOpListEvents: false, revenueOpListDecisions: false,
		revenueOpListDailyReports: false, revenueOpListChannelDrafts: false, revenueOpListSendRecords: false,
		revenueOpListOpportunities: false, revenueOpListEconomicTasks: false, revenueOpListReflections: false,
		revenueOpListDeliveries: false, revenueOpFindOpportunity: false,
		revenueOpSaveMarketResearch: true, revenueOpSaveSNSMetric: true, revenueOpSaveProduct: true,
		revenueOpSaveCustomerVoice: true, revenueOpSaveRevenueEvent: true, revenueOpSaveOpportunity: true,
		revenueOpSaveEconomicTask: true, revenueOpSaveReflection: true, revenueOpSavePolicyDecision: true,
		revenueOpSaveDailyReport: true, revenueOpSaveChannelDraft: true, revenueOpSaveExternalApply: true,
		revenueOpSaveDelivery: true,
	}
	got := map[string]bool{}
	for _, spec := range f.host.specs {
		if spec.Group == GroupRevenue {
			got[spec.Op] = spec.Mutating
			if spec.Mutating {
				if _, ok := f.host.recoverable[opKey(GroupRevenue, spec.Op)]; !ok {
					t.Errorf("mutation %q is not recoverable", spec.Op)
				}
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("revenue operation contract=%v, want %v", got, want)
	}
}

func TestRevenueClientRoundTripsEverySaveListAndFind(t *testing.T) {
	f := newRevenueGroupFixture(t, t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	market := domainrevenue.MarketResearchItem{ItemID: "market-1", SourcePlatform: "note", Theme: "research", CreatedAt: now}
	metric := domainrevenue.SNSPostMetric{PostID: "post-1", Platform: "x", Impressions: 8, CreatedAt: now}
	product := domainrevenue.Product{ProductID: "product-1", ProductName: "Research worksheet", Status: "draft", CreatedAt: now}
	voice := domainrevenue.CustomerVoice{VoiceID: "voice-1", RawText: "Needs a clearer guide", PermissionStatus: "unknown", CreatedAt: now}
	event := domainrevenue.RevenueEvent{EventID: "event-1", EventType: "purchase", Amount: 1200, CreatedAt: now}
	opportunity := domainrevenue.Opportunity{OpportunityID: "opp-1", SourceKind: "viewer", Title: "Opportunity", ExpectedRevenue: 1200, ExpectedCost: 200, CreatedAt: now}
	opportunity = domainrevenue.NormalizeOpportunityEconomics(opportunity)
	task := domainrevenue.EconomicTask{TaskID: "task-1", OpportunityID: opportunity.OpportunityID, AgentID: "shiro", TaskKind: "draft_report", Status: "draft", CreatedAt: now}
	reflection := domainrevenue.EconomicReflection{ReflectionID: "reflection-1", OpportunityID: opportunity.OpportunityID, Outcome: "produced", Lessons: []string{"reuse the research"}, CreatedAt: now}
	decision := domainrevenue.PolicyDecisionRecord{DecisionID: "decision-1", DecisionType: "high_ticket_offer", Status: "blocked", CreatedAt: now}
	report := domainrevenue.BuildDailyRoutineReport(domainrevenue.DailyRoutineInput{
		ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000003"), Date: "2026-10-04", Now: now,
	})
	draft := domainrevenue.ChannelDraft{
		ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000004"),
		Kind:       modulecore.ArtifactKindDraft, Channel: "email", Subject: "A useful guide", Body: "Draft body", CreatedAt: now,
	}
	draft.ContentHash = domainrevenue.ComputeChannelDraftContentHash(draft)
	apply := domainrevenue.ExternalSendApplyRecord{
		ActionID:   modulecore.ActionID("act_00000000-0000-5000-8000-000000000005"),
		ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000004"),
		DecisionID: decision.DecisionID, Channel: "email", ApplyStatus: "blocked", SendResult: "not_sent",
		FailureReason: "policy blocked", CreatedAt: now,
	}
	delivery := domainrevenue.Delivery{DeliveryID: "delivery-1", TraceID: "trace-1", OpportunityID: opportunity.OpportunityID, DeliveryKind: "handoff", Status: "completed", Target: "internal-review", CreatedAt: now}

	assertRevenueSaveAndList(t, ctx, f.client.SaveMarketResearchItem, f.client.ListMarketResearchItems, "market", market)
	assertRevenueSaveAndList(t, ctx, f.client.SaveSNSPostMetric, f.client.ListSNSPostMetrics, "metric", metric)
	assertRevenueSaveAndList(t, ctx, f.client.SaveProduct, f.client.ListProducts, "product", product)
	assertRevenueSaveAndList(t, ctx, f.client.SaveCustomerVoice, f.client.ListCustomerVoices, "voice", voice)
	assertRevenueSaveAndList(t, ctx, f.client.SaveRevenueEvent, f.client.ListRevenueEvents, "event", event)
	assertRevenueSaveAndList(t, ctx, f.client.SaveOpportunity, f.client.ListOpportunities, "opportunity", opportunity)
	assertRevenueSaveAndList(t, ctx, f.client.SaveEconomicTask, f.client.ListEconomicTasks, "task", task)
	assertRevenueSaveAndList(t, ctx, f.client.SaveEconomicReflection, f.client.ListEconomicReflections, "reflection", reflection)
	assertRevenueSaveAndList(t, ctx, f.client.SavePolicyDecisionRecord, f.client.ListPolicyDecisionRecords, "decision", decision)
	assertRevenueSaveAndList(t, ctx, f.client.SaveDailyRoutineReport, f.client.ListDailyRoutineReports, "report", report)
	assertRevenueSaveAndList(t, ctx, f.client.SaveChannelDraft, f.client.ListChannelDrafts, "draft", draft)
	assertRevenueSaveAndList(t, ctx, f.client.SaveExternalSendApplyRecord, f.client.ListExternalSendApplyRecords, "apply record", apply)
	assertRevenueSaveAndList(t, ctx, f.client.SaveDelivery, f.client.ListDeliveries, "delivery", delivery)

	foundOpportunity, found, err := f.client.FindOpportunityByID(ctx, opportunity.OpportunityID)
	if err != nil || !found || !reflect.DeepEqual(foundOpportunity, domainrevenue.NormalizeOpportunityEconomics(opportunity)) {
		t.Fatalf("FindOpportunityByID()=%#v found=%v err=%v", foundOpportunity, found, err)
	}
	if _, found, err := f.client.FindOpportunityByID(ctx, "missing-opportunity"); err != nil || found {
		t.Fatalf("missing opportunity found=%v err=%v", found, err)
	}
	bounded, err := f.client.ListMarketResearchItems(ctx, 1)
	if err != nil || len(bounded) != 1 {
		t.Fatalf("bounded market list count=%d err=%v", len(bounded), err)
	}
	maximum, err := f.client.ListOpportunities(ctx, 1000)
	if err != nil || len(maximum) != 1 {
		t.Fatalf("maximum compatible opportunity list count=%d err=%v", len(maximum), err)
	}
}

func TestRevenueGroupRejectsUnknownDTOFields(t *testing.T) {
	f := newRevenueGroupFixture(t, t.TempDir())
	ctx := context.Background()
	var listed []domainrevenue.MarketResearchItem
	err := f.rpc.Call(ctx, GroupRevenue, revenueOpListMarketResearch, map[string]any{"limit": 1, "unexpected": true}, &listed)
	if storageHostErrorCode(err) != ErrorCodeSchemaRejected {
		t.Fatalf("unknown list field err=%v, want schema rejection", err)
	}
	badPayload := json.RawMessage(`{"record":{"product_id":"bad-product","product_name":"Good product","status":"draft","created_at":"2026-10-04T12:00:00Z","unexpected":true}}`)
	operation := f.rpc.NewOperation(GroupRevenue, revenueOpSaveProduct, badPayload)
	var saved domainrevenue.Product
	err = f.rpc.Do(ctx, operation, &saved)
	if storageHostErrorCode(err) != ErrorCodeSchemaRejected {
		t.Fatalf("unknown product field err=%v, want schema rejection", err)
	}
	products, err := f.owner.ListProducts(ctx, 10)
	if err != nil || len(products) != 0 {
		t.Fatalf("products after rejected DTO=%#v err=%v; want no owner mutation", products, err)
	}
}

func TestRevenueGroupRejectsOverLimitListRequest(t *testing.T) {
	f := newRevenueGroupFixture(t, t.TempDir())
	var records []domainrevenue.MarketResearchItem
	err := f.rpc.Call(context.Background(), GroupRevenue, revenueOpListMarketResearch, revenueListPayload{Limit: 1001}, &records)
	if storageHostErrorCode(err) != ErrorCodeSchemaRejected {
		t.Fatalf("over-limit list request err=%v, want schema rejection", err)
	}
	if f.counted.lastMarketResearchLimit != 0 {
		t.Fatalf("over-limit request reached owner with limit=%d", f.counted.lastMarketResearchLimit)
	}
	items, err := f.client.ListMarketResearchItems(context.Background(), 1001)
	if storageHostErrorCode(err) != ErrorCodeSchemaRejected || items != nil {
		t.Fatalf("typed client over-limit result=%#v err=%v, want local schema rejection", items, err)
	}
}

func TestRevenueGroupUsesOwnerDefaultListLimit(t *testing.T) {
	f := newRevenueGroupFixture(t, t.TempDir())
	var items []domainrevenue.MarketResearchItem
	if err := f.rpc.Call(context.Background(), GroupRevenue, revenueOpListMarketResearch, revenueListPayload{Limit: 0}, &items); err != nil {
		t.Fatalf("zero-limit list request: %v", err)
	}
	if f.counted.lastMarketResearchLimit != 50 {
		t.Fatalf("owner list limit=%d, want compatibility default 50", f.counted.lastMarketResearchLimit)
	}
}

func TestRevenueGroupRejectsOwnerListOverReturn(t *testing.T) {
	f := newRevenueGroupFixture(t, t.TempDir())
	f.counted.overreturnMarketResearch = true
	items, err := f.client.ListMarketResearchItems(context.Background(), 2)
	if storageHostErrorCode(err) != ErrorCodeStoreUnavailable || items != nil {
		t.Fatalf("owner list over-return items=%#v err=%v; want fail-closed unavailable", items, err)
	}
}

func TestRevenueClientRejectsListBeyondExplicitAndDefaultLimit(t *testing.T) {
	tests := []struct {
		name     string
		limit    int
		rowCount int
	}{
		{name: "explicit", limit: 2, rowCount: 3},
		{name: "default", limit: 0, rowCount: 51},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := make([]domainrevenue.MarketResearchItem, test.rowCount)
			encoded, err := json.Marshal(rows)
			if err != nil {
				t.Fatalf("marshal fixture rows: %v", err)
			}
			response, err := json.Marshal(Response{Result: encoded})
			if err != nil {
				t.Fatalf("marshal fixture response: %v", err)
			}
			client, err := NewClient(ClientConfig{
				Endpoint: "http://storagehost-fixture.invalid", Token: "revenue-list-test-token",
				HTTPClient: &http.Client{Transport: staticResponseRoundTripper(response)},
			})
			if err != nil {
				t.Fatalf("NewClient(): %v", err)
			}
			client.generation = 1
			client.specs[opKey(GroupRevenue, revenueOpListMarketResearch)] = false
			items, err := NewRevenueClient(client).ListMarketResearchItems(context.Background(), test.limit)
			if err == nil || items != nil {
				t.Fatalf("client accepted %d rows for limit %d: items=%d err=%v", test.rowCount, test.limit, len(items), err)
			}
		})
	}
}

func TestRevenueLostResponseReopensOriginalReceiptWithoutRerun(t *testing.T) {
	root := t.TempDir()
	f := newRevenueGroupFixture(t, root)
	opportunity := domainrevenue.Opportunity{OpportunityID: "opp-reopen", SourceKind: "viewer", Title: "original", ExpectedRevenue: 800, ExpectedCost: 100, CreatedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	const opID = "revenue-lost-response-reopen"
	f.host.crashAfterCommitFor = opID
	operation := f.client.NewSaveOpportunityOperation(opID, opportunity)
	var first domainrevenue.Opportunity
	if err := f.rpc.Do(context.Background(), operation, &first); storageHostErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("first call result=%#v err=%v; want outcome_unknown", first, err)
	}
	if got := f.ownerCounter.Load(); got != 1 {
		t.Fatalf("owner save count before reopen=%d, want 1", got)
	}
	updated := opportunity
	updated.Title = "later valid canonical update"
	updated.UpdatedAt = opportunity.CreatedAt.Add(time.Minute)
	if err := f.owner.SaveOpportunity(context.Background(), updated); err != nil {
		t.Fatalf("later valid canonical update failed: %v", err)
	}
	identity, err := revenueOperationIdentity(MutationMetadata{
		OpID: opID, Payload: operation.payload, JournalGeneration: f.host.Generation(), RequestGeneration: f.host.Generation(),
	}, revenueOpSaveOpportunity)
	if err != nil {
		t.Fatalf("derive owner operation identity: %v", err)
	}
	if _, found, err := f.owner.LookupRevenueOpportunityStorageHostReceipt(context.Background(), identity, opportunity); err != nil || !found {
		t.Fatalf("owner receipt before handler reopen found=%v err=%v", found, err)
	}
	f.close()
	f.open(t, true)

	// A process restart recreates the caller-owned operation handle from the
	// durable request ID. Reusing the in-memory handle would use host.result,
	// which is a read-only lookup and cannot invoke owner reconciliation.
	retry := f.client.NewSaveOpportunityOperation(opID, opportunity)
	var recovered domainrevenue.Opportunity
	if err := f.rpc.Do(context.Background(), retry, &recovered); err != nil {
		t.Fatalf("reopened exact operation failed: %v (owner lookup identity=%+v found=%v err=%v)", err, f.counted.lastLookupIdentity, f.counted.lastLookupFound, f.counted.lastLookupError)
	}
	if !f.counted.lastLookupFound || f.counted.lastLookupError != nil {
		t.Fatalf("owner reconciliation did not produce a valid receipt: found=%v err=%v", f.counted.lastLookupFound, f.counted.lastLookupError)
	}
	want := domainrevenue.NormalizeOpportunityEconomics(opportunity)
	if !reflect.DeepEqual(recovered, want) {
		t.Fatalf("recovered result=%#v, want original result %#v", recovered, want)
	}
	if got := f.ownerCounter.Load(); got != 1 {
		t.Fatalf("owner save count after reopen=%d, want no rerun (1 total)", got)
	}
	current, found, err := f.owner.FindOpportunityByID(context.Background(), opportunity.OpportunityID)
	if err != nil || !found || current.Title != updated.Title {
		t.Fatalf("current opportunity=%#v found=%v err=%v; want later valid update", current, found, err)
	}
}

func assertRevenueSaveAndList[T any](
	t *testing.T,
	ctx context.Context,
	save func(context.Context, string, T) (T, error),
	list func(context.Context, int) ([]T, error),
	name string,
	item T,
) {
	t.Helper()
	got, err := save(ctx, "revenue-roundtrip-"+strings.ReplaceAll(name, " ", "_"), item)
	if err != nil || !reflect.DeepEqual(got, item) {
		t.Fatalf("save %s result=%#v err=%v; want exact %#v", name, got, err, item)
	}
	items, err := list(ctx, 10)
	if err != nil || len(items) != 1 || !reflect.DeepEqual(items[0], item) {
		t.Fatalf("list %s items=%#v err=%v; want exact saved record", name, items, err)
	}
}

type revenueGroupFixture struct {
	root         string
	database     string
	owner        *persistrevenue.SQLiteStore
	ownerCounter *atomic.Int32
	counted      *countedRevenueGroupOwner
	host         *Handler
	server       *httptest.Server
	rpc          *Client
	client       *RevenueClient
}

type countedRevenueGroupOwner struct {
	RevenueGroupOwner
	store                    *persistrevenue.SQLiteStore
	calls                    *atomic.Int32
	overreturnMarketResearch bool
	lastMarketResearchLimit  int
	lastLookupIdentity       persistrevenue.RevenueStorageHostOperationIdentity
	lastLookupFound          bool
	lastLookupError          error
}

func (owner *countedRevenueGroupOwner) ListMarketResearchItems(ctx context.Context, limit int) ([]domainrevenue.MarketResearchItem, error) {
	owner.lastMarketResearchLimit = limit
	if owner.overreturnMarketResearch {
		return make([]domainrevenue.MarketResearchItem, limit+1), nil
	}
	return owner.RevenueGroupOwner.ListMarketResearchItems(ctx, limit)
}

func (owner *countedRevenueGroupOwner) SaveOpportunityForStorageHostOperation(ctx context.Context, identity persistrevenue.RevenueStorageHostOperationIdentity, item domainrevenue.Opportunity) (domainrevenue.Opportunity, error) {
	owner.calls.Add(1)
	return owner.store.SaveOpportunityForStorageHostOperation(ctx, identity, item)
}

func (owner *countedRevenueGroupOwner) LookupRevenueOpportunityStorageHostReceipt(ctx context.Context, identity persistrevenue.RevenueStorageHostOperationIdentity, expected domainrevenue.Opportunity) (json.RawMessage, bool, error) {
	result, found, err := owner.store.LookupRevenueOpportunityStorageHostReceipt(ctx, identity, expected)
	owner.lastLookupIdentity = identity
	owner.lastLookupFound = found
	owner.lastLookupError = err
	return result, found, err
}

func newRevenueGroupFixture(t *testing.T, root string) *revenueGroupFixture {
	t.Helper()
	f := &revenueGroupFixture{root: root, database: filepath.Join(root, "revenue.sqlite"), ownerCounter: &atomic.Int32{}}
	f.open(t, false)
	t.Cleanup(f.close)
	return f
}

func (f *revenueGroupFixture) open(t *testing.T, reuseClient bool) {
	t.Helper()
	var err error
	f.owner, err = persistrevenue.NewSQLiteStore(f.database)
	if err != nil {
		t.Fatalf("open Revenue owner: %v", err)
	}
	f.host, err = NewHandler(HandlerConfig{Token: "revenue-test-token", JournalDir: filepath.Join(f.root, "journal")})
	if err != nil {
		_ = f.owner.Close()
		t.Fatalf("open Revenue storage host: %v", err)
	}
	f.counted = &countedRevenueGroupOwner{RevenueGroupOwner: f.owner, store: f.owner, calls: f.ownerCounter}
	if err := RegisterRevenueGroup(f.host, f.counted); err != nil {
		_ = f.host.Close()
		_ = f.owner.Close()
		t.Fatalf("register Revenue group: %v", err)
	}
	f.server = httptest.NewServer(f.host)
	if reuseClient {
		f.rpc.endpoint = f.server.URL + RPCPath
		f.rpc.httpClient = f.server.Client()
	} else {
		f.rpc, err = NewClient(ClientConfig{Endpoint: f.server.URL, Token: "revenue-test-token", HTTPClient: f.server.Client()})
		if err != nil {
			t.Fatalf("open Revenue storage host client: %v", err)
		}
	}
	if err := f.rpc.Handshake(context.Background()); err != nil {
		t.Fatalf("Revenue storage host handshake: %v", err)
	}
	f.client = NewRevenueClient(f.rpc)
}

func (f *revenueGroupFixture) close() {
	if f.server != nil {
		f.server.Close()
		f.server = nil
	}
	if f.host != nil {
		_ = f.host.Close()
		f.host = nil
	}
	if f.owner != nil {
		_ = f.owner.Close()
		f.owner = nil
	}
}

type staticResponseRoundTripper []byte

func (response staticResponseRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	body := append([]byte(nil), response...)
	return &http.Response{
		Status: "200 OK", StatusCode: http.StatusOK, Header: make(http.Header),
		Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: request,
	}, nil
}
