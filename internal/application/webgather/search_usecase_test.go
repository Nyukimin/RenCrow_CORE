package webgather

import (
	"context"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	"path/filepath"
	"testing"
	"time"
)

type fakeSearchProvider struct {
	resp modulewebgather.SearchResponse
	err  error
}

func (p fakeSearchProvider) Search(context.Context, modulewebgather.SearchRequest) (modulewebgather.SearchResponse, error) {
	return p.resp, p.err
}

func TestSearchUseCaseSavesAndReadsCache(t *testing.T) {
	ctx := context.Background()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	cache := NewL1SearchCache(store)
	usecase := NewSearchUseCase(cache, map[string]modulewebgather.SearchProvider{
		"searxng": fakeSearchProvider{resp: modulewebgather.SearchResponse{
			Results: []modulewebgather.SearchResult{{URL: "https://example.com/a", Title: "RenCrow queue", Snippet: "LLM queue timeout", Rank: 1, SourceEngine: "test"}},
		}},
	})
	resp, err := usecase.Search(ctx, modulewebgather.SearchRequest{Query: "RenCrow queue", Provider: "searxng", Limit: 5})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(resp.Results) != 1 || resp.Diagnostics["cache_hit"] != false {
		t.Fatalf("unexpected live response: %+v", resp)
	}
	cached, err := usecase.Search(ctx, modulewebgather.SearchRequest{Query: "RenCrow queue", Provider: "searxng", Limit: 5})
	if err != nil {
		t.Fatalf("cached Search failed: %v", err)
	}
	if len(cached.Results) != 1 || cached.Diagnostics["cache_hit"] != true {
		t.Fatalf("unexpected cached response: %+v", cached)
	}
	local, err := usecase.Search(ctx, modulewebgather.SearchRequest{Query: "timeout", Provider: "local_cache", Limit: 5})
	if err != nil {
		t.Fatalf("local cache Search failed: %v", err)
	}
	if len(local.Results) != 1 || local.Results[0].URL != "https://example.com/a" {
		t.Fatalf("unexpected local cache results: %+v", local)
	}
}

func TestSearchUseCaseUnconfiguredProviderFails(t *testing.T) {
	resp, err := NewSearchUseCase(nil, nil).Search(context.Background(), modulewebgather.SearchRequest{Query: "x", Provider: "searxng"})
	if err == nil {
		t.Fatal("expected unconfigured provider error")
	}
	if resp.Diagnostics["error"] == "" {
		t.Fatalf("expected diagnostic error: %+v", resp)
	}
}

func TestL1SearchCacheReadsLegacyWebSearchResults(t *testing.T) {
	ctx := context.Background()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	if _, err := store.SaveSearchCache(ctx, "google", "RenCrow", `[{"title":"Example","link":"https://example.com","snippet":"RenCrow docs"}]`, []string{"https://example.com"}, time.Hour); err != nil {
		t.Fatalf("SaveSearchCache failed: %v", err)
	}
	results, hit, err := NewL1SearchCache(store).SearchLocal(ctx, "RenCrow", 5, time.Now().UTC())
	if err != nil {
		t.Fatalf("SearchLocal failed: %v", err)
	}
	if !hit || len(results) != 1 || results[0].URL != "https://example.com" || results[0].SourceEngine != "legacy_web_search" {
		t.Fatalf("unexpected legacy results: hit=%t results=%+v", hit, results)
	}
}
