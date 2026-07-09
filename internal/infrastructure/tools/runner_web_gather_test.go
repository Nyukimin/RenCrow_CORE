package tools

import (
	"context"
	"testing"

	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type fakeWebGatherToolFetcher struct {
	req  modulewebgather.FetchRequest
	resp modulewebgather.FetchResponse
	err  error
}

type fakeWebGatherToolSearcher struct {
	req  modulewebgather.SearchRequest
	resp modulewebgather.SearchResponse
	err  error
}

type fakeWebGatherToolSearchAndFetcher struct {
	req  modulewebgather.SearchAndFetchRequest
	resp modulewebgather.SearchAndFetchResponse
	err  error
}

func (s *fakeWebGatherToolSearcher) Search(_ context.Context, req modulewebgather.SearchRequest) (modulewebgather.SearchResponse, error) {
	s.req = req
	return s.resp, s.err
}

func (s *fakeWebGatherToolSearchAndFetcher) SearchAndFetch(_ context.Context, req modulewebgather.SearchAndFetchRequest) (modulewebgather.SearchAndFetchResponse, error) {
	s.req = req
	return s.resp, s.err
}

func (f *fakeWebGatherToolFetcher) FetchURL(_ context.Context, req modulewebgather.FetchRequest) (modulewebgather.FetchResponse, error) {
	f.req = req
	return f.resp, f.err
}

func TestToolRunner_WebGatherFetchV2(t *testing.T) {
	fetcher := &fakeWebGatherToolFetcher{resp: modulewebgather.FetchResponse{
		Status:           "ok",
		URL:              "https://example.com",
		FinalURL:         "https://example.com",
		StagingID:        "stage-1",
		ValidationStatus: "pending",
	}}
	runner := NewToolRunner(ToolRunnerConfig{WebGatherFetcher: fetcher})
	resp, err := runner.ExecuteV2(context.Background(), "web_gather.fetch", map[string]any{
		"url":       "https://example.com",
		"namespace": "kb:web",
		"policy": map[string]any{
			"request_timeout_ms": 1000,
			"max_body_bytes":     4096,
			"max_redirects":      2,
		},
	})
	if err != nil {
		t.Fatalf("ExecuteV2 failed: %v", err)
	}
	if resp.IsError() {
		t.Fatalf("expected success, got %v", resp.Error)
	}
	if fetcher.req.Namespace != "kb:web" || fetcher.req.Policy.RequestTimeout.Milliseconds() != 1000 {
		t.Fatalf("unexpected request: %+v", fetcher.req)
	}
}

func TestToolRunner_WebGatherFetchV2AllowsWebwrightProvider(t *testing.T) {
	fetcher := &fakeWebGatherToolFetcher{resp: modulewebgather.FetchResponse{Status: "ok", URL: "https://example.com"}}
	runner := NewToolRunner(ToolRunnerConfig{WebGatherFetcher: fetcher})
	resp, err := runner.ExecuteV2(context.Background(), "web_gather.fetch", map[string]any{
		"url":            "https://example.com",
		"fetch_provider": "webwright",
	})
	if err != nil {
		t.Fatalf("ExecuteV2 failed: %v", err)
	}
	if resp.IsError() {
		t.Fatalf("expected success, got %v", resp.Error)
	}
	if fetcher.req.FetchProvider != "webwright" {
		t.Fatalf("expected webwright fetch provider, got %+v", fetcher.req)
	}
}

func TestToolRunner_WebGatherFetchMetadataRegistered(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{WebGatherFetcher: &fakeWebGatherToolFetcher{}})
	metas, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	for _, meta := range metas {
		if meta.ToolID == "web_gather.fetch" {
			if meta.Category != "query" || meta.Parameters == nil {
				t.Fatalf("unexpected metadata: %+v", meta)
			}
			return
		}
	}
	t.Fatal("web_gather.fetch metadata not registered")
}

func TestToolRunner_WebGatherSearchV2(t *testing.T) {
	searcher := &fakeWebGatherToolSearcher{resp: modulewebgather.SearchResponse{
		Query:    "RenCrow",
		Provider: "local_cache",
		Results:  []modulewebgather.SearchResult{{URL: "https://example.com", Title: "Example", Rank: 1}},
		Diagnostics: map[string]any{
			"elapsed_ms": int64(1),
			"cache_hit":  true,
			"error":      "",
		},
	}}
	runner := NewToolRunner(ToolRunnerConfig{WebGatherSearcher: searcher})
	resp, err := runner.ExecuteV2(context.Background(), "web_gather.search", map[string]any{
		"query":    "RenCrow",
		"provider": "local_cache",
		"limit":    3,
	})
	if err != nil {
		t.Fatalf("ExecuteV2 failed: %v", err)
	}
	if resp.IsError() {
		t.Fatalf("expected success, got %v", resp.Error)
	}
	if searcher.req.Query != "RenCrow" || searcher.req.Limit != 3 {
		t.Fatalf("unexpected request: %+v", searcher.req)
	}
}

func TestToolRunner_WebGatherSearchV2AllowsFeedProviders(t *testing.T) {
	searcher := &fakeWebGatherToolSearcher{resp: modulewebgather.SearchResponse{
		Query:       "https://example.com/feed.xml",
		Provider:    "rss_atom",
		Diagnostics: map[string]any{"error": ""},
	}}
	runner := NewToolRunner(ToolRunnerConfig{WebGatherSearcher: searcher})
	resp, err := runner.ExecuteV2(context.Background(), "web_gather.search", map[string]any{
		"query":    "https://example.com/feed.xml",
		"provider": "rss_atom",
	})
	if err != nil {
		t.Fatalf("ExecuteV2 failed: %v", err)
	}
	if resp.IsError() {
		t.Fatalf("expected success, got %v", resp.Error)
	}
	if searcher.req.Provider != "rss_atom" {
		t.Fatalf("expected rss_atom provider, got %+v", searcher.req)
	}
}

func TestToolRunner_WebGatherSearchMetadataRegistered(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{WebGatherSearcher: &fakeWebGatherToolSearcher{}})
	metas, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	for _, meta := range metas {
		if meta.ToolID == "web_gather.search" {
			if meta.Category != "query" || meta.Parameters == nil {
				t.Fatalf("unexpected metadata: %+v", meta)
			}
			return
		}
	}
	t.Fatal("web_gather.search metadata not registered")
}

func TestToolRunner_WebGatherSearchAndFetchV2(t *testing.T) {
	searchAndFetcher := &fakeWebGatherToolSearchAndFetcher{resp: modulewebgather.SearchAndFetchResponse{
		Query:    "RenCrow",
		Provider: "local_cache",
		Items: []modulewebgather.SearchAndFetchItem{{
			SearchResult: modulewebgather.SearchResult{URL: "https://example.com", Title: "Example", Rank: 1},
			Fetch:        modulewebgather.FetchResponse{URL: "https://example.com", Status: "ok", StagingID: "stage-1"},
		}},
		Diagnostics: map[string]any{"fetch_error_cnt": 0, "error": ""},
	}}
	runner := NewToolRunner(ToolRunnerConfig{WebGatherSearchFetch: searchAndFetcher})
	resp, err := runner.ExecuteV2(context.Background(), "web_gather.search_and_fetch", map[string]any{
		"query":         "RenCrow",
		"provider":      "local_cache",
		"limit":         4,
		"max_fetches":   2,
		"store_staging": false,
	})
	if err != nil {
		t.Fatalf("ExecuteV2 failed: %v", err)
	}
	if resp.IsError() {
		t.Fatalf("expected success, got %v", resp.Error)
	}
	if searchAndFetcher.req.Query != "RenCrow" || searchAndFetcher.req.MaxFetches != 2 || searchAndFetcher.req.StoreStaging {
		t.Fatalf("unexpected request: %+v", searchAndFetcher.req)
	}
}

func TestToolRunner_WebGatherSearchAndFetchV2AllowsWebwrightProvider(t *testing.T) {
	searchAndFetcher := &fakeWebGatherToolSearchAndFetcher{resp: modulewebgather.SearchAndFetchResponse{
		Query:       "RenCrow",
		Provider:    "local_cache",
		Diagnostics: map[string]any{"error": ""},
	}}
	runner := NewToolRunner(ToolRunnerConfig{WebGatherSearchFetch: searchAndFetcher})
	resp, err := runner.ExecuteV2(context.Background(), "web_gather.search_and_fetch", map[string]any{
		"query":          "RenCrow",
		"provider":       "local_cache",
		"fetch_provider": "webwright",
	})
	if err != nil {
		t.Fatalf("ExecuteV2 failed: %v", err)
	}
	if resp.IsError() {
		t.Fatalf("expected success, got %v", resp.Error)
	}
	if searchAndFetcher.req.FetchProvider != "webwright" {
		t.Fatalf("expected webwright fetch provider, got %+v", searchAndFetcher.req)
	}
}

func TestToolRunner_WebGatherSearchAndFetchMetadataRegistered(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{WebGatherSearchFetch: &fakeWebGatherToolSearchAndFetcher{}})
	metas, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	for _, meta := range metas {
		if meta.ToolID == "web_gather.search_and_fetch" {
			if meta.Category != "query" || meta.Parameters == nil {
				t.Fatalf("unexpected metadata: %+v", meta)
			}
			return
		}
	}
	t.Fatal("web_gather.search_and_fetch metadata not registered")
}
