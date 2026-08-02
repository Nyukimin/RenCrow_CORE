package idlechat

import (
	"context"
	"testing"
	"time"

	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

func TestIdleChatReadDailyNewsBriefReturnsOnlyFactualNewsItems(t *testing.T) {
	fetchedAt := time.Date(2026, 7, 21, 4, 0, 8, 0, jst)
	withDailySeedCache(t, &DailySeedCache{
		Date: "2026-07-21",
		NewsSeedItems: []NewsSeed{
			{
				Title:      "報道記事",
				Category:   "technology",
				Source:     "公式RSS",
				SourceType: "rss",
				URL:        "https://example.com/article",
				Summary:    "記事の要約",
				TermNotes:  []modulechat.NewsTermNote{{Term: "LLM", Explanation: "大規模言語モデル", Status: "contextual"}},
			},
			{Title: "SNSの話題", Source: "Reddit", SourceType: "reddit", URL: "https://example.com/social"},
		},
		FetchedAt:          fetchedAt,
		EnrichmentStatus:   domainnews.EnrichmentReady,
		EnrichmentProvider: "ChatWorker",
		EnrichedAt:         fetchedAt.Add(time.Minute),
	})

	orch := NewIdleChatOrchestrator(nil, nil, nil, 5, 10, 0.7, nil, "")
	now := time.Date(2026, 7, 21, 5, 30, 0, 0, jst)
	brief, err := orch.Read(context.Background(), now)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if brief.Status != domainnews.StatusReady || brief.Date != "2026-07-21" {
		t.Fatalf("brief metadata = %+v", brief)
	}
	if len(brief.Items) != 1 || brief.Items[0].Title != "報道記事" {
		t.Fatalf("items = %+v", brief.Items)
	}
	if brief.Items[0].ID == "" || len(brief.Items[0].TermNotes) != 1 {
		t.Fatalf("item provenance = %+v", brief.Items[0])
	}

	brief.Items[0].Title = "mutated"
	if got := getDailyCache().NewsSeedItems[0].Title; got != "報道記事" {
		t.Fatalf("Read must not expose mutable cache storage: %q", got)
	}
}

func TestIdleChatReadDailyNewsBriefReportsStaleBeforeExpectedMorningDate(t *testing.T) {
	withDailySeedCache(t, &DailySeedCache{
		Date:             "2026-07-19",
		NewsSeedItems:    []NewsSeed{{Title: "前日の記事", SourceType: "rss"}},
		EnrichmentStatus: domainnews.EnrichmentReady,
	})
	orch := NewIdleChatOrchestrator(nil, nil, nil, 5, 10, 0.7, nil, "")

	now := time.Date(2026, 7, 21, 3, 30, 0, 0, jst)
	brief, err := orch.Read(context.Background(), now)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if brief.Status != domainnews.StatusStale || brief.IsUsable(now) {
		t.Fatalf("stale brief = %+v", brief)
	}
}
