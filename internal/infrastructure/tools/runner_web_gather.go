package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
	modulewebgather "github.com/Nyukimin/picoclaw_multiLLM/modules/webgather"
)

func (r *ToolRunner) executeWebGatherFetchV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	if r.config.WebGatherFetcher == nil {
		return tool.NewError(tool.ErrInternalError, "web_gather.fetch is not configured", nil), nil
	}
	req, err := webGatherFetchRequestFromArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, err.Error(), nil), nil
	}
	resp, err := r.config.WebGatherFetcher.FetchURL(ctx, req)
	if err != nil {
		code := tool.ErrInternalError
		var wgErr *modulewebgather.Error
		if errors.As(err, &wgErr) {
			code = toolCodeForWebGatherError(wgErr.Code)
		}
		return tool.NewError(code, resp.ErrorMessage, map[string]any{
			"web_gather_error_code": resp.ErrorCode,
			"url":                   resp.URL,
			"diagnostics":           resp.Diagnostics,
		}), nil
	}
	return tool.NewSuccess(resp), nil
}

func (r *ToolRunner) executeWebGatherSearchV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	if r.config.WebGatherSearcher == nil {
		return tool.NewError(tool.ErrInternalError, "web_gather.search is not configured", nil), nil
	}
	req, err := webGatherSearchRequestFromArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, err.Error(), nil), nil
	}
	resp, err := r.config.WebGatherSearcher.Search(ctx, req)
	if err != nil {
		code := tool.ErrInternalError
		var wgErr *modulewebgather.Error
		if errors.As(err, &wgErr) {
			code = toolCodeForWebGatherError(wgErr.Code)
		}
		return tool.NewError(code, fmt.Sprint(resp.Diagnostics["error"]), map[string]any{
			"provider":    resp.Provider,
			"query":       resp.Query,
			"diagnostics": resp.Diagnostics,
		}), nil
	}
	return tool.NewSuccess(resp), nil
}

func (r *ToolRunner) executeWebGatherSearchAndFetchV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	if r.config.WebGatherSearchFetch == nil {
		return tool.NewError(tool.ErrInternalError, "web_gather.search_and_fetch is not configured", nil), nil
	}
	req, err := webGatherSearchAndFetchRequestFromArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, err.Error(), nil), nil
	}
	resp, err := r.config.WebGatherSearchFetch.SearchAndFetch(ctx, req)
	if err != nil {
		code := tool.ErrInternalError
		var wgErr *modulewebgather.Error
		if errors.As(err, &wgErr) {
			code = toolCodeForWebGatherError(wgErr.Code)
		}
		return tool.NewError(code, fmt.Sprint(resp.Diagnostics["error"]), map[string]any{
			"provider":    resp.Provider,
			"query":       resp.Query,
			"diagnostics": resp.Diagnostics,
		}), nil
	}
	return tool.NewSuccess(resp), nil
}

func webGatherFetchRequestFromArgs(args map[string]any) (modulewebgather.FetchRequest, error) {
	req := modulewebgather.FetchRequest{
		Namespace:       modulewebgather.DefaultNamespace,
		FetchProvider:   modulewebgather.DefaultFetchProvider,
		Extractor:       modulewebgather.DefaultExtractor,
		StoreStaging:    true,
		StoreStagingSet: true,
		LicenseNote:     modulewebgather.DefaultLicenseNote,
		Policy:          modulewebgather.DefaultFetchPolicy(),
	}
	if value, ok := args["url"].(string); ok {
		req.URL = strings.TrimSpace(value)
	}
	if req.URL == "" {
		return req, fmt.Errorf("'url' is required")
	}
	if value, ok := args["fetch_provider"].(string); ok && strings.TrimSpace(value) != "" {
		req.FetchProvider = strings.ToLower(strings.TrimSpace(value))
	}
	if !isAllowedWebGatherToolFetchProvider(req.FetchProvider) {
		return req, fmt.Errorf("unsupported fetch_provider: %s", req.FetchProvider)
	}
	if value, ok := args["extractor"].(string); ok && strings.TrimSpace(value) != "" {
		req.Extractor = strings.TrimSpace(value)
	}
	if !isAllowedWebGatherToolExtractor(req.Extractor) {
		return req, fmt.Errorf("unsupported extractor: %s", req.Extractor)
	}
	if value, ok := args["namespace"].(string); ok && strings.TrimSpace(value) != "" {
		req.Namespace = strings.TrimSpace(value)
	}
	if value, ok := args["source_id"].(string); ok {
		req.SourceID = strings.TrimSpace(value)
	}
	if value, ok := args["store_staging"].(bool); ok {
		req.StoreStaging = value
		req.StoreStagingSet = true
	}
	if value, ok := args["refresh"].(bool); ok {
		req.Refresh = value
	}
	if policy, ok := args["policy"].(map[string]any); ok {
		if ms, ok := int64Arg(policy["request_timeout_ms"]); ok && ms > 0 {
			req.Policy.RequestTimeout = time.Duration(ms) * time.Millisecond
		}
		if n, ok := int64Arg(policy["max_body_bytes"]); ok && n > 0 {
			req.Policy.MaxBodyBytes = n
		}
		if n, ok := int64Arg(policy["max_redirects"]); ok && n >= 0 {
			req.Policy.MaxRedirects = int(n)
		}
	}
	return req, nil
}

func webGatherSearchAndFetchRequestFromArgs(args map[string]any) (modulewebgather.SearchAndFetchRequest, error) {
	searchReq, err := webGatherSearchRequestFromArgs(args)
	if err != nil {
		return modulewebgather.SearchAndFetchRequest{}, err
	}
	req := modulewebgather.SearchAndFetchRequest{
		Query:           searchReq.Query,
		Provider:        searchReq.Provider,
		Limit:           searchReq.Limit,
		MaxFetches:      modulewebgather.DefaultMaxFetches,
		Language:        searchReq.Language,
		Freshness:       searchReq.Freshness,
		Namespace:       searchReq.Namespace,
		Refresh:         searchReq.Refresh,
		FetchProvider:   modulewebgather.DefaultFetchProvider,
		Extractor:       modulewebgather.DefaultExtractor,
		StoreStaging:    true,
		StoreStagingSet: true,
		Policy:          modulewebgather.DefaultFetchPolicy(),
	}
	if n, ok := int64Arg(args["max_fetches"]); ok && n > 0 {
		req.MaxFetches = int(n)
	}
	if value, ok := args["fetch_provider"].(string); ok && strings.TrimSpace(value) != "" {
		req.FetchProvider = strings.ToLower(strings.TrimSpace(value))
	}
	if !isAllowedWebGatherToolFetchProvider(req.FetchProvider) {
		return req, fmt.Errorf("unsupported fetch_provider: %s", req.FetchProvider)
	}
	if value, ok := args["extractor"].(string); ok && strings.TrimSpace(value) != "" {
		req.Extractor = strings.TrimSpace(value)
	}
	if !isAllowedWebGatherToolExtractor(req.Extractor) {
		return req, fmt.Errorf("unsupported extractor: %s", req.Extractor)
	}
	if value, ok := args["store_staging"].(bool); ok {
		req.StoreStaging = value
		req.StoreStagingSet = true
	}
	if policy, ok := args["policy"].(map[string]any); ok {
		if ms, ok := int64Arg(policy["request_timeout_ms"]); ok && ms > 0 {
			req.Policy.RequestTimeout = time.Duration(ms) * time.Millisecond
		}
		if n, ok := int64Arg(policy["max_body_bytes"]); ok && n > 0 {
			req.Policy.MaxBodyBytes = n
		}
		if n, ok := int64Arg(policy["max_redirects"]); ok && n >= 0 {
			req.Policy.MaxRedirects = int(n)
		}
	}
	return req, nil
}

func webGatherSearchRequestFromArgs(args map[string]any) (modulewebgather.SearchRequest, error) {
	req := modulewebgather.SearchRequest{
		Provider:  modulewebgather.DefaultSearchProvider,
		Limit:     modulewebgather.DefaultSearchLimit,
		Language:  modulewebgather.DefaultSearchLanguage,
		Freshness: modulewebgather.DefaultSearchFreshness,
		Namespace: "kb:research",
	}
	if value, ok := args["query"].(string); ok {
		req.Query = strings.TrimSpace(value)
	}
	if req.Query == "" {
		return req, fmt.Errorf("'query' is required")
	}
	if value, ok := args["provider"].(string); ok && strings.TrimSpace(value) != "" {
		req.Provider = strings.TrimSpace(value)
	}
	if req.Provider != "local_cache" && req.Provider != "searxng" && req.Provider != "rss_atom" && req.Provider != "sitemap" && req.Provider != "yacy" {
		return req, fmt.Errorf("unsupported search provider: %s", req.Provider)
	}
	if n, ok := int64Arg(args["limit"]); ok && n > 0 {
		req.Limit = int(n)
	}
	if value, ok := args["language"].(string); ok && strings.TrimSpace(value) != "" {
		req.Language = strings.TrimSpace(value)
	}
	if value, ok := args["freshness"].(string); ok && strings.TrimSpace(value) != "" {
		req.Freshness = strings.TrimSpace(value)
	}
	if value, ok := args["namespace"].(string); ok && strings.TrimSpace(value) != "" {
		req.Namespace = strings.TrimSpace(value)
	}
	if value, ok := args["refresh"].(bool); ok {
		req.Refresh = value
	}
	return req, nil
}

func isAllowedWebGatherToolExtractor(value string) bool {
	switch value {
	case "go_readability", "html_basic", "plain_text", "json_text":
		return true
	default:
		return false
	}
}

func isAllowedWebGatherToolFetchProvider(value string) bool {
	switch value {
	case "http", "webwright":
		return true
	default:
		return false
	}
}

func int64Arg(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		return int64(v), true
	case json.Number:
		n, err := strconv.ParseInt(v.String(), 10, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

func toolCodeForWebGatherError(code modulewebgather.ErrorCode) tool.ErrorCode {
	switch code {
	case modulewebgather.ErrFetchTimeout:
		return tool.ErrTimeout
	case modulewebgather.ErrRateLimited:
		return tool.ErrRateLimited
	case modulewebgather.ErrInvalidURL, modulewebgather.ErrUnsupportedScheme, modulewebgather.ErrBlockedByPolicy, modulewebgather.ErrRobotsDisallowed, modulewebgather.ErrUnsupportedContentType:
		return tool.ErrValidationFailed
	default:
		return tool.ErrInternalError
	}
}
