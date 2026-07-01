package chat

import (
	"strings"
	"testing"
)

func TestSelectExternalTopicSeedUsesWikipediaMaterial(t *testing.T) {
	cache := &DailySeedCache{WikipediaSeeds: []string{"地下鉄博物館", "活版印刷"}}
	seed, ok := SelectExternalTopicSeed(cache, 1, "織物")
	if !ok {
		t.Fatalf("SelectExternalTopicSeed ok = false")
	}
	if seed.Category != TopicCategoryExternal || seed.Genre1 != "織物" || seed.ExternalMaterial == nil {
		t.Fatalf("unexpected seed: %+v", seed)
	}
	if seed.ExternalMaterial.Title != "活版印刷" || seed.ExternalMaterial.Provider != "Wikipedia" || seed.ExternalMaterial.Category != "wikipedia_random" {
		t.Fatalf("external material mismatch: %+v", seed.ExternalMaterial)
	}
}

func TestSelectNewsTopicSeedPrefersStructuredSeeds(t *testing.T) {
	cache := &DailySeedCache{
		NewsSeeds: []string{"legacy"},
		NewsSeedItems: []NewsSeed{
			{Title: "新しい医療制度の検討が始まる", Category: "domestic", Source: "NHK"},
		},
	}
	seed, ok := SelectNewsTopicSeed(cache, 0)
	if !ok {
		t.Fatalf("SelectNewsTopicSeed ok = false")
	}
	if seed.Category != TopicCategoryNews || seed.News == nil {
		t.Fatalf("unexpected seed: %+v", seed)
	}
	if seed.News.Title != "新しい医療制度の検討が始まる" || seed.News.Category != "domestic" || seed.News.Source != "NHK" {
		t.Fatalf("news seed mismatch: %+v", seed.News)
	}
}

func TestSelectNewsTopicSeedKeepsLegacyTitles(t *testing.T) {
	cache := &DailySeedCache{NewsSeeds: []string{"新しい医療制度の検討が始まる"}}
	seed, ok := SelectNewsTopicSeed(cache, 0)
	if !ok || seed.News == nil || seed.News.Title != "新しい医療制度の検討が始まる" {
		t.Fatalf("legacy news seed mismatch: ok=%v seed=%+v", ok, seed)
	}
}

func TestNewsSeedLabelsTitlesAndSummary(t *testing.T) {
	seeds := []NewsSeed{
		{Title: " A ", Category: "tech", Source: "ITmedia"},
		{Title: "", Category: "tech", Source: "ITmedia"},
		{Title: "B", Category: "business", Source: "ITmedia"},
		{Title: "C"},
	}
	if got := NewsSeedSourceLabel(seeds[0]); got != "News:tech:ITmedia:A" {
		t.Fatalf("label = %q", got)
	}
	if got := NewsSeedSourceLabel(seeds[3]); got != "News:C" {
		t.Fatalf("legacy label = %q", got)
	}
	if got := strings.Join(NewsSeedTitles(seeds), ","); got != "A,B,C" {
		t.Fatalf("titles = %q", got)
	}
	if got := NewsSeedCategorySummary(seeds); got != "tech=2,business=1,unknown=1" {
		t.Fatalf("summary = %q", got)
	}
}

func TestParseNewsSeedsExtractsCategorySourceAndURL(t *testing.T) {
	const rss = `<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0">
  <channel>
    <item>
      <title>新型端末の省電力技術を発表</title>
      <link>https://example.test/tech/1</link>
    </item>
    <item>
      <title>  </title>
      <link>https://example.test/empty</link>
    </item>
    <item>
      <title>生成AIの教育利用が広がる</title>
      <link>https://example.test/tech/2</link>
    </item>
  </channel>
</rss>`

	got, err := ParseNewsSeeds(strings.NewReader(rss), NewsSeedSource{
		Category: "tech",
		Name:     "Example Tech",
		URL:      "https://example.test/rss.xml",
	}, 1)
	if err != nil {
		t.Fatalf("ParseNewsSeeds error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("news seeds len = %d, want 1: %+v", len(got), got)
	}
	if got[0].Title != "新型端末の省電力技術を発表" ||
		got[0].Category != "tech" ||
		got[0].Source != "Example Tech" ||
		got[0].URL != "https://example.test/tech/1" {
		t.Fatalf("news seed mismatch: %+v", got[0])
	}
}

func TestRecentTopicRecordsDropsBlankTopics(t *testing.T) {
	got := RecentTopicRecords([]string{" A ", "", " B "})
	if len(got) != 2 || got[0].Topic != "A" || got[1].Topic != "B" {
		t.Fatalf("recent topics = %+v", got)
	}
}
