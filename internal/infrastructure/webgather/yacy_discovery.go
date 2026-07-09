package webgather

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type YaCyProvider struct {
	baseURL string
	client  *http.Client
}

func NewYaCyProvider(baseURL string) *YaCyProvider {
	return &YaCyProvider{baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/")}
}

func (p *YaCyProvider) Search(ctx context.Context, req modulewebgather.SearchRequest) (modulewebgather.SearchResponse, error) {
	if p == nil || strings.TrimSpace(p.baseURL) == "" {
		return modulewebgather.SearchResponse{}, modulewebgather.NewError(modulewebgather.ErrFetchFailed, "yacy base url is not configured")
	}
	endpoint, err := url.Parse(p.baseURL + "/yacysearch.json")
	if err != nil {
		return modulewebgather.SearchResponse{}, modulewebgather.WrapError(modulewebgather.ErrInvalidURL, "invalid yacy base url", err)
	}
	q := endpoint.Query()
	q.Set("query", req.Query)
	q.Set("maximumRecords", strconv.Itoa(req.Limit))
	q.Set("contentdom", "text")
	endpoint.RawQuery = q.Encode()

	client := p.client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return modulewebgather.SearchResponse{}, modulewebgather.WrapError(modulewebgather.ErrInvalidURL, "failed to build yacy request", err)
	}
	httpReq.Header.Set("User-Agent", "RenCrow-WebGather/0.1")
	resp, err := client.Do(httpReq)
	if err != nil {
		return modulewebgather.SearchResponse{}, modulewebgather.WrapError(modulewebgather.ErrFetchFailed, "failed to query yacy", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return modulewebgather.SearchResponse{}, modulewebgather.NewError(modulewebgather.ErrHTTPStatus, fmt.Sprintf("yacy returned HTTP %d", resp.StatusCode))
	}
	var payload yacySearchPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return modulewebgather.SearchResponse{}, modulewebgather.WrapError(modulewebgather.ErrFetchFailed, "failed to decode yacy response", err)
	}
	results := make([]modulewebgather.SearchResult, 0, len(payload.Channels))
	rank := 1
	for _, ch := range payload.Channels {
		for _, item := range ch.Items {
			if strings.TrimSpace(item.Link) == "" {
				continue
			}
			results = append(results, modulewebgather.SearchResult{
				URL:          strings.TrimSpace(item.Link),
				Title:        strings.TrimSpace(item.Title),
				Snippet:      strings.TrimSpace(item.Description),
				Rank:         rank,
				SourceEngine: "yacy",
			})
			rank++
			if req.Limit > 0 && len(results) >= req.Limit {
				break
			}
		}
	}
	return modulewebgather.SearchResponse{
		Query:    req.Query,
		Provider: req.Provider,
		Results:  results,
		Diagnostics: map[string]any{
			"cache_hit": false,
			"error":     "",
			"base_url":  p.baseURL,
		},
	}, nil
}

type yacySearchPayload struct {
	Channels []struct {
		Items []struct {
			Title       string `json:"title"`
			Link        string `json:"link"`
			Description string `json:"description"`
		} `json:"items"`
	} `json:"channels"`
}
