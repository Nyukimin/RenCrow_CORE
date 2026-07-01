package feed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mmcdole/gofeed"
)

func TestRSSParserExtractFromItem(t *testing.T) {
	parser := NewRSSParser([]string{"https://example.test/feed.xml"})
	item := parser.extractFromItem(&gofeed.Item{
		Title:       "OpenAI Company releases RenCrow update",
		Description: "RenCrow now stores glossary context.",
	}, "https://example.test/feed.xml")

	if item == nil {
		t.Fatal("expected glossary item")
	}
	if item.Term != "OpenAI" {
		t.Fatalf("expected first capitalized term, got %q", item.Term)
	}
	if item.Category != "organization" {
		t.Fatalf("expected organization category, got %q", item.Category)
	}
	if !strings.Contains(item.Explanation, "RenCrow now stores glossary context") {
		t.Fatalf("description should be used as explanation context, got %q", item.Explanation)
	}
}

func TestRSSParserExtractFromDescriptionFallbackAndCategories(t *testing.T) {
	parser := NewRSSParser(nil)
	item := parser.extractFromItem(&gofeed.Item{
		Title:       "Kyoto city appears in the source",
		Description: "lowercase description context.",
	}, "feed")

	if item == nil {
		t.Fatal("expected glossary item from description fallback")
	}
	if item.Term != "Kyoto" {
		t.Fatalf("expected description term, got %q", item.Term)
	}
	if item.Category != "location" {
		t.Fatalf("expected location category, got %q", item.Category)
	}

	if got := parser.determineCategory("RenCrow", "new assistant concept"); got != "new_word" {
		t.Fatalf("default category = %q", got)
	}
	if got := parser.extractFromItem(&gofeed.Item{Title: "and or but", Description: "also none"}, "feed"); got != nil {
		t.Fatalf("expected nil when no potential terms exist, got %#v", got)
	}
}

func TestTruncateString(t *testing.T) {
	if got := truncateString("short", 10); got != "short" {
		t.Fatalf("short string changed: %q", got)
	}
	if got := truncateString("0123456789abcdef", 5); got != "01234..." {
		t.Fatalf("unexpected truncation: %q", got)
	}
}

func TestRSSParserFetchAndParseSkipsFailedFeeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?>
<rss version="2.0">
  <channel>
    <title>Glossary</title>
    <item>
      <title>RenCrow Company glossary update</title>
      <description>RenCrow stores terms.</description>
    </item>
  </channel>
</rss>`))
	}))
	defer server.Close()

	parser := NewRSSParser([]string{"http://127.0.0.1:1/missing", server.URL})
	items, err := parser.FetchAndParse(context.Background())
	if err != nil {
		t.Fatalf("FetchAndParse failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one parsed item, got %#v", items)
	}
	if items[0].Term != "RenCrow" || items[0].Category != "organization" {
		t.Fatalf("unexpected parsed item: %#v", items[0])
	}
}
