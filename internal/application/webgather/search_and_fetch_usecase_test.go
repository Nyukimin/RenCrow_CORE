package webgather

import (
	"context"
	"testing"

	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type fakeSearchAndFetchSearcher struct {
	req  modulewebgather.SearchRequest
	resp modulewebgather.SearchResponse
	err  error
}

func (s *fakeSearchAndFetchSearcher) Search(_ context.Context, req modulewebgather.SearchRequest) (modulewebgather.SearchResponse, error) {
	s.req = req
	return s.resp, s.err
}

type fakeSearchAndFetchFetcher struct {
	reqs  []modulewebgather.FetchRequest
	resp  map[string]modulewebgather.FetchResponse
	errBy map[string]error
}

func (f *fakeSearchAndFetchFetcher) FetchURL(_ context.Context, req modulewebgather.FetchRequest) (modulewebgather.FetchResponse, error) {
	f.reqs = append(f.reqs, req)
	if err := f.errBy[req.URL]; err != nil {
		return f.resp[req.URL], err
	}
	return f.resp[req.URL], nil
}

func TestSearchAndFetchUseCaseKeepsFetchFailuresItemLevel(t *testing.T) {
	searcher := &fakeSearchAndFetchSearcher{resp: modulewebgather.SearchResponse{
		Query:    "RenCrow",
		Provider: "local_cache",
		Results: []modulewebgather.SearchResult{
			{URL: "https://example.com/ok", Title: "OK", Rank: 1},
			{URL: "https://example.com/fail", Title: "Fail", Rank: 2},
		},
		Diagnostics: map[string]any{"cache_hit": true, "error": ""},
	}}
	fetcher := &fakeSearchAndFetchFetcher{
		resp: map[string]modulewebgather.FetchResponse{
			"https://example.com/ok":   {URL: "https://example.com/ok", Status: "ok", StagingID: "stage-1"},
			"https://example.com/fail": {URL: "https://example.com/fail", Status: "failed", ErrorCode: modulewebgather.ErrFetchTimeout, ErrorMessage: "timeout"},
		},
		errBy: map[string]error{
			"https://example.com/fail": modulewebgather.NewError(modulewebgather.ErrFetchTimeout, "timeout"),
		},
	}
	resp, err := NewSearchAndFetchUseCase(searcher, fetcher).SearchAndFetch(context.Background(), modulewebgather.SearchAndFetchRequest{
		Query:           "RenCrow",
		Provider:        "local_cache",
		Limit:           2,
		MaxFetches:      2,
		Namespace:       "kb:research",
		StoreStaging:    false,
		StoreStagingSet: true,
	})
	if err != nil {
		t.Fatalf("SearchAndFetch failed: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("expected two items, got %+v", resp)
	}
	if resp.Diagnostics["fetch_error_cnt"] != 1 {
		t.Fatalf("expected item-level fetch error count, got %+v", resp.Diagnostics)
	}
	if resp.Items[1].Fetch.Status != "failed" || resp.Items[1].SearchResult.URL != "https://example.com/fail" {
		t.Fatalf("unexpected failed item: %+v", resp.Items[1])
	}
	if len(fetcher.reqs) != 2 || fetcher.reqs[0].StoreStaging {
		t.Fatalf("unexpected fetch requests: %+v", fetcher.reqs)
	}
}

func TestSearchAndFetchUseCaseSearchFailureFailsWholeRun(t *testing.T) {
	searchErr := modulewebgather.NewError(modulewebgather.ErrFetchFailed, "search failed")
	searcher := &fakeSearchAndFetchSearcher{
		resp: modulewebgather.SearchResponse{Diagnostics: map[string]any{"error": "search failed"}},
		err:  searchErr,
	}
	resp, err := NewSearchAndFetchUseCase(searcher, &fakeSearchAndFetchFetcher{}).SearchAndFetch(context.Background(), modulewebgather.SearchAndFetchRequest{Query: "x"})
	if err == nil {
		t.Fatal("expected search error")
	}
	if len(resp.Items) != 0 || resp.Diagnostics["error"] == "" {
		t.Fatalf("unexpected failure response: %+v", resp)
	}
}
