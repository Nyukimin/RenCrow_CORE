package research

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/webgather"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

type scopedResearchFakeHTTP struct {
	calls int
	url   string
}

func (f *scopedResearchFakeHTTP) Fetch(_ context.Context, rawURL string, _ modulewebgather.FetchPolicy) (modulewebgather.FetchArtifact, error) {
	f.calls++
	f.url = rawURL
	return modulewebgather.FetchArtifact{
		OriginalURL: rawURL, FinalURL: rawURL, StatusCode: 200, ContentType: "text/html",
		Body: []byte("Single owner-written research source body."), FetchedAt: time.Now().UTC(),
	}, nil
}

type scopedResearchTextExtractor struct{}

func (scopedResearchTextExtractor) Extract(_ context.Context, artifact modulewebgather.FetchArtifact, requested string) (modulewebgather.ExtractedDocument, error) {
	return modulewebgather.ExtractedDocument{Text: string(artifact.Body), Title: "Source", Extractor: requested}, nil
}

func TestResearchScopedFetchUseCaseWritesOnlyThroughResearchOwner(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "research.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const sourceURL = "https://example.org/research-source?edition=7&__rencrow_research_scope=source-value"
	httpFetcher := &scopedResearchFakeHTTP{}
	resolver := webgather.ResearchCacheScopeResolver(func(ctx context.Context) (webgather.ResearchCacheScope, bool) {
		envelope, ok := engine.ExecutionEnvelope(ctx)
		if !ok {
			return webgather.ResearchCacheScope{}, false
		}
		return webgather.ResearchCacheScope{DataScope: envelope.DataScope, OwnerID: envelope.OwnerID}, true
	})
	fetchUseCase := webgather.NewUseCase(httpFetcher, scopedResearchTextExtractor{}, nil).WithFetchCache(
		webgather.NewResearchScopedFetchCache(webgather.NewL1FetchCache(store), resolver),
	)
	owner := NewAdapters(nil, fetchUseCase, store).WithSourcePolicy(func(context.Context, c.Candidate) (bool, bool) { return true, true })
	runStore := newResearchEngineStore()
	instance := &engine.Engine{Ports: engine.Ports{
		Acquisition: owner, Owner: owner, Store: runStore,
		Capabilities: []c.Capability{{ProviderID: "direct", SourceKinds: []string{"web"}, FetchText: true, Enabled: true, Ready: true, FreeAllowed: true}},
	}}
	envelope := c.Envelope{
		TaskID: "tsk_scoped_fetch_test", RunID: "run_scoped_fetch_test", TraceID: "trc_scoped_fetch_test",
		RequestID: "req_scoped_fetch_test", Actor: "owner", DataScope: "public", PolicyRevision: "scoped-cache-test",
	}
	result, err := instance.Run(context.Background(), envelope, c.Request{
		SchemaVersion: c.RequestSchema, Mode: "collect", Objective: "Single owner-written research source.",
		SourceKinds: []string{"web"}, SeedURLs: []string{sourceURL},
		Outputs: c.Outputs{Save: []c.SaveTarget{{Target: KnowledgeStagingTarget, Required: true}}, Evidence: c.EvidenceRequirements{MinDocuments: 1, MinIndependentSources: 1}},
		Limits:  c.Limits{Candidates: 1, FetchAttempts: 1, MaxDocuments: 1, DeadlineSeconds: 30},
	})
	if err != nil || result.Status != "succeeded" || result.Counts.RecordsSaved != 1 || len(result.Receipts) != 1 || httpFetcher.calls != 1 || httpFetcher.url != sourceURL {
		t.Fatalf("scoped FetchUseCase/Owner run = %+v err=%v http=%+v", result, err, httpFetcher)
	}
	items, err := store.RecentStagingItems(context.Background(), l1sqlite.L1StagingStatusPending, 10)
	if err != nil || len(items) != 1 || items[0].Namespace != "kb:research" || items[0].RawText != "Single owner-written research source body." {
		t.Fatalf("Research fetch created a secondary generic staging write: items=%+v err=%v", items, err)
	}
	if metaScope, _ := items[0].Meta["research_scope"].(string); metaScope != "public" {
		t.Fatalf("the sole staging write did not belong to the Research Owner: meta=%+v", items[0].Meta)
	}
}
