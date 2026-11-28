package webgather

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type researchCacheScopeContextKey struct{}

func researchCacheScopeFromContext(ctx context.Context) (ResearchCacheScope, bool) {
	scope, ok := ctx.Value(researchCacheScopeContextKey{}).(ResearchCacheScope)
	return scope, ok
}

func withResearchCacheScope(scope ResearchCacheScope) context.Context {
	return context.WithValue(context.Background(), researchCacheScopeContextKey{}, scope)
}

type scopedSearchCacheFixture struct {
	values map[string][]modulewebgather.SearchResult
	keys   []string
}

func newScopedSearchCacheFixture() *scopedSearchCacheFixture {
	return &scopedSearchCacheFixture{values: map[string][]modulewebgather.SearchResult{}}
}

func (f *scopedSearchCacheFixture) Get(_ context.Context, provider, query string, _ time.Time) ([]modulewebgather.SearchResult, bool, error) {
	key := provider + "\x00" + query
	f.keys = append(f.keys, key)
	value, ok := f.values[key]
	return value, ok, nil
}

func (f *scopedSearchCacheFixture) Save(_ context.Context, provider, query string, results []modulewebgather.SearchResult, _ time.Duration) error {
	key := provider + "\x00" + query
	f.keys = append(f.keys, key)
	f.values[key] = append([]modulewebgather.SearchResult(nil), results...)
	return nil
}

func (f *scopedSearchCacheFixture) SearchLocal(context.Context, string, int, time.Time) ([]modulewebgather.SearchResult, bool, error) {
	return []modulewebgather.SearchResult{{URL: "https://must-not-leak.invalid/"}}, true, nil
}

func TestResearchScopedSearchCacheSeparatesOwnerAndHidesQuery(t *testing.T) {
	base := newScopedSearchCacheFixture()
	cache := NewResearchScopedSearchCache(base, researchCacheScopeFromContext)
	public := withResearchCacheScope(ResearchCacheScope{DataScope: "public"})
	alice := withResearchCacheScope(ResearchCacheScope{DataScope: "user", OwnerID: "alice"})
	bob := withResearchCacheScope(ResearchCacheScope{DataScope: "user", OwnerID: "bob"})
	query := "Alice's private research interest"
	secret := []modulewebgather.SearchResult{{URL: "https://example.org/public-source", Title: "Scoped result"}}
	if err := cache.Save(public, "provider", query, secret, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := cache.Get(alice, "provider", query, time.Now()); err != nil || hit {
		t.Fatalf("public cache crossed into user scope: hit=%v err=%v", hit, err)
	}
	if err := cache.Save(alice, "provider", query, secret, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := cache.Get(bob, "provider", query, time.Now()); err != nil || hit {
		t.Fatalf("alice cache crossed into another user: hit=%v err=%v", hit, err)
	}
	if got, hit, err := cache.Get(alice, "provider", query, time.Now()); err != nil || !hit || len(got) != 1 {
		t.Fatalf("same-scope cache did not hit: results=%+v hit=%v err=%v", got, hit, err)
	}
	for _, key := range base.keys {
		if strings.Contains(key, query) {
			t.Fatalf("raw private query was stored as cache key: %q", key)
		}
	}
	if _, _, err := cache.SearchLocal(alice, "anything", 10, time.Now()); err == nil {
		t.Fatal("unscoped recent-search API was represented as a successful empty result")
	}
	invalid := withResearchCacheScope(ResearchCacheScope{DataScope: "user", OwnerID: " "})
	before := len(base.keys)
	if _, hit, err := cache.Get(invalid, "provider", query, time.Now()); err != nil || hit || len(base.keys) != before {
		t.Fatalf("invalid scope reached shared cache: hit=%v err=%v keys=%d", hit, err, len(base.keys))
	}
}

func TestResearchFetchCachePartitionPreservesReservedSourceQuery(t *testing.T) {
	ctx := withResearchCacheScope(ResearchCacheScope{DataScope: "user", OwnerID: "alice"})
	first := modulewebgather.FetchRequest{URL: "https://example.org/item?__rencrow_research_scope=source-a&edition=1"}
	second := modulewebgather.FetchRequest{URL: "https://example.org/item?__rencrow_research_scope=source-b&edition=1"}
	firstScoped, okFirst := researchScopedFetchRequest(ctx, researchCacheScopeFromContext, first)
	secondScoped, okSecond := researchScopedFetchRequest(ctx, researchCacheScopeFromContext, second)
	if !okFirst || !okSecond || firstScoped.URL == secondScoped.URL || first.URL != "https://example.org/item?__rencrow_research_scope=source-a&edition=1" {
		t.Fatalf("partition collided with a meaningful source query or mutated the request: first=%q second=%q original=%q", firstScoped.URL, secondScoped.URL, first.URL)
	}
	if !strings.Contains(firstScoped.URL, "__rencrow_research_scope=source-a") || !strings.Contains(firstScoped.URL, "edition=1") || strings.Count(firstScoped.URL, "__rencrow_research_scope=") != 2 {
		t.Fatalf("cache partition overwrote rather than preserved source query values: %q", firstScoped.URL)
	}
}

type researchScopedCountingFetcher struct {
	calls int
	urls  []string
}

func (f *researchScopedCountingFetcher) Fetch(ctx context.Context, rawURL string, _ modulewebgather.FetchPolicy) (modulewebgather.FetchArtifact, error) {
	f.calls++
	f.urls = append(f.urls, rawURL)
	scope, _ := researchCacheScopeFromContext(ctx)
	return modulewebgather.FetchArtifact{
		OriginalURL: rawURL, FinalURL: rawURL, StatusCode: 200, ContentType: "text/html",
		Body: []byte("source body for " + scope.DataScope + ":" + scope.OwnerID), FetchedAt: time.Now().UTC(),
	}, nil
}

type researchScopedBodyExtractor struct{}

func (researchScopedBodyExtractor) Extract(_ context.Context, artifact modulewebgather.FetchArtifact, requested string) (modulewebgather.ExtractedDocument, error) {
	return modulewebgather.ExtractedDocument{Text: string(artifact.Body), Title: "Source", Extractor: requested}, nil
}

type researchScopedFetchCacheFixture struct{ gets, saves, rates int }

func (f *researchScopedFetchCacheFixture) Get(context.Context, modulewebgather.FetchRequest, time.Time) (modulewebgather.FetchResponse, bool, error) {
	f.gets++
	return modulewebgather.FetchResponse{}, false, nil
}
func (f *researchScopedFetchCacheFixture) Save(context.Context, modulewebgather.FetchRequest, modulewebgather.FetchResponse, time.Duration) error {
	f.saves++
	return nil
}
func (f *researchScopedFetchCacheFixture) RateDelay(context.Context, string, time.Time, time.Duration) (time.Duration, error) {
	f.rates++
	return 0, nil
}
func (f *researchScopedFetchCacheFixture) RecordRate(context.Context, string, time.Time) error {
	f.rates++
	return nil
}

func TestResearchScopedFetchCacheReusesOnlySameScopeWithoutStaging(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "research.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	resolver := ResearchCacheScopeResolver(researchCacheScopeFromContext)
	fetcher := &researchScopedCountingFetcher{}
	useCase := NewUseCase(fetcher, researchScopedBodyExtractor{}, nil).WithFetchCache(
		NewResearchScopedFetchCache(NewL1FetchCache(store), resolver),
	)
	request := modulewebgather.FetchRequest{URL: "https://example.org/article?edition=1&__rencrow_research_scope=source-value", StoreStaging: false, StoreStagingSet: true}
	public := withResearchCacheScope(ResearchCacheScope{DataScope: "public"})
	alice := withResearchCacheScope(ResearchCacheScope{DataScope: "user", OwnerID: "alice"})
	bob := withResearchCacheScope(ResearchCacheScope{DataScope: "user", OwnerID: "bob"})
	allowNextHostFetch := func() {
		t.Helper()
		if _, err := store.SaveWebGatherRateState(context.Background(), "example.org", time.Now().Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	firstPublic, err := useCase.FetchURL(public, request)
	if err != nil {
		t.Fatal(err)
	}
	allowNextHostFetch()
	firstAlice, err := useCase.FetchURL(alice, request)
	if err != nil {
		t.Fatal(err)
	}
	secondPublic, err := useCase.FetchURL(public, request)
	if err != nil {
		t.Fatal(err)
	}
	secondAlice, err := useCase.FetchURL(alice, request)
	if err != nil {
		t.Fatal(err)
	}
	allowNextHostFetch()
	firstBob, err := useCase.FetchURL(bob, request)
	if err != nil {
		t.Fatal(err)
	}
	if fetcher.calls != 3 || len(fetcher.urls) != 3 {
		t.Fatalf("fetch did not isolate scopes while reusing same-scope bodies: calls=%d urls=%v", fetcher.calls, fetcher.urls)
	}
	for _, fetchedURL := range fetcher.urls {
		if fetchedURL != request.URL {
			t.Fatalf("cache partition changed the real HTTP URL: %q", fetchedURL)
		}
	}
	if firstPublic.ExtractedText == firstAlice.ExtractedText || firstAlice.TextExcerpt != secondAlice.TextExcerpt || firstPublic.TextExcerpt != secondPublic.TextExcerpt || strings.Contains(firstBob.TextExcerpt, "alice") {
		t.Fatalf("cache returned a body from another scope: public=%+v alice=%+v secondPublic=%+v secondAlice=%+v bob=%+v", firstPublic, firstAlice, secondPublic, secondAlice, firstBob)
	}
	if secondPublic.Diagnostics["cache_hit"] != true || secondAlice.Diagnostics["cache_hit"] != true {
		t.Fatalf("same-scope body was not reused: public=%+v alice=%+v", secondPublic.Diagnostics, secondAlice.Diagnostics)
	}
	items, err := store.SearchResearchStagingDocuments(context.Background(), "public", "", "source body", nil, time.Time{}, time.Time{}, 10)
	if err != nil || len(items.Items) != 0 {
		t.Fatalf("Research fetch created a secondary staging record: items=%d err=%v", len(items.Items), err)
	}
}

func TestResearchScopedCacheDoesNotReadOrWriteWithoutTrustedScope(t *testing.T) {
	base := newScopedSearchCacheFixture()
	search := NewResearchScopedSearchCache(base, researchCacheScopeFromContext)
	if _, hit, err := search.Get(context.Background(), "provider", "query", time.Now()); err != nil || hit || len(base.keys) != 0 {
		t.Fatalf("search cache used missing scope: hit=%v err=%v keys=%v", hit, err, base.keys)
	}
	if err := search.Save(context.Background(), "provider", "query", nil, time.Hour); err != nil || len(base.keys) != 0 {
		t.Fatalf("search cache wrote missing scope: err=%v keys=%v", err, base.keys)
	}
	fetchBase := &researchScopedFetchCacheFixture{}
	fetch := NewResearchScopedFetchCache(fetchBase, researchCacheScopeFromContext)
	if _, hit, err := fetch.Get(context.Background(), modulewebgather.FetchRequest{URL: "https://example.org/a"}, time.Now()); err != nil || hit {
		t.Fatalf("fetch cache used missing scope: hit=%v err=%v", hit, err)
	}
	if err := fetch.Save(context.Background(), modulewebgather.FetchRequest{URL: "https://example.org/a"}, modulewebgather.FetchResponse{}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if fetchBase.gets != 0 || fetchBase.saves != 0 {
		t.Fatalf("fetch cache accessed without trusted scope: %+v", fetchBase)
	}
}
