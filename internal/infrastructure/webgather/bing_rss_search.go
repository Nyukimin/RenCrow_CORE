package webgather

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

const (
	defaultBingWebRSSEndpoint  = "https://www.bing.com/search"
	defaultBingNewsRSSEndpoint = "https://www.bing.com/news/search"
)

// BingRSSSearchProvider provides a zero-credential live search path over
// Bing's RSS responses. It is ordinary Go HTTP/XML code and therefore keeps
// the same module contract on Windows, Ubuntu, and macOS.
type BingRSSSearchProvider struct {
	client   *http.Client
	endpoint string
	news     bool
	now      func() time.Time
}

func NewBingRSSSearchProvider() *BingRSSSearchProvider {
	return &BingRSSSearchProvider{endpoint: defaultBingWebRSSEndpoint}
}

func NewBingNewsRSSSearchProvider() *BingRSSSearchProvider {
	return &BingRSSSearchProvider{endpoint: defaultBingNewsRSSEndpoint, news: true}
}

func (p *BingRSSSearchProvider) Search(ctx context.Context, req modulewebgather.SearchRequest) (modulewebgather.SearchResponse, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return modulewebgather.SearchResponse{}, modulewebgather.NewError(modulewebgather.ErrInvalidURL, "query is required")
	}
	endpoint := strings.TrimSpace(p.endpoint)
	if endpoint == "" {
		if p.news {
			endpoint = defaultBingNewsRSSEndpoint
		} else {
			endpoint = defaultBingWebRSSEndpoint
		}
	}
	searchURL, err := url.Parse(endpoint)
	if err != nil {
		return modulewebgather.SearchResponse{}, modulewebgather.WrapError(modulewebgather.ErrInvalidURL, "invalid Bing RSS endpoint", err)
	}
	values := searchURL.Query()
	values.Set("q", query)
	values.Set("format", "rss")
	values.Set("adlt", "strict")
	if language := bingLanguage(req.Language); language != "" {
		values.Set("setlang", language)
	}
	if p.news {
		// Ask Bing to prefer newest coverage. Freshness is also enforced from
		// each item's publication time below, so this is only an ordering hint.
		values.Set("qft", `sortbydate="1"`)
	}
	searchURL.RawQuery = values.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL.String(), nil)
	if err != nil {
		return modulewebgather.SearchResponse{}, modulewebgather.WrapError(modulewebgather.ErrInvalidURL, "failed to build Bing RSS request", err)
	}
	httpReq.Header.Set("Accept", "application/rss+xml, application/xml, text/xml")
	httpReq.Header.Set("User-Agent", "RenCrow-WebGather/0.1")
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return modulewebgather.SearchResponse{}, modulewebgather.WrapError(modulewebgather.ErrFetchFailed, "failed to fetch Bing RSS search", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return modulewebgather.SearchResponse{}, modulewebgather.NewError(modulewebgather.ErrHTTPStatus, fmt.Sprintf("Bing RSS search returned HTTP %d", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return modulewebgather.SearchResponse{}, modulewebgather.WrapError(modulewebgather.ErrFetchFailed, "failed to read Bing RSS search", err)
	}
	results, skippedStale, err := p.parseResults(body, req)
	if err != nil {
		return modulewebgather.SearchResponse{}, err
	}
	return modulewebgather.SearchResponse{
		Query:    req.Query,
		Provider: req.Provider,
		Results:  results,
		Diagnostics: map[string]any{
			"cache_hit":           false,
			"error":               "",
			"endpoint":            searchURL.Host,
			"news":                p.news,
			"stale_items_skipped": skippedStale,
		},
	}, nil
}

type bingRSSDocument struct {
	Channel struct {
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
			PubDate     string `xml:"pubDate"`
		} `xml:"item"`
	} `xml:"channel"`
}

func (p *BingRSSSearchProvider) parseResults(body []byte, req modulewebgather.SearchRequest) ([]modulewebgather.SearchResult, int, error) {
	var feed bingRSSDocument
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, 0, modulewebgather.WrapError(modulewebgather.ErrFetchFailed, "failed to decode Bing RSS search", err)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = modulewebgather.DefaultSearchLimit
	}
	now := time.Now().UTC()
	if p.now != nil {
		now = p.now().UTC()
	}
	cutoff := bingFreshnessCutoff(now, req.Freshness)
	results := make([]modulewebgather.SearchResult, 0, limit)
	skippedStale := 0
	for _, item := range feed.Channel.Items {
		if p.news && !matchesSingleASCIIQuery(req.Query, item.Title+" "+item.Description) {
			continue
		}
		publishedAt := parseBingPublishedAt(item.PubDate)
		if p.news && !cutoff.IsZero() && !publishedAt.IsZero() && publishedAt.Before(cutoff) {
			skippedStale++
			continue
		}
		link := unwrapBingResultURL(item.Link)
		normalized, err := modulewebgather.NormalizeURL(link, false)
		if err != nil {
			continue
		}
		publishedAtText := ""
		if !publishedAt.IsZero() {
			publishedAtText = publishedAt.Format(time.RFC3339)
		}
		results = append(results, modulewebgather.SearchResult{
			URL:          normalized,
			Title:        strings.TrimSpace(item.Title),
			Snippet:      compactSnippet(item.Description),
			PublishedAt:  publishedAtText,
			Rank:         len(results) + 1,
			SourceEngine: bingSourceEngine(p.news),
		})
		if len(results) >= limit {
			break
		}
	}
	return results, skippedStale, nil
}

func matchesSingleASCIIQuery(query string, text string) bool {
	token := strings.ToLower(strings.TrimSpace(query))
	if token == "" || strings.ContainsAny(token, " \t\r\n") || len(token) > 30 {
		return true
	}
	for _, r := range token {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return true
		}
	}
	haystack := strings.ToLower(text)
	for offset := 0; offset < len(haystack); {
		index := strings.Index(haystack[offset:], token)
		if index < 0 {
			return false
		}
		index += offset
		leftOK := index == 0 || !isASCIIWordByte(haystack[index-1])
		right := index + len(token)
		rightOK := right == len(haystack) || !isASCIIWordByte(haystack[right])
		if leftOK && rightOK {
			return true
		}
		offset = index + 1
	}
	return false
}

func isASCIIWordByte(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= '0' && value <= '9') || value == '_' || value == '-'
}

func unwrapBingResultURL(raw string) string {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if strings.HasSuffix(strings.ToLower(parsed.Hostname()), "bing.com") {
		if target := strings.TrimSpace(parsed.Query().Get("url")); target != "" {
			return target
		}
	}
	return raw
}

func bingLanguage(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "", "ja", "ja-jp":
		return "ja-jp"
	case "en", "en-us":
		return "en-us"
	default:
		return strings.ToLower(strings.TrimSpace(language))
	}
}

func bingFreshnessCutoff(now time.Time, freshness string) time.Time {
	switch strings.ToLower(strings.TrimSpace(freshness)) {
	case "day", "24h", "today":
		// The scheduled RenCrow morning window starts at 04:00 JST. A 36-hour
		// boundary includes that completed window even before the next 04:00.
		return now.Add(-36 * time.Hour)
	case "week", "7d":
		return now.Add(-7 * 24 * time.Hour)
	case "month", "30d":
		return now.Add(-30 * 24 * time.Hour)
	default:
		return time.Time{}
	}
}

func parseBingPublishedAt(value string) time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func bingSourceEngine(news bool) string {
	if news {
		return "bing_news_rss"
	}
	return "bing_rss"
}

var _ modulewebgather.SearchProvider = (*BingRSSSearchProvider)(nil)
