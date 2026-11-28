package research

import (
	"context"
	"errors"
	"testing"
	"time"

	appwebgather "github.com/Nyukimin/RenCrow_CORE/internal/application/webgather"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

type researchFetchPacingStub struct {
	delay         time.Duration
	refreshCached bool
	err           error
	started       chan struct{}
}

func (p *researchFetchPacingStub) ResearchFetchPacing(context.Context, modulewebgather.FetchRequest) (time.Duration, bool, error) {
	if p.started != nil {
		p.started <- struct{}{}
	}
	return p.delay, p.refreshCached, p.err
}

type pacedFetcherStub struct {
	pacing     *researchFetchPacingStub
	response   modulewebgather.FetchResponse
	fetchErr   error
	request    modulewebgather.FetchRequest
	fetchCalls int
	fetchedAt  time.Time
}

func (f *pacedFetcherStub) ResearchFetchPacing(ctx context.Context, request modulewebgather.FetchRequest) (time.Duration, bool, error) {
	return f.pacing.ResearchFetchPacing(ctx, request)
}

func (f *pacedFetcherStub) FetchURL(_ context.Context, request modulewebgather.FetchRequest) (modulewebgather.FetchResponse, error) {
	f.fetchCalls++
	f.request = request
	f.fetchedAt = time.Now()
	return f.response, f.fetchErr
}

type acquisitionLeaseFixture struct {
	claims   int
	releases int
}

func (s *acquisitionLeaseFixture) ClaimPublicAcquisition(_ context.Context, rawURL string, now time.Time) (l1sqlite.L1PublicAcquisitionLease, bool, error) {
	canonicalURL, err := l1sqlite.CanonicalPublicAcquisitionURL(rawURL)
	if err != nil {
		return l1sqlite.L1PublicAcquisitionLease{}, false, err
	}
	s.claims++
	return l1sqlite.L1PublicAcquisitionLease{
		CanonicalURL: canonicalURL, Token: "pacing-test-lease", ExpiresAt: now.Add(l1sqlite.L1PublicAcquisitionLeaseTTL),
	}, true, nil
}

func (s *acquisitionLeaseFixture) ReleasePublicAcquisition(context.Context, l1sqlite.L1PublicAcquisitionLease, time.Time) (bool, error) {
	s.releases++
	return true, nil
}

func TestAcquireWaitsBeforeOneFetchURLCallAndRefreshesOnlyProvenRateLimitCache(t *testing.T) {
	const wait = 40 * time.Millisecond
	started := make(chan struct{}, 1)
	fetcher := &pacedFetcherStub{
		pacing:   &researchFetchPacingStub{delay: wait, refreshCached: true, started: started},
		response: completeFetchResponse("https://example.org/article"),
	}
	leases := &acquisitionLeaseFixture{}
	adapter := NewAdapters(nil, fetcher, nil).WithAcquisitionLeaseStore(leases)
	startedAt := time.Now()

	result, err := adapter.Acquire(context.Background(), c.Candidate{URL: "https://example.org/article"})
	if err != nil || result.Code != "" || result.Document.ContentState != "fetched" {
		t.Fatalf("paced acquisition failed: result=%+v err=%v", result, err)
	}
	if fetcher.fetchCalls != 1 || fetcher.fetchedAt.Sub(startedAt) < wait-5*time.Millisecond {
		t.Fatalf("acquisition did not wait before exactly one FetchURL call: calls=%d elapsed=%s", fetcher.fetchCalls, fetcher.fetchedAt.Sub(startedAt))
	}
	if !fetcher.request.Refresh {
		t.Fatal("exact cached rate_limited proof did not request a one-time cache refresh after pacing")
	}
	if leases.claims != 1 || leases.releases != 1 {
		t.Fatalf("acquisition lease was not held and released once: claims=%d releases=%d", leases.claims, leases.releases)
	}
}

func TestAcquirePacesFreshBodyFallbackAfterCacheHitWithoutRepeatingHTTP(t *testing.T) {
	const wait = 40 * time.Millisecond
	cache := &fallbackPacingCache{
		readyAt: time.Now().Add(wait),
		cached: modulewebgather.FetchResponse{
			URL: "https://example.org/article", FinalURL: "https://example.org/article", Status: "ok",
			TextPreview: "cached preview", ExtractedChars: 500,
			Diagnostics: map[string]any{"cache_hit": true},
		},
	}
	provider := &pacingHTTPFixture{}
	useCase := appwebgather.NewUseCase(provider, pacingTextExtractor{}, nil).WithFetchCache(cache)
	fetcher := &useCaseFetcherSpy{useCase: useCase}
	leases := &acquisitionLeaseFixture{}
	adapter := NewAdapters(nil, fetcher, nil).WithAcquisitionLeaseStore(leases)

	result, err := adapter.Acquire(context.Background(), c.Candidate{URL: "https://example.org/article"})
	if err != nil || result.Document.ContentState != "fetched" {
		t.Fatalf("fresh body fallback failed: result=%+v err=%v", result, err)
	}
	if provider.calls != 1 || provider.calledAt.Before(cache.readyAt) {
		t.Fatalf("fallback HTTP did not honor the shared host delay exactly once: calls=%d at=%s ready=%s", provider.calls, provider.calledAt, cache.readyAt)
	}
	if fetcher.fetchURLCalls != 2 || fetcher.pacingCalls != 2 || len(fetcher.requests) != 2 || fetcher.requests[0].Refresh || !fetcher.requests[1].Refresh {
		t.Fatalf("cache-only lookup and paced fresh fallback were not preserved: fetch_url_calls=%d pacing_calls=%d requests=%+v", fetcher.fetchURLCalls, fetcher.pacingCalls, fetcher.requests)
	}
	if cache.rateCalls != 2 || leases.claims != 1 || leases.releases != 1 {
		t.Fatalf("fallback pacing or lease lifecycle was incorrect: rate_reads=%d claims=%d releases=%d", cache.rateCalls, leases.claims, leases.releases)
	}
}

func TestAcquirePacingCancellationAndDeadlineReleaseLeaseWithoutFetch(t *testing.T) {
	tests := []struct {
		name     string
		context  func() (context.Context, func())
		wantErr  error
		wantCode string
	}{
		{
			name: "cancelled",
			context: func() (context.Context, func()) {
				ctx, cancel := context.WithCancel(context.Background())
				return ctx, cancel
			},
			wantErr: context.Canceled, wantCode: "cancelled",
		},
		{
			name: "deadline",
			context: func() (context.Context, func()) {
				return context.WithTimeout(context.Background(), 30*time.Millisecond)
			},
			wantErr: context.DeadlineExceeded, wantCode: "timeout",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{}, 1)
			fetcher := &pacedFetcherStub{pacing: &researchFetchPacingStub{delay: time.Hour, started: started}}
			leases := &acquisitionLeaseFixture{}
			adapter := NewAdapters(nil, fetcher, nil).WithAcquisitionLeaseStore(leases)
			ctx, cancel := test.context()
			defer cancel()

			type acquisition struct {
				result engine.AcquireResult
				err    error
			}
			finished := make(chan acquisition, 1)
			go func() {
				result, err := adapter.Acquire(ctx, c.Candidate{URL: "https://example.org/article"})
				finished <- acquisition{result: result, err: err}
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("acquisition did not enter the pacing wait")
			}
			if test.name == "cancelled" {
				cancel()
			}
			got := <-finished
			if !errors.Is(got.err, test.wantErr) || got.result.Code != test.wantCode {
				t.Fatalf("pacing interruption lost its terminal state: result=%+v err=%v", got.result, got.err)
			}
			if fetcher.fetchCalls != 0 || leases.claims != 1 || leases.releases != 1 {
				t.Fatalf("interrupted wait did not release its lease without fetching: calls=%d claims=%d releases=%d", fetcher.fetchCalls, leases.claims, leases.releases)
			}
		})
	}
}

func TestAcquirePreservesConcurrentRateLimitFailureWithoutRetry(t *testing.T) {
	fetchErr := modulewebgather.NewError(modulewebgather.ErrRateLimited, "shared rate state changed after pacing")
	fetcher := &pacedFetcherStub{
		pacing:   &researchFetchPacingStub{},
		fetchErr: fetchErr,
	}
	leases := &acquisitionLeaseFixture{}
	adapter := NewAdapters(nil, fetcher, nil).WithAcquisitionLeaseStore(leases)

	result, err := adapter.Acquire(context.Background(), c.Candidate{URL: "https://example.org/article"})
	if !errors.Is(err, fetchErr) || result.Code != string(modulewebgather.ErrRateLimited) {
		t.Fatalf("concurrent rate limit was hidden as a generic fetch failure: result=%+v err=%v", result, err)
	}
	if fetcher.fetchCalls != 1 || leases.claims != 1 || leases.releases != 1 {
		t.Fatalf("concurrent rate limit was retried or lease leaked: calls=%d claims=%d releases=%d", fetcher.fetchCalls, leases.claims, leases.releases)
	}
}

func completeFetchResponse(rawURL string) modulewebgather.FetchResponse {
	const text = "Complete public source text."
	return modulewebgather.FetchResponse{
		URL: rawURL, FinalURL: rawURL, Status: "ok", ExtractedText: text,
		ExtractedChars: len([]rune(text)), RawHash: modulewebgather.SHA256Text(text),
	}
}

type fallbackPacingCache struct {
	cached    modulewebgather.FetchResponse
	readyAt   time.Time
	rateCalls int
}

func (c *fallbackPacingCache) Get(_ context.Context, request modulewebgather.FetchRequest, _ time.Time) (modulewebgather.FetchResponse, bool, error) {
	if request.Refresh {
		return modulewebgather.FetchResponse{}, false, nil
	}
	return c.cached, true, nil
}

func (*fallbackPacingCache) Save(context.Context, modulewebgather.FetchRequest, modulewebgather.FetchResponse, time.Duration) error {
	return nil
}

func (c *fallbackPacingCache) RateDelay(_ context.Context, _ string, now time.Time, _ time.Duration) (time.Duration, error) {
	c.rateCalls++
	if c.readyAt.After(now) {
		return c.readyAt.Sub(now), nil
	}
	return 0, nil
}

func (*fallbackPacingCache) RecordRate(context.Context, string, time.Time) error { return nil }

type useCaseFetcherSpy struct {
	useCase       *appwebgather.UseCase
	pacingCalls   int
	fetchURLCalls int
	requests      []modulewebgather.FetchRequest
}

func (f *useCaseFetcherSpy) ResearchFetchPacing(ctx context.Context, request modulewebgather.FetchRequest) (time.Duration, bool, error) {
	f.pacingCalls++
	return f.useCase.ResearchFetchPacing(ctx, request)
}

func (f *useCaseFetcherSpy) FetchURL(ctx context.Context, request modulewebgather.FetchRequest) (modulewebgather.FetchResponse, error) {
	f.fetchURLCalls++
	f.requests = append(f.requests, request)
	return f.useCase.FetchURL(ctx, request)
}

type pacingHTTPFixture struct {
	calls    int
	calledAt time.Time
}

func (f *pacingHTTPFixture) Fetch(_ context.Context, rawURL string, _ modulewebgather.FetchPolicy) (modulewebgather.FetchArtifact, error) {
	f.calls++
	f.calledAt = time.Now()
	return modulewebgather.FetchArtifact{
		FinalURL: rawURL, StatusCode: 200, ContentType: "text/plain", FetchedAt: f.calledAt,
	}, nil
}

type pacingTextExtractor struct{}

func (pacingTextExtractor) Extract(_ context.Context, _ modulewebgather.FetchArtifact, name string) (modulewebgather.ExtractedDocument, error) {
	const text = "Fresh public source body."
	return modulewebgather.ExtractedDocument{Text: text, Extractor: name}, nil
}
