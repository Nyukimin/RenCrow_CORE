package newsbrief

import (
	"context"
	"testing"
	"time"

	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

type l1NewsReaderStoreStub struct {
	items []l1sqlite.L1NewsItem
}

func (s l1NewsReaderStoreStub) RecentNewsItems(context.Context, string, int) ([]l1sqlite.L1NewsItem, error) {
	return s.items, nil
}

func TestL1ReaderReturnsExpectedMorningDateOnly(t *testing.T) {
	now := time.Date(2026, 8, 3, 1, 0, 0, 0, newsJST)
	reader := NewL1Reader(l1NewsReaderStoreStub{items: []l1sqlite.L1NewsItem{
		{ID: "expected", Category: "tech", SourceID: "rss:news:test", SourceURL: "https://example.com/expected", PublishedAt: time.Date(2026, 8, 2, 12, 0, 0, 0, newsJST), RawText: "見出し\n記事本文", SummaryDraft: "feed要約", Meta: map[string]interface{}{"source_name": "Test", "source_kind": "rss", "feed_item_title": "見出し"}},
		{ID: "old", Category: "tech", SourceID: "rss:news:test", SourceURL: "https://example.com/old", PublishedAt: time.Date(2026, 8, 1, 12, 0, 0, 0, newsJST), SummaryDraft: "Old"},
	}})
	brief, err := reader.Read(context.Background(), now)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if !brief.IsUsable(now) || len(brief.Items) != 1 || brief.Items[0].ID != "expected" {
		t.Fatalf("unexpected brief: %+v", brief)
	}
	if brief.Source != domainnews.SourcePersistent {
		t.Fatalf("source = %q", brief.Source)
	}
	if brief.Items[0].Title != "見出し" || brief.Items[0].Summary != "記事本文" || brief.Items[0].Source != "Test" {
		t.Fatalf("unexpected item mapping: %+v", brief.Items[0])
	}
}

func TestFallbackReaderUsesPersistentBriefWhenPrimaryIsStale(t *testing.T) {
	now := time.Date(2026, 8, 3, 8, 0, 0, 0, newsJST)
	stale := domainnews.ReaderFunc(func(context.Context, time.Time) (domainnews.DailyNewsBrief, error) {
		return domainnews.DailyNewsBrief{Date: "2026-08-02", Source: domainnews.SourceScheduled, Items: []domainnews.Item{{ID: "stale"}}, EnrichmentStatus: domainnews.EnrichmentReady}, nil
	})
	persistent := domainnews.ReaderFunc(func(context.Context, time.Time) (domainnews.DailyNewsBrief, error) {
		return domainnews.DailyNewsBrief{Date: "2026-08-03", Source: domainnews.SourceScheduled, Items: []domainnews.Item{{ID: "fresh"}}, EnrichmentStatus: domainnews.EnrichmentPartial}, nil
	})
	brief, err := NewFallbackReader(stale, persistent).Read(context.Background(), now)
	if err != nil || len(brief.Items) != 1 || brief.Items[0].ID != "fresh" {
		t.Fatalf("unexpected fallback brief: %+v err=%v", brief, err)
	}
}
