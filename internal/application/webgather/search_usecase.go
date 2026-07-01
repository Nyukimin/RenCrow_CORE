package webgather

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	modulewebgather "github.com/Nyukimin/picoclaw_multiLLM/modules/webgather"
)

type SearchCache interface {
	Get(ctx context.Context, provider string, query string, now time.Time) ([]modulewebgather.SearchResult, bool, error)
	Save(ctx context.Context, provider string, query string, results []modulewebgather.SearchResult, ttl time.Duration) error
	SearchLocal(ctx context.Context, query string, limit int, now time.Time) ([]modulewebgather.SearchResult, bool, error)
}

type SearchUseCase struct {
	providers map[string]modulewebgather.SearchProvider
	cache     SearchCache
	now       func() time.Time
}

func NewSearchUseCase(cache SearchCache, providers map[string]modulewebgather.SearchProvider) *SearchUseCase {
	if providers == nil {
		providers = map[string]modulewebgather.SearchProvider{}
	}
	return &SearchUseCase{
		providers: providers,
		cache:     cache,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func (u *SearchUseCase) Search(ctx context.Context, req modulewebgather.SearchRequest) (modulewebgather.SearchResponse, error) {
	req = normalizeSearchRequest(req)
	start := time.Now()
	log.Printf("web_gather.search_started provider=%s query_len=%d limit=%d", req.Provider, len([]rune(req.Query)), req.Limit)
	if strings.TrimSpace(req.Query) == "" {
		err := modulewebgather.NewError(modulewebgather.ErrInvalidURL, "query is required")
		return searchFailure(req, err, start), err
	}
	if u == nil {
		err := modulewebgather.NewError(modulewebgather.ErrFetchFailed, "web gather search usecase is not configured")
		return searchFailure(req, err, start), err
	}
	now := u.now()
	if req.Provider == "local_cache" {
		if u.cache == nil {
			err := modulewebgather.NewError(modulewebgather.ErrCacheError, "search cache is not configured")
			return searchFailure(req, err, start), err
		}
		results, hit, err := u.cache.SearchLocal(ctx, req.Query, req.Limit, now)
		if err != nil {
			wrapped := modulewebgather.WrapError(modulewebgather.ErrCacheError, "failed to search local cache", err)
			return searchFailure(req, wrapped, start), wrapped
		}
		resp := modulewebgather.SearchResponse{
			Query:    req.Query,
			Provider: req.Provider,
			Results:  nonNilSearchResults(limitSearchResults(results, req.Limit)),
			Diagnostics: map[string]any{
				"elapsed_ms": time.Since(start).Milliseconds(),
				"cache_hit":  hit,
				"error":      "",
			},
		}
		log.Printf("web_gather.search_completed provider=%s result_count=%d elapsed_ms=%d cache_hit=%t", req.Provider, len(resp.Results), time.Since(start).Milliseconds(), hit)
		return resp, nil
	}
	if u.cache != nil && !req.Refresh {
		results, hit, err := u.cache.Get(ctx, req.Provider, req.Query, now)
		if err != nil {
			wrapped := modulewebgather.WrapError(modulewebgather.ErrCacheError, "failed to read search cache", err)
			return searchFailure(req, wrapped, start), wrapped
		}
		if hit {
			resp := modulewebgather.SearchResponse{
				Query:    req.Query,
				Provider: req.Provider,
				Results:  nonNilSearchResults(limitSearchResults(results, req.Limit)),
				Diagnostics: map[string]any{
					"elapsed_ms": time.Since(start).Milliseconds(),
					"cache_hit":  true,
					"error":      "",
				},
			}
			log.Printf("web_gather.search_completed provider=%s result_count=%d elapsed_ms=%d cache_hit=true", req.Provider, len(resp.Results), time.Since(start).Milliseconds())
			return resp, nil
		}
	}
	provider := u.providers[req.Provider]
	if provider == nil {
		err := modulewebgather.NewError(modulewebgather.ErrFetchFailed, "search provider is not configured: "+req.Provider)
		return searchFailure(req, err, start), err
	}
	resp, err := provider.Search(ctx, req)
	if err != nil {
		log.Printf("web_gather.search_failed provider=%s error_code=%s elapsed_ms=%d", req.Provider, searchErrorCode(err), time.Since(start).Milliseconds())
		return searchFailure(req, err, start), err
	}
	resp.Query = req.Query
	resp.Provider = req.Provider
	resp.Results = nonNilSearchResults(limitSearchResults(resp.Results, req.Limit))
	if resp.Diagnostics == nil {
		resp.Diagnostics = map[string]any{}
	}
	resp.Diagnostics["elapsed_ms"] = time.Since(start).Milliseconds()
	resp.Diagnostics["cache_hit"] = false
	if _, ok := resp.Diagnostics["error"]; !ok {
		resp.Diagnostics["error"] = ""
	}
	if u.cache != nil && len(resp.Results) > 0 {
		if err := u.cache.Save(ctx, req.Provider, req.Query, resp.Results, 6*time.Hour); err != nil {
			wrapped := modulewebgather.WrapError(modulewebgather.ErrCacheError, "failed to save search cache", err)
			return searchFailure(req, wrapped, start), wrapped
		}
	}
	log.Printf("web_gather.search_completed provider=%s result_count=%d elapsed_ms=%d cache_hit=false", req.Provider, len(resp.Results), time.Since(start).Milliseconds())
	return resp, nil
}

func normalizeSearchRequest(req modulewebgather.SearchRequest) modulewebgather.SearchRequest {
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
	return req
}

func limitSearchResults(results []modulewebgather.SearchResult, limit int) []modulewebgather.SearchResult {
	if limit <= 0 || len(results) <= limit {
		return results
	}
	return results[:limit]
}

func nonNilSearchResults(results []modulewebgather.SearchResult) []modulewebgather.SearchResult {
	if results == nil {
		return []modulewebgather.SearchResult{}
	}
	return results
}

func searchFailure(req modulewebgather.SearchRequest, err error, start time.Time) modulewebgather.SearchResponse {
	message := ""
	if err != nil {
		message = err.Error()
	}
	return modulewebgather.SearchResponse{
		Query:    req.Query,
		Provider: req.Provider,
		Results:  []modulewebgather.SearchResult{},
		Diagnostics: map[string]any{
			"elapsed_ms": time.Since(start).Milliseconds(),
			"cache_hit":  false,
			"error":      message,
		},
	}
}

func searchErrorCode(err error) modulewebgather.ErrorCode {
	var wgErr *modulewebgather.Error
	if errors.As(err, &wgErr) {
		return wgErr.Code
	}
	return modulewebgather.ErrFetchFailed
}
