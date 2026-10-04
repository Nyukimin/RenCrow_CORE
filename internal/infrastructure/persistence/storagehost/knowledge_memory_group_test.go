package storagehost

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	appkm "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	persistkm "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/knowledgememory"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestKnowledgeMemoryGroupRegistersOnlyClosedTypedOperations(t *testing.T) {
	root := t.TempDir()
	owner, err := persistkm.NewSQLiteStore(filepath.Join(root, "knowledge-memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	host, err := NewHandler(HandlerConfig{Token: "knowledge-memory-test-token", JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if err := RegisterKnowledgeMemoryGroup(host, owner); err != nil {
		t.Fatalf("RegisterKnowledgeMemoryGroup: %v", err)
	}
	want := map[string]bool{
		"save_personal_archive": true, "list_personal_archive": false,
		"save_creative_knowledge": true, "list_creative_knowledge": false,
		"save_news_knowledge": true, "list_news_knowledge": false,
		"save_daily_rule": true, "list_daily_rules": false,
		"save_temporal_marker": true, "list_temporal_markers": false,
		"save_dream_run": true, "list_dream_runs": false,
		"search_public": false, "search_user": false,
		"propose_creative_candidate": true,
	}
	got := map[string]bool{}
	for _, spec := range host.specs {
		if spec.Group == GroupKnowledgeMemory {
			got[spec.Op] = spec.Mutating
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("knowledge memory operations = %v, want exactly %v", got, want)
	}
	for operation, mutating := range want {
		if mutating {
			if _, ok := host.recoverable[opKey(GroupKnowledgeMemory, operation)]; !ok {
				t.Fatalf("knowledge memory mutation %q lacks owner reconciliation", operation)
			}
		}
	}
}

func TestKnowledgeMemoryClientRoundTripsAllStoreTypesAndSeparatedSearches(t *testing.T) {
	fixture := newKnowledgeMemoryGroupFixture(t, t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 15, 0, 0, 0, time.UTC)
	personal := domainkm.PersonalArchiveEntry{EntryID: "pa-rpc", UserID: "user-rpc", OriginalText: "protected", Protected: true, CreatedAt: now}
	creative := domainkm.CreativeKnowledgeItem{ItemID: "creative-public-rpc", Title: "Public Nebula", Status: "reviewed", CreatedAt: now.Add(time.Second)}
	news := domainkm.NewsKnowledgeItem{ItemID: "news-user-rpc", UserID: "user-rpc", Source: "source", Topic: "Private Nebula", Summary: "owner document", Status: "reviewed", Visibility: "private", CreatedAt: now.Add(2 * time.Second)}
	rule := domainkm.DailyIntakeRule{RuleID: "rule-rpc", UserID: "user-rpc", Topic: "AI", Cadence: "daily", Status: "active", CreatedAt: now.Add(3 * time.Second)}
	marker := domainkm.TemporalMemoryMarker{MarkerID: "marker-rpc", UserID: "user-rpc", Layer: "today", ReferenceID: personal.EntryID, Summary: "summary", CreatedAt: now.Add(4 * time.Second)}
	dream := domainkm.DreamConsolidationRun{TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(), ActorID: "mio", Status: "proposal", ReviewStatus: "pending", CreatedAt: now.Add(5 * time.Second)}

	if err := fixture.client.SavePersonalArchiveEntry(ctx, personal); err != nil {
		t.Fatal(err)
	}
	if err := fixture.client.SaveCreativeKnowledgeItem(ctx, creative); err != nil {
		t.Fatal(err)
	}
	if err := fixture.client.SaveNewsKnowledgeItem(ctx, news); err != nil {
		t.Fatal(err)
	}
	if err := fixture.client.SaveDailyIntakeRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	if err := fixture.client.SaveTemporalMemoryMarker(ctx, marker); err != nil {
		t.Fatal(err)
	}
	if err := fixture.client.SaveDreamConsolidationRun(ctx, dream); err != nil {
		t.Fatal(err)
	}

	personalList, err := fixture.client.ListPersonalArchiveEntries(ctx, 10)
	assertKnowledgeMemoryList(t, personalList, err, personal)
	creativeList, err := fixture.client.ListCreativeKnowledgeItems(ctx, 10)
	assertKnowledgeMemoryList(t, creativeList, err, creative)
	newsList, err := fixture.client.ListNewsKnowledgeItems(ctx, 10)
	assertKnowledgeMemoryList(t, newsList, err, news)
	ruleList, err := fixture.client.ListDailyIntakeRules(ctx, 10)
	assertKnowledgeMemoryList(t, ruleList, err, rule)
	markerList, err := fixture.client.ListTemporalMemoryMarkers(ctx, 10)
	assertKnowledgeMemoryList(t, markerList, err, marker)
	dreamList, err := fixture.client.ListDreamConsolidationRuns(ctx, 10)
	assertKnowledgeMemoryList(t, dreamList, err, dream)

	publicCtx := knowledgeMemoryAgentScopeContext(t, "request-public", "shiro", "", []string{domaintool.DataScopePublic})
	public, err := fixture.client.Search(publicCtx, appkm.SearchRequest{Scope: appkm.SearchScope{Scope: appkm.SearchScopePublic}, Query: "Nebula", RecordType: "creative_knowledge", Limit: 20})
	if err != nil || len(public) != 1 || public[0].RecordID != creative.ItemID || public[0].Scope != "public" || public[0].UserID != "" || public[0].Visibility != "public" {
		t.Fatalf("public search = %#v, err %v", public, err)
	}
	userCtx := knowledgeMemoryAgentScopeContext(t, "request-user", "shiro", news.UserID, []string{domaintool.DataScopeUser})
	private, err := fixture.client.Search(userCtx, appkm.SearchRequest{Scope: appkm.SearchScope{Scope: appkm.SearchScopeUser, UserID: news.UserID}, Query: "Private Nebula", RecordType: "news_knowledge", Limit: 20})
	if err != nil || len(private) != 1 || private[0].RecordID != news.ItemID || private[0].Scope != "user" || private[0].UserID != news.UserID || private[0].Visibility != "private" {
		t.Fatalf("user search = %#v, err %v", private, err)
	}
	if _, err := fixture.client.Search(publicCtx, appkm.SearchRequest{Scope: appkm.SearchScope{Scope: appkm.SearchScopeUser, UserID: news.UserID}, Query: "Private", Limit: 20}); knowledgeMemoryErrorCode(err) != ErrorCodeUnauthorized {
		t.Fatalf("public caller user search error = %v, want unauthorized", err)
	}
}

func TestKnowledgeMemoryCandidateLostResponseRecoversAfterOwnerAndHostReopen(t *testing.T) {
	root := t.TempDir()
	fixture := newKnowledgeMemoryGroupFixture(t, root)
	candidate, receipt := knowledgeMemoryCandidateFixture()
	ctx := knowledgeMemoryAgentScopeContext(t, "request-candidate", receipt.ActorID, candidate.UserID, []string{domaintool.DataScopeUser})
	const opID = "knowledge-memory-candidate-commit-gap"
	fixture.rpc.opID = opID
	fixture.host.crashAfterCommitFor = opID
	err := func() error {
		_, err := fixture.client.SaveCreativeCandidateWithReceipt(ctx, candidate, receipt)
		return err
	}()
	if knowledgeMemoryErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("lost response error = %v, want outcome_unknown", err)
	}
	fixture.restart(t)
	fixture.rpc.opID = opID
	replayed, err := fixture.client.SaveCreativeCandidateWithReceipt(ctx, candidate, receipt)
	if err != nil || replayed {
		t.Fatalf("recovered candidate = replayed %v, err %v; want original false result", replayed, err)
	}
	var candidates, receipts, proofs int
	for _, query := range []struct {
		sql string
		out *int
	}{
		{`SELECT COUNT(*) FROM creative_knowledge WHERE item_id = ?`, &candidates},
		{`SELECT COUNT(*) FROM knowledge_memory_request_receipts WHERE item_id = ?`, &receipts},
		{`SELECT COUNT(*) FROM knowledge_memory_storagehost_receipts WHERE item_id = ?`, &proofs},
	} {
		if err := fixture.ownerDBCount(query.sql, candidate.ItemID, query.out); err != nil {
			t.Fatal(err)
		}
	}
	if candidates != 1 || receipts != 1 || proofs != 1 {
		t.Fatalf("candidate/receipt/proof counts = %d/%d/%d", candidates, receipts, proofs)
	}
	results, err := fixture.client.Search(ctx, appkm.SearchRequest{Scope: appkm.SearchScope{Scope: appkm.SearchScopeUser, UserID: candidate.UserID}, Query: candidate.Title, RecordType: "creative_knowledge", Limit: 20})
	if err != nil || len(results) != 0 {
		t.Fatalf("private candidate search projection = %#v, err %v", results, err)
	}
}

func TestKnowledgeMemoryCandidateSameOperationChangedPayloadOrUserConflicts(t *testing.T) {
	fixture := newKnowledgeMemoryGroupFixture(t, t.TempDir())
	candidate, receipt := knowledgeMemoryCandidateFixture()
	ctx := knowledgeMemoryAgentScopeContext(t, "request-conflict", receipt.ActorID, candidate.UserID, []string{domaintool.DataScopeUser})
	const opID = "knowledge-memory-candidate-conflict"
	fixture.rpc.opID = opID
	if _, err := fixture.client.SaveCreativeCandidateWithReceipt(ctx, candidate, receipt); err != nil {
		t.Fatal(err)
	}
	changed := candidate
	changed.Title = "changed private idea"
	fixture.rpc.opID = opID
	if _, err := fixture.client.SaveCreativeCandidateWithReceipt(ctx, changed, receipt); !errors.Is(err, persistkm.ErrKnowledgeMemoryRequestConflict) {
		t.Fatalf("changed payload error = %v, want request conflict", err)
	}
	other := candidate
	other.UserID = "other-user"
	otherReceipt := receipt
	otherReceipt.UserID = other.UserID
	otherCtx := knowledgeMemoryAgentScopeContext(t, "request-other", receipt.ActorID, other.UserID, []string{domaintool.DataScopeUser})
	fixture.rpc.opID = opID
	if _, err := fixture.client.SaveCreativeCandidateWithReceipt(otherCtx, other, otherReceipt); !errors.Is(err, persistkm.ErrKnowledgeMemoryRequestConflict) {
		t.Fatalf("changed user error = %v, want request conflict", err)
	}
}

func TestKnowledgeMemoryAllCanonicalSavesRecoverLostResponseAfterReopen(t *testing.T) {
	now := time.Date(2026, 10, 3, 17, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		save  func(context.Context, *KnowledgeMemoryClient) error
		count func(context.Context, *KnowledgeMemoryClient) (int, error)
	}{
		{
			name: "personal archive",
			save: func(ctx context.Context, client *KnowledgeMemoryClient) error {
				return client.SavePersonalArchiveEntry(ctx, domainkm.PersonalArchiveEntry{EntryID: "pa-recover", UserID: "user-rpc", OriginalText: "protected", Protected: true, CreatedAt: now})
			},
			count: func(ctx context.Context, client *KnowledgeMemoryClient) (int, error) {
				items, err := client.ListPersonalArchiveEntries(ctx, 10)
				return len(items), err
			},
		},
		{
			name: "creative knowledge",
			save: func(ctx context.Context, client *KnowledgeMemoryClient) error {
				return client.SaveCreativeKnowledgeItem(ctx, domainkm.CreativeKnowledgeItem{ItemID: "creative-recover", Title: "recover", Status: "reviewed", CreatedAt: now})
			},
			count: func(ctx context.Context, client *KnowledgeMemoryClient) (int, error) {
				items, err := client.ListCreativeKnowledgeItems(ctx, 10)
				return len(items), err
			},
		},
		{
			name: "news knowledge",
			save: func(ctx context.Context, client *KnowledgeMemoryClient) error {
				return client.SaveNewsKnowledgeItem(ctx, domainkm.NewsKnowledgeItem{ItemID: "news-recover", Source: "source", Topic: "recover", Status: "reviewed", CreatedAt: now})
			},
			count: func(ctx context.Context, client *KnowledgeMemoryClient) (int, error) {
				items, err := client.ListNewsKnowledgeItems(ctx, 10)
				return len(items), err
			},
		},
		{
			name: "daily rule",
			save: func(ctx context.Context, client *KnowledgeMemoryClient) error {
				return client.SaveDailyIntakeRule(ctx, domainkm.DailyIntakeRule{RuleID: "rule-recover", UserID: "user-rpc", Topic: "recover", Cadence: "daily", Status: "active", CreatedAt: now})
			},
			count: func(ctx context.Context, client *KnowledgeMemoryClient) (int, error) {
				items, err := client.ListDailyIntakeRules(ctx, 10)
				return len(items), err
			},
		},
		{
			name: "temporal marker",
			save: func(ctx context.Context, client *KnowledgeMemoryClient) error {
				return client.SaveTemporalMemoryMarker(ctx, domainkm.TemporalMemoryMarker{MarkerID: "marker-recover", UserID: "user-rpc", Layer: "today", ReferenceID: "pa-recover", Summary: "recover", CreatedAt: now})
			},
			count: func(ctx context.Context, client *KnowledgeMemoryClient) (int, error) {
				items, err := client.ListTemporalMemoryMarkers(ctx, 10)
				return len(items), err
			},
		},
		{
			name: "dream run",
			save: func(ctx context.Context, client *KnowledgeMemoryClient) error {
				return client.SaveDreamConsolidationRun(ctx, domainkm.DreamConsolidationRun{TaskID: modulecore.TaskID("tsk_00000000-0000-5000-8000-000000000071"), RunID: modulecore.RunID("run_00000000-0000-5000-8000-000000000071"), ActorID: "mio", Status: "proposal", ReviewStatus: "pending", CreatedAt: now})
			},
			count: func(ctx context.Context, client *KnowledgeMemoryClient) (int, error) {
				items, err := client.ListDreamConsolidationRuns(ctx, 10)
				return len(items), err
			},
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newKnowledgeMemoryGroupFixture(t, t.TempDir())
			opID := "knowledge-memory-save-recover-" + string(rune('a'+index))
			fixture.rpc.opID = opID
			fixture.host.crashAfterCommitFor = opID
			if err := test.save(context.Background(), fixture.client); knowledgeMemoryErrorCode(err) != ErrorCodeOutcomeUnknown {
				t.Fatalf("lost response error = %v, want outcome_unknown", err)
			}
			fixture.restart(t)
			fixture.rpc.opID = opID
			if err := test.save(context.Background(), fixture.client); err != nil {
				t.Fatalf("recover save: %v", err)
			}
			count, err := test.count(context.Background(), fixture.client)
			if err != nil || count != 1 {
				t.Fatalf("recovered count = %d, err %v", count, err)
			}
			var proofs int
			if err := fixture.ownerDBCount(`SELECT COUNT(*) FROM knowledge_memory_storagehost_save_receipts WHERE op_id = ?`, opID, &proofs); err != nil || proofs != 1 {
				t.Fatalf("owner proof count = %d, err %v", proofs, err)
			}
		})
	}
}

type knowledgeMemoryGroupFixture struct {
	root, databasePath string
	owner              *persistkm.SQLiteStore
	host               *Handler
	server             *httptest.Server
	rpc                *Client
	client             *KnowledgeMemoryClient
}

func newKnowledgeMemoryGroupFixture(t *testing.T, root string) *knowledgeMemoryGroupFixture {
	t.Helper()
	f := &knowledgeMemoryGroupFixture{root: root, databasePath: filepath.Join(root, "knowledge-memory.db")}
	f.open(t, false)
	t.Cleanup(f.close)
	return f
}

func (f *knowledgeMemoryGroupFixture) open(t *testing.T, reuseClient bool) {
	t.Helper()
	var err error
	f.owner, err = persistkm.NewSQLiteStore(f.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	f.host, err = NewHandler(HandlerConfig{Token: "knowledge-memory-test-token", JournalDir: filepath.Join(f.root, "journal")})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterKnowledgeMemoryGroup(f.host, f.owner); err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(f.host)
	if !reuseClient {
		f.rpc, err = NewClient(ClientConfig{Endpoint: f.server.URL, Token: "knowledge-memory-test-token", HTTPClient: f.server.Client()})
		if err != nil {
			t.Fatal(err)
		}
	} else {
		f.rpc.endpoint = f.server.URL + RPCPath
		f.rpc.httpClient = f.server.Client()
	}
	if err := f.rpc.Handshake(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.client = NewKnowledgeMemoryClient(f.rpc)
}

func (f *knowledgeMemoryGroupFixture) close() {
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

func (f *knowledgeMemoryGroupFixture) restart(t *testing.T) { t.Helper(); f.close(); f.open(t, true) }

func (f *knowledgeMemoryGroupFixture) ownerDBCount(query string, arg string, out *int) error {
	db, err := sql.Open("sqlite", f.databasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.QueryRow(query, arg).Scan(out)
}

func knowledgeMemoryAgentScopeContext(t *testing.T, requestID, actorID, userID string, scopes []string) context.Context {
	t.Helper()
	scope := domaintool.ToolExecutionScope{RequestID: requestID, ActorKind: domaintool.ActorKindAgent, ActorID: actorID, AuthenticatedUserID: userID, AllowedDataScopes: scopes, AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator}
	if err := scope.Validate(); err != nil {
		t.Fatal(err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func knowledgeMemoryCandidateFixture() (domainkm.CreativeKnowledgeItem, persistkm.KnowledgeMemoryRequestReceipt) {
	now := time.Date(2026, 10, 3, 16, 0, 0, 0, time.UTC)
	candidate := domainkm.CreativeKnowledgeItem{ItemID: "candidate-rpc", UserID: "user-rpc", Title: "Hidden Comet", Status: "candidate", Visibility: "private", CreatedAt: now}
	return candidate, persistkm.KnowledgeMemoryRequestReceipt{ActionID: modulecore.ActionID("act_00000000-0000-5000-8000-000000000062"), UserID: candidate.UserID, ActorID: "shiro", PayloadHash: "sha256:hidden-comet", ItemID: candidate.ItemID, CreatedAt: now.Add(time.Second)}
}

func assertKnowledgeMemoryList[T any](t *testing.T, got []T, err error, want T) {
	t.Helper()
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("list = %#v, err %v; want %#v", got, err, want)
	}
}

func knowledgeMemoryErrorCode(err error) string {
	var wire *Error
	if errors.As(err, &wire) {
		return wire.Code
	}
	return ""
}
