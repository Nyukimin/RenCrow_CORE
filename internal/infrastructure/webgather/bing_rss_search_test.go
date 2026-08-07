package webgather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

func TestBingRSSSearchProviderUnwrapsNewsLinksAndEnforcesFreshness(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "AI ニュース" || r.URL.Query().Get("format") != "rss" {
			t.Fatalf("unexpected query: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss><channel>
<item><title>Fresh</title><link>https://www.bing.com/news/apiclick.aspx?url=https%3A%2F%2Fexample.com%2Ffresh</link><description>fresh summary</description><pubDate>Sun, 02 Aug 2026 12:00:00 GMT</pubDate></item>
<item><title>Old</title><link>https://example.com/old</link><description>old summary</description><pubDate>Mon, 20 Jul 2026 12:00:00 GMT</pubDate></item>
</channel></rss>`))
	}))
	defer server.Close()

	provider := NewBingNewsRSSSearchProvider()
	provider.endpoint = server.URL
	provider.now = func() time.Time { return time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC) }
	resp, err := provider.Search(context.Background(), modulewebgather.SearchRequest{
		Query: "AI ニュース", Provider: "bing_news_rss", Limit: 5, Language: "ja", Freshness: "day",
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].URL != "https://example.com/fresh" {
		t.Fatalf("unexpected results: %+v", resp.Results)
	}
	if resp.Results[0].SourceEngine != "bing_news_rss" || resp.Results[0].PublishedAt == "" {
		t.Fatalf("missing news metadata: %+v", resp.Results[0])
	}
	if resp.Diagnostics["stale_items_skipped"] != 1 {
		t.Fatalf("unexpected diagnostics: %+v", resp.Diagnostics)
	}
}

func TestBingRSSSearchProviderReturnsGenericWebResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss><channel>
<item><title>RenCrow</title><link>https://example.com/rencrow</link><description>Go runtime</description></item>
</channel></rss>`))
	}))
	defer server.Close()
	provider := NewBingRSSSearchProvider()
	provider.endpoint = server.URL
	resp, err := provider.Search(context.Background(), modulewebgather.SearchRequest{Query: "RenCrow", Provider: "bing_rss", Limit: 1})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(resp.Results) != 1 || !strings.Contains(resp.Results[0].Snippet, "Go runtime") {
		t.Fatalf("unexpected results: %+v", resp.Results)
	}
}
