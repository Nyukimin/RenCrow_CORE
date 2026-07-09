package webgather

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type SearXNGProvider struct {
	BaseURL string
	Client  *http.Client
}

func NewSearXNGProvider(baseURL string) *SearXNGProvider {
	return &SearXNGProvider{BaseURL: baseURL}
}

func (p *SearXNGProvider) Search(ctx context.Context, req modulewebgather.SearchRequest) (modulewebgather.SearchResponse, error) {
	base := strings.TrimSpace(p.BaseURL)
	if base == "" {
		return modulewebgather.SearchResponse{}, modulewebgather.NewError(modulewebgather.ErrFetchFailed, "searxng base URL is required")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return modulewebgather.SearchResponse{}, modulewebgather.WrapError(modulewebgather.ErrInvalidURL, "invalid searxng base URL", err)
	}
	if strings.TrimRight(u.Path, "/") == "" {
		u.Path = "/search"
	} else {
		u.Path = strings.TrimRight(u.Path, "/") + "/search"
	}
	q := u.Query()
	q.Set("q", req.Query)
	q.Set("format", "json")
	if req.Language != "" {
		q.Set("language", req.Language)
	}
	u.RawQuery = q.Encode()
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	start := time.Now()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return modulewebgather.SearchResponse{}, modulewebgather.WrapError(modulewebgather.ErrInvalidURL, "failed to build searxng request", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "RenCrow-WebGather/0.1")
	resp, err := client.Do(httpReq)
	if err != nil {
		return modulewebgather.SearchResponse{}, modulewebgather.WrapError(modulewebgather.ErrFetchFailed, "searxng request failed", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return modulewebgather.SearchResponse{}, modulewebgather.NewError(modulewebgather.ErrRateLimited, "searxng returned 429")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return modulewebgather.SearchResponse{}, modulewebgather.NewError(modulewebgather.ErrHTTPStatus, fmt.Sprintf("searxng returned HTTP %d", resp.StatusCode))
	}
	var payload struct {
		Results []struct {
			URL     string `json:"url"`
			Title   string `json:"title"`
			Content string `json:"content"`
			Engine  string `json:"engine"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return modulewebgather.SearchResponse{}, modulewebgather.WrapError(modulewebgather.ErrExtractFailed, "failed to decode searxng JSON", err)
	}
	results := make([]modulewebgather.SearchResult, 0, len(payload.Results))
	for _, item := range payload.Results {
		if strings.TrimSpace(item.URL) == "" {
			continue
		}
		results = append(results, modulewebgather.SearchResult{
			URL:          strings.TrimSpace(item.URL),
			Title:        strings.TrimSpace(item.Title),
			Snippet:      strings.TrimSpace(item.Content),
			Rank:         len(results) + 1,
			SourceEngine: firstNonEmpty(strings.TrimSpace(item.Engine), "searxng"),
		})
		if req.Limit > 0 && len(results) >= req.Limit {
			break
		}
	}
	return modulewebgather.SearchResponse{
		Query:    req.Query,
		Provider: "searxng",
		Results:  results,
		Diagnostics: map[string]any{
			"elapsed_ms": time.Since(start).Milliseconds(),
			"cache_hit":  false,
			"error":      "",
		},
	}, nil
}
