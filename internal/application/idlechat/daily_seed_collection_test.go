package idlechat

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

func TestDailySeedCollectionSnapshotExposesCachedItemsAndConfiguredSources(t *testing.T) {
	fetchedAt := time.Date(2026, 7, 21, 4, 0, 8, 0, jst)
	withDailySeedCache(t, &DailySeedCache{
		Date:           "2026-07-21",
		WikipediaSeeds: []string{"項目A", "項目B"},
		NewsSeedItems: []NewsSeed{
			{Title: "AIニュース", Category: "ai_frontier", Source: "OpenAI News", SourceType: "rss", URL: "https://example.com/ai", SourceReadStatus: "ready", SourceReadURL: "https://example.com/ai", TermNotes: []modulechat.NewsTermNote{{Term: "LLM", Explanation: "大規模言語モデルです。", SourceKind: "article_context", Status: "contextual"}}, Summary: "要点", Perspective: "Shiroの見解: 見解"},
			{Title: "世間の反応", Category: "social", Source: "Reddit r/technology", SourceType: "reddit", URL: "https://example.com/reddit"},
		},
		FetchedAt:          fetchedAt,
		EnrichmentStatus:   "ready",
		EnrichmentProvider: "ChatWorker",
		EnrichedAt:         fetchedAt.Add(time.Minute),
	})
	orch := NewIdleChatOrchestrator(nil, nil, nil, 5, 10, 0.7, nil, "")
	orch.SetNewsSourceConfig(NewsSourceConfig{
		RedditEnabled:     true,
		RedditCommunities: []string{"technology"},
		RedditLimit:       8,
		XEnabled:          true,
		XBearerToken:      "must-not-leak",
		XQueries: []XNewsQuery{
			{Name: "X AI", Category: "ai_social", Query: "AI lang:ja -is:retweet", Limit: 10},
		},
	})

	now := time.Date(2026, 7, 21, 5, 30, 0, 0, jst)
	got := orch.DailySeedCollectionSnapshot(now)

	if got.Status != "ready" || got.SkillID != dailySourceBriefSkillID || got.Schedule != "04:00" || got.Timezone != "JST" {
		t.Fatalf("schedule status = %+v", got)
	}
	if got.FetchedAt == nil || !got.FetchedAt.Equal(fetchedAt) {
		t.Fatalf("fetched_at = %v", got.FetchedAt)
	}
	wantNext := time.Date(2026, 7, 22, 4, 0, 0, 0, jst)
	if !got.NextRunAt.Equal(wantNext) {
		t.Fatalf("next_run_at = %v want %v", got.NextRunAt, wantNext)
	}
	if got.Total != 2 || got.WikipediaCount != 2 {
		t.Fatalf("counts = total:%d wikipedia:%d", got.Total, got.WikipediaCount)
	}
	if got.CategoryCounts["ai_frontier"] != 1 || got.SourceCounts["Reddit r/technology"] != 1 {
		t.Fatalf("summaries = categories:%v sources:%v", got.CategoryCounts, got.SourceCounts)
	}
	if len(got.Items) != 2 || got.Items[0].URL != "https://example.com/ai" {
		t.Fatalf("items = %+v", got.Items)
	}
	if got.Items[0].Summary != "要点" || len(got.Items[0].TermNotes) != 1 || got.Items[0].Perspective != "Shiroの見解: 見解" {
		t.Fatalf("annotations = %+v", got.Items[0])
	}
	if got.EnrichmentStatus != "ready" || got.EnrichmentProvider != "ChatWorker" || got.EnrichedAt == nil {
		t.Fatalf("enrichment snapshot = %+v", got)
	}
	if !collectionHasSource(got.Sources, "OpenAI News", "rss_or_atom", true) {
		t.Fatalf("missing default RSS source: %+v", got.Sources)
	}
	if !collectionHasSource(got.Sources, "Reddit r/technology", "reddit_atom", true) {
		t.Fatalf("missing enabled Reddit source: %+v", got.Sources)
	}
	if !collectionHasSource(got.Sources, "X AI", "x_recent_search", true) {
		t.Fatalf("missing enabled X source: %+v", got.Sources)
	}
	if len(got.Tools) == 0 {
		t.Fatal("tools must be explicit")
	}
	tools := strings.Join(got.Tools, ",")
	if !strings.Contains(tools, "shiro_worker_llm") || !strings.Contains(tools, "web_gather.fetch") || !strings.Contains(tools, "不明語のみ") {
		t.Fatalf("tools must expose direct fetch, unknown-term search, and Shiro: %v", got.Tools)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "must-not-leak") {
		t.Fatal("collection snapshot must not expose credentials")
	}

	got.Items[0].Title = "changed"
	cache := getDailyCache()
	if cache.NewsSeedItems[0].Title != "AIニュース" {
		t.Fatal("snapshot must not expose mutable cache storage")
	}
}

func TestDailySeedCollectionSnapshotReportsEmptyCache(t *testing.T) {
	withDailySeedCache(t, nil)
	orch := NewIdleChatOrchestrator(nil, nil, nil, 5, 10, 0.7, nil, "")

	got := orch.DailySeedCollectionSnapshot(time.Date(2026, 7, 21, 3, 0, 0, 0, jst))

	if got.Status != "empty" || got.FetchedAt != nil || got.Total != 0 {
		t.Fatalf("empty snapshot = %+v", got)
	}
}

func collectionHasSource(sources []DailySeedCollectionSource, name, kind string, enabled bool) bool {
	for _, source := range sources {
		if source.Name == name && source.Kind == kind && source.Enabled == enabled {
			return true
		}
	}
	return false
}
