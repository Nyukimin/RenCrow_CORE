package research

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/sourcefetcher"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

type blockingResearchFetcher struct {
	started chan struct{}
	resume  chan struct{}
	result  modulewebgather.FetchResponse
}

type blockingResearchSearcher struct {
	started chan struct{}
	resume  chan struct{}
}

func (s *blockingResearchSearcher) Search(ctx context.Context, req modulewebgather.SearchRequest) (modulewebgather.SearchResponse, error) {
	s.started <- struct{}{}
	select {
	case <-s.resume:
		return modulewebgather.SearchResponse{Provider: req.Provider}, nil
	case <-ctx.Done():
		return modulewebgather.SearchResponse{}, ctx.Err()
	}
}

func (f *blockingResearchFetcher) FetchURL(ctx context.Context, _ modulewebgather.FetchRequest) (modulewebgather.FetchResponse, error) {
	f.started <- struct{}{}
	select {
	case <-f.resume:
		return f.result, nil
	case <-ctx.Done():
		return modulewebgather.FetchResponse{}, ctx.Err()
	}
}

func TestResearchAcquisitionLeaseExcludesSourceFetcherForSameURL(t *testing.T) {
	ctx := context.Background()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>A web source for the shared lease test.</body></html>"))
	}))
	defer server.Close()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SaveSourceRegistryEntry(ctx, l1sqlite.L1SourceRegistryEntry{
		SourceID: "web:shared", URL: server.URL, Kind: l1sqlite.L1SourceKindWebGather,
		TrustScore: 0.9, FetchInterval: time.Hour, LicenseNote: "test", Enabled: true,
		Meta: map[string]interface{}{"allow_localhost": true},
	}); err != nil {
		t.Fatal(err)
	}
	text := "Complete shared acquisition source text."
	fetcher := &blockingResearchFetcher{
		started: make(chan struct{}, 1), resume: make(chan struct{}),
		result: modulewebgather.FetchResponse{URL: server.URL, FinalURL: server.URL, Status: "ok", ContentType: "text/html", ExtractedText: text, ExtractedChars: len([]rune(text))},
	}
	adapter := NewAdapters(nil, fetcher, store).WithSourcePolicy(func(context.Context, c.Candidate) (bool, bool) { return true, true })
	acquired := make(chan error, 1)
	go func() {
		_, err := adapter.Acquire(ctx, c.Candidate{URL: server.URL, SourceKind: "web"})
		acquired <- err
	}()
	select {
	case <-fetcher.started:
	case <-time.After(5 * time.Second):
		t.Fatal("research acquisition did not start")
	}
	result, err := sourcefetcher.RunSource(ctx, store, "web:shared", time.Now().UTC(), sourcefetcher.SweepOptions{LimitPerSource: 1})
	if err == nil || result.Failed != 1 {
		t.Fatalf("source fetcher during Research lease=(%+v,%v), want busy failure", result, err)
	}
	if requests.Load() != 0 {
		t.Fatalf("source fetcher made %d HTTP requests while Research held the lease", requests.Load())
	}
	close(fetcher.resume)
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("research acquisition failed after lease release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("research acquisition did not finish")
	}
}

func TestDirectFeedDiscoveryLeaseExcludesSourceFetcherForSameURL(t *testing.T) {
	ctx := context.Background()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>feed</title></channel></rss>`))
	}))
	defer server.Close()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SaveSourceRegistryEntry(ctx, l1sqlite.L1SourceRegistryEntry{
		SourceID: "rss:shared", URL: server.URL, Kind: l1sqlite.L1SourceKindRSS,
		TrustScore: 0.9, FetchInterval: time.Hour, LicenseNote: "test", Enabled: true,
		Meta: map[string]interface{}{"allow_localhost": true},
	}); err != nil {
		t.Fatal(err)
	}
	searcher := &blockingResearchSearcher{started: make(chan struct{}, 1), resume: make(chan struct{})}
	adapter := NewAdapters(searcher, nil, store)
	discovered := make(chan error, 1)
	go func() {
		_, err := adapter.Discover(ctx, engine.DiscoveryRequest{
			Request:  c.Request{Query: server.URL, SourceKinds: []string{"rss"}},
			Provider: c.Capability{ProviderID: "rss_atom", Search: true, Enabled: true, Ready: true},
		})
		discovered <- err
	}()
	select {
	case <-searcher.started:
	case <-time.After(5 * time.Second):
		t.Fatal("direct feed discovery did not start")
	}
	result, err := sourcefetcher.RunSource(ctx, store, "rss:shared", time.Now().UTC(), sourcefetcher.SweepOptions{LimitPerSource: 1})
	if err == nil || result.Failed != 1 {
		t.Fatalf("source fetcher during direct discovery=(%+v,%v), want busy failure", result, err)
	}
	if requests.Load() != 0 {
		t.Fatalf("source fetcher made %d requests while direct discovery held the lease", requests.Load())
	}
	close(searcher.resume)
	select {
	case err := <-discovered:
		if err != nil {
			t.Fatalf("direct discovery failed after lease release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("direct feed discovery did not finish")
	}
}
