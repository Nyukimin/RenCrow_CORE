package webgather

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type FeedDiscoveryProvider struct {
	client *http.Client
}

func NewFeedDiscoveryProvider() *FeedDiscoveryProvider {
	return &FeedDiscoveryProvider{}
}

func (p *FeedDiscoveryProvider) Search(ctx context.Context, req modulewebgather.SearchRequest) (modulewebgather.SearchResponse, error) {
	feedURL, err := modulewebgather.NormalizeURL(req.Query, false)
	if err != nil {
		return modulewebgather.SearchResponse{}, err
	}
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return modulewebgather.SearchResponse{}, modulewebgather.WrapError(modulewebgather.ErrInvalidURL, "failed to build feed request", err)
	}
	httpReq.Header.Set("User-Agent", "RenCrow-WebGather/0.1")
	resp, err := client.Do(httpReq)
	if err != nil {
		return modulewebgather.SearchResponse{}, modulewebgather.WrapError(modulewebgather.ErrFetchFailed, "failed to fetch feed", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return modulewebgather.SearchResponse{}, modulewebgather.NewError(modulewebgather.ErrHTTPStatus, fmt.Sprintf("feed returned HTTP %d", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return modulewebgather.SearchResponse{}, modulewebgather.WrapError(modulewebgather.ErrFetchFailed, "failed to read feed", err)
	}
	results := parseFeedDiscoveryResults(body, req.Limit)
	return modulewebgather.SearchResponse{
		Query:    req.Query,
		Provider: req.Provider,
		Results:  results,
		Diagnostics: map[string]any{
			"cache_hit": false,
			"error":     "",
			"feed_url":  feedURL,
		},
	}, nil
}

type rssDocument struct {
	Channel struct {
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
		} `xml:"item"`
	} `xml:"channel"`
}

type atomDocument struct {
	Entries []struct {
		Title   string `xml:"title"`
		Summary string `xml:"summary"`
		Content string `xml:"content"`
		Links   []struct {
			Href string `xml:"href,attr"`
			Rel  string `xml:"rel,attr"`
		} `xml:"link"`
	} `xml:"entry"`
}

type sitemapDocument struct {
	URLs []struct {
		Loc string `xml:"loc"`
	} `xml:"url"`
}

func parseFeedDiscoveryResults(body []byte, limit int) []modulewebgather.SearchResult {
	if limit <= 0 {
		limit = modulewebgather.DefaultSearchLimit
	}
	var out []modulewebgather.SearchResult
	var rss rssDocument
	if xml.Unmarshal(body, &rss) == nil {
		for _, item := range rss.Channel.Items {
			if strings.TrimSpace(item.Link) == "" {
				continue
			}
			out = append(out, modulewebgather.SearchResult{
				URL:          strings.TrimSpace(item.Link),
				Title:        strings.TrimSpace(item.Title),
				Snippet:      compactSnippet(item.Description),
				Rank:         len(out) + 1,
				SourceEngine: "rss_atom",
			})
			if len(out) >= limit {
				return out
			}
		}
	}
	var atom atomDocument
	if xml.Unmarshal(body, &atom) == nil {
		for _, entry := range atom.Entries {
			link := atomEntryLink(entry.Links)
			if strings.TrimSpace(link) == "" {
				continue
			}
			out = append(out, modulewebgather.SearchResult{
				URL:          strings.TrimSpace(link),
				Title:        strings.TrimSpace(entry.Title),
				Snippet:      compactSnippet(firstNonEmptyString(entry.Summary, entry.Content)),
				Rank:         len(out) + 1,
				SourceEngine: "rss_atom",
			})
			if len(out) >= limit {
				return out
			}
		}
	}
	var sitemap sitemapDocument
	if xml.Unmarshal(body, &sitemap) == nil {
		for _, item := range sitemap.URLs {
			if strings.TrimSpace(item.Loc) == "" {
				continue
			}
			out = append(out, modulewebgather.SearchResult{
				URL:          strings.TrimSpace(item.Loc),
				Title:        strings.TrimSpace(item.Loc),
				Rank:         len(out) + 1,
				SourceEngine: "sitemap",
			})
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

func atomEntryLink(links []struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}) string {
	fallback := ""
	for _, link := range links {
		if strings.TrimSpace(link.Href) == "" {
			continue
		}
		if fallback == "" {
			fallback = link.Href
		}
		if strings.TrimSpace(link.Rel) == "" || link.Rel == "alternate" {
			return link.Href
		}
	}
	return fallback
}

func compactSnippet(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
