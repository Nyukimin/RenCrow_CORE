package webgather

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	modulewebgather "github.com/Nyukimin/picoclaw_multiLLM/modules/webgather"
)

func TestFeedDiscoveryProviderParsesRSSAndSitemap(t *testing.T) {
	provider := NewFeedDiscoveryProvider()
	provider.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `<rss><channel><item><title>One</title><link>https://example.com/one</link><description>First item</description></item></channel></rss>`
		if r.URL.Path == "/sitemap.xml" {
			body = `<urlset><url><loc>https://example.com/two</loc></url></urlset>`
		}
		return &http.Response{
			StatusCode: 200,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}
	rss, err := provider.Search(context.Background(), modulewebgather.SearchRequest{
		Query:    "https://example.com/feed.xml",
		Provider: "rss_atom",
		Limit:    5,
	})
	if err != nil {
		t.Fatalf("rss Search failed: %v", err)
	}
	if len(rss.Results) != 1 || rss.Results[0].URL != "https://example.com/one" || rss.Results[0].SourceEngine != "rss_atom" {
		t.Fatalf("unexpected rss results: %+v", rss.Results)
	}

	sitemap, err := provider.Search(context.Background(), modulewebgather.SearchRequest{
		Query:    "https://example.com/sitemap.xml",
		Provider: "sitemap",
		Limit:    5,
	})
	if err != nil {
		t.Fatalf("sitemap Search failed: %v", err)
	}
	if len(sitemap.Results) != 1 || sitemap.Results[0].URL != "https://example.com/two" || sitemap.Results[0].SourceEngine != "sitemap" {
		t.Fatalf("unexpected sitemap results: %+v", sitemap.Results)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
