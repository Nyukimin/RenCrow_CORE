package webgather

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type researchFetchPacingProbe interface {
	ResearchFetchPacing(context.Context, modulewebgather.FetchRequest) (time.Duration, bool, error)
}

func TestResearchFetchPacingUsesScopedCacheAndSharedRateState(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := withResearchCacheScope(ResearchCacheScope{DataScope: "user", OwnerID: "ren"})
	cache := NewResearchScopedFetchCache(NewL1FetchCache(store), researchCacheScopeFromContext)
	now := time.Now().UTC()
	if _, err := store.SaveWebGatherRateState(ctx, "example.org", now); err != nil {
		t.Fatal(err)
	}
	useCase := NewUseCase(&countingFetcher{}, fakeExtractor{}, nil).WithFetchCache(cache)
	pacer, ok := any(useCase).(researchFetchPacingProbe)
	if !ok {
		t.Fatal("WebGather UseCase does not expose the Research pre-acquisition pacing port")
	}

	rateLimitedRequest := modulewebgather.FetchRequest{
		URL: "https://example.org/rate-limited", FetchProvider: modulewebgather.DefaultFetchProvider,
		Extractor: modulewebgather.DefaultExtractor, StoreStaging: false, StoreStagingSet: true,
	}
	rateLimited := modulewebgather.FetchResponse{
		URL: rateLimitedRequest.URL, Status: "failed", ErrorCode: modulewebgather.ErrRateLimited,
		ErrorMessage: "existing shared rate limit",
	}
	if err := cache.Save(ctx, rateLimitedRequest, rateLimited, time.Minute); err != nil {
		t.Fatal(err)
	}
	delay, refresh, err := pacer.ResearchFetchPacing(ctx, rateLimitedRequest)
	if err != nil || delay <= 0 || delay > 3*time.Second || !refresh {
		t.Fatalf("rate-limited cache was not paired with shared host delay: delay=%s refresh=%v err=%v", delay, refresh, err)
	}

	// Research preflight must not change ordinary WebGather FetchURL behavior:
	// without Refresh it still returns the cached rate_limited result.
	response, err := useCase.FetchURL(ctx, rateLimitedRequest)
	var fetchErr *modulewebgather.Error
	if !errors.As(err, &fetchErr) || fetchErr.Code != modulewebgather.ErrRateLimited || response.ErrorCode != modulewebgather.ErrRateLimited {
		t.Fatalf("ordinary WebGather fetch changed its cached rate limit result: response=%+v err=%v", response, err)
	}

	for name, response := range map[string]modulewebgather.FetchResponse{
		"cached success": {
			URL: "https://example.org/cached", Status: "ok", TextPreview: "cached",
			TextExcerpt: "cached complete body evidence", ExtractedChars: 29,
		},
		"cached non-rate failure": {
			URL: "https://example.org/blocked", Status: "failed", ErrorCode: modulewebgather.ErrBlockedByPolicy,
		},
	} {
		request := modulewebgather.FetchRequest{
			URL: response.URL, FetchProvider: modulewebgather.DefaultFetchProvider,
			Extractor: modulewebgather.DefaultExtractor, StoreStaging: false, StoreStagingSet: true,
		}
		if err := cache.Save(ctx, request, response, time.Minute); err != nil {
			t.Fatal(err)
		}
		delay, refresh, err := pacer.ResearchFetchPacing(ctx, request)
		if err != nil || delay != 0 || refresh {
			t.Fatalf("%s should stay on WebGather's existing cache path: delay=%s refresh=%v err=%v", name, delay, refresh, err)
		}
	}
}
