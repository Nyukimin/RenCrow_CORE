package l1sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestL1SQLiteStoreRecentNewsItemsBySource(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	for i, sourceID := range []string{"rss:busy", "rss:busy", "rss:quiet"} {
		published := base.Add(-time.Duration(i) * time.Minute)
		if _, err := store.db.ExecContext(ctx, `
INSERT INTO l1_news_item (
	id, staging_id, category, source_id, source_url, published_at, fetched_at,
	raw_text, raw_hash, summary_draft, keywords_json, license_note, meta_json,
	created_at, updated_at
) VALUES (?, ?, 'ai', ?, ?, ?, ?, ?, ?, ?, '[]', 'rss', '{}', ?, ?)
`, sourceID+published.Format(time.RFC3339), "staging:"+sourceID+published.Format(time.RFC3339), sourceID,
			"https://example.com/"+sourceID+published.Format("150405"), published, published,
			"body "+sourceID+published.Format(time.RFC3339), "hash "+sourceID+published.Format(time.RFC3339),
			"summary", published, published); err != nil {
			t.Fatalf("insert news item failed: %v", err)
		}
	}
	items, err := store.RecentNewsItemsBySource(ctx, "ai", 1, 100)
	if err != nil {
		t.Fatalf("RecentNewsItemsBySource failed: %v", err)
	}
	if len(items) != 2 || items[0].SourceID != "rss:busy" || items[1].SourceID != "rss:quiet" {
		t.Fatalf("expected one recent item per source: %+v", items)
	}
}

func TestL1SQLiteStoreRecentNewsItemsSinceUsesOnlyTimeBoundary(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	rows := []struct {
		id        string
		sourceID  string
		published time.Time
	}{
		{id: "recent-busy-1", sourceID: "rss:busy", published: base.Add(-time.Hour)},
		{id: "recent-busy-2", sourceID: "rss:busy", published: base.Add(-2 * time.Hour)},
		{id: "recent-quiet", sourceID: "rss:quiet", published: base.Add(-23 * time.Hour)},
		{id: "expired", sourceID: "rss:old", published: base.Add(-25 * time.Hour)},
	}
	for _, row := range rows {
		if _, err := store.db.ExecContext(ctx, `
INSERT INTO l1_news_item (
	id, staging_id, category, source_id, source_url, published_at, fetched_at,
	raw_text, raw_hash, summary_draft, keywords_json, license_note, meta_json,
	created_at, updated_at
) VALUES (?, ?, 'ai', ?, ?, ?, ?, ?, ?, 'summary', '[]', 'rss', '{}', ?, ?)
`, row.id, "staging:"+row.id, row.sourceID, "https://example.com/"+row.id,
			row.published, row.published, "body "+row.id, "hash "+row.id,
			row.published, row.published); err != nil {
			t.Fatalf("insert news item failed: %v", err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO l1_news_item (
	id, staging_id, category, source_id, source_url, published_at, fetched_at,
	raw_text, raw_hash, summary_draft, keywords_json, license_note, meta_json,
	created_at, updated_at
) VALUES ('duplicate-url', 'staging:duplicate-url', 'ai', 'rss:mirror',
	'https://example.com/recent-busy-1', ?, ?, 'duplicate body', 'duplicate hash',
	'duplicate summary', '[]', 'rss', '{}', ?, ?)
`, base.Add(-3*time.Hour), base.Add(-3*time.Hour), base.Add(-3*time.Hour), base.Add(-3*time.Hour)); err != nil {
		t.Fatalf("insert duplicate URL failed: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO l1_news_item (
	id, staging_id, category, source_id, source_url, published_at, fetched_at,
	raw_text, raw_hash, summary_draft, keywords_json, license_note, meta_json,
	created_at, updated_at
) VALUES ('recent-sports', 'staging:recent-sports', 'sports', 'rss:sports',
	'https://example.com/recent-sports', ?, ?, 'sports body', 'sports hash',
	'sports summary', '[]', 'rss', '{}', ?, ?)
`, base.Add(-30*time.Minute), base.Add(-30*time.Minute), base.Add(-30*time.Minute), base.Add(-30*time.Minute)); err != nil {
		t.Fatalf("insert sports item failed: %v", err)
	}

	items, err := store.RecentNewsItemsSince(ctx, "ai", base.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("RecentNewsItemsSince failed: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected all three items in the time window, got %+v", items)
	}
	if items[0].ID != "recent-busy-1" || items[1].ID != "recent-busy-2" || items[2].ID != "recent-quiet" {
		t.Fatalf("unexpected time-window ordering or source cap: %+v", items)
	}
	sportsItems, err := store.RecentNewsItemsSince(ctx, "sports", base.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("RecentNewsItemsSince sports query failed: %v", err)
	}
	if len(sportsItems) != 0 {
		t.Fatalf("sports must be excluded from NewsPack: %+v", sportsItems)
	}
}
