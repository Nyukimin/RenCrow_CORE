package webgather

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type Searcher interface {
	Search(ctx context.Context, req modulewebgather.SearchRequest) (modulewebgather.SearchResponse, error)
}

type Fetcher interface {
	FetchURL(ctx context.Context, req modulewebgather.FetchRequest) (modulewebgather.FetchResponse, error)
}

type SearchAndFetchUseCase struct {
	searcher Searcher
	fetcher  Fetcher
}

func NewSearchAndFetchUseCase(searcher Searcher, fetcher Fetcher) *SearchAndFetchUseCase {
	return &SearchAndFetchUseCase{searcher: searcher, fetcher: fetcher}
}

func (u *SearchAndFetchUseCase) SearchAndFetch(ctx context.Context, req modulewebgather.SearchAndFetchRequest) (modulewebgather.SearchAndFetchResponse, error) {
	req = normalizeSearchAndFetchRequest(req)
	start := time.Now()
	log.Printf("web_gather.search_and_fetch_started provider=%s query_len=%d limit=%d max_fetches=%d", req.Provider, len([]rune(req.Query)), req.Limit, req.MaxFetches)
	if strings.TrimSpace(req.Query) == "" {
		err := modulewebgather.NewError(modulewebgather.ErrInvalidURL, "query is required")
		return searchAndFetchFailure(req, err, start), err
	}
	if u == nil || u.searcher == nil {
		err := modulewebgather.NewError(modulewebgather.ErrFetchFailed, "web gather searcher is not configured")
		return searchAndFetchFailure(req, err, start), err
	}
	if u.fetcher == nil {
		err := modulewebgather.NewError(modulewebgather.ErrFetchFailed, "web gather fetcher is not configured")
		return searchAndFetchFailure(req, err, start), err
	}
	searchResp, err := u.searcher.Search(ctx, modulewebgather.SearchRequest{
		Query:     req.Query,
		Provider:  req.Provider,
		Limit:     req.Limit,
		Language:  req.Language,
		Freshness: req.Freshness,
		Namespace: req.Namespace,
		Refresh:   req.Refresh,
	})
	if err != nil {
		resp := searchAndFetchFailure(req, err, start)
		resp.Diagnostics["search"] = searchResp.Diagnostics
		return resp, err
	}
	results := limitSearchResults(searchResp.Results, req.MaxFetches)
	items := make([]modulewebgather.SearchAndFetchItem, 0, len(results))
	fetchErrors := 0
	for _, result := range results {
		fetchReq := modulewebgather.FetchRequest{
			URL:             result.URL,
			Namespace:       req.Namespace,
			FetchProvider:   req.FetchProvider,
			Extractor:       req.Extractor,
			StoreStaging:    req.StoreStaging,
			StoreStagingSet: req.StoreStagingSet,
			Policy:          req.Policy,
		}
		fetchResp, fetchErr := u.fetcher.FetchURL(ctx, fetchReq)
		if fetchErr != nil {
			fetchErrors++
			log.Printf("web_gather.search_and_fetch_item_failed url=%s error_code=%s", modulewebgather.SafeURLForLog(result.URL), errorCodeOf(fetchErr))
		}
		items = append(items, modulewebgather.SearchAndFetchItem{
			SearchResult: result,
			Fetch:        fetchResp,
		})
	}
	resp := modulewebgather.SearchAndFetchResponse{
		Query:    req.Query,
		Provider: req.Provider,
		Items:    nonNilSearchAndFetchItems(items),
		Diagnostics: map[string]any{
			"elapsed_ms":        time.Since(start).Milliseconds(),
			"search_result_cnt": len(searchResp.Results),
			"fetch_attempt_cnt": len(items),
			"fetch_error_cnt":   fetchErrors,
			"search":            searchResp.Diagnostics,
			"error":             "",
		},
	}
	log.Printf("web_gather.search_and_fetch_completed provider=%s search_result_count=%d fetch_attempt_count=%d fetch_error_count=%d elapsed_ms=%d", req.Provider, len(searchResp.Results), len(items), fetchErrors, time.Since(start).Milliseconds())
	return resp, nil
}

func normalizeSearchAndFetchRequest(req modulewebgather.SearchAndFetchRequest) modulewebgather.SearchAndFetchRequest {
	req.Query = strings.TrimSpace(req.Query)
	req.Provider = strings.TrimSpace(req.Provider)
	if req.Provider == "" {
		req.Provider = modulewebgather.DefaultSearchProvider
	}
	if req.Limit <= 0 {
		req.Limit = modulewebgather.DefaultSearchLimit
	}
	if req.Limit > 20 {
		req.Limit = 20
	}
	if req.MaxFetches <= 0 {
		req.MaxFetches = modulewebgather.DefaultMaxFetches
	}
	if req.MaxFetches > req.Limit {
		req.MaxFetches = req.Limit
	}
	if req.MaxFetches > 10 {
		req.MaxFetches = 10
	}
	req.Language = strings.TrimSpace(req.Language)
	if req.Language == "" {
		req.Language = modulewebgather.DefaultSearchLanguage
	}
	req.Freshness = strings.TrimSpace(req.Freshness)
	if req.Freshness == "" {
		req.Freshness = modulewebgather.DefaultSearchFreshness
	}
	req.Namespace = strings.TrimSpace(req.Namespace)
	if req.Namespace == "" {
		req.Namespace = "kb:research"
	}
	req.FetchProvider = strings.TrimSpace(req.FetchProvider)
	if req.FetchProvider == "" {
		req.FetchProvider = modulewebgather.DefaultFetchProvider
	}
	req.Extractor = strings.TrimSpace(req.Extractor)
	if req.Extractor == "" {
		req.Extractor = modulewebgather.DefaultExtractor
	}
	req.Policy = req.Policy.WithDefaults()
	if !req.StoreStagingSet && !req.StoreStaging {
		req.StoreStaging = true
	}
	return req
}

func searchAndFetchFailure(req modulewebgather.SearchAndFetchRequest, err error, start time.Time) modulewebgather.SearchAndFetchResponse {
	message := ""
	if err != nil {
		message = err.Error()
		var wgErr *modulewebgather.Error
		if errors.As(err, &wgErr) {
			message = wgErr.Message
		}
	}
	return modulewebgather.SearchAndFetchResponse{
		Query:    req.Query,
		Provider: req.Provider,
		Items:    []modulewebgather.SearchAndFetchItem{},
		Diagnostics: map[string]any{
			"elapsed_ms":        time.Since(start).Milliseconds(),
			"search_result_cnt": 0,
			"fetch_attempt_cnt": 0,
			"fetch_error_cnt":   0,
			"error":             message,
		},
	}
}

func nonNilSearchAndFetchItems(items []modulewebgather.SearchAndFetchItem) []modulewebgather.SearchAndFetchItem {
	if items == nil {
		return []modulewebgather.SearchAndFetchItem{}
	}
	return items
}
