package l1sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestL1SQLiteStoreNewsArticleFetchLedger(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	entry, claimed, err := store.ClaimNewsArticleFetch(ctx, "https://Example.com/story#section", now)
	if err != nil || !claimed || entry.AttemptCount != 1 {
		t.Fatalf("first claim failed: entry=%+v claimed=%v err=%v", entry, claimed, err)
	}
	if _, claimed, err := store.ClaimNewsArticleFetch(ctx, "https://example.com/story", now.Add(time.Second)); err != nil || claimed {
		t.Fatalf("active claim must not be duplicated: claimed=%v err=%v", claimed, err)
	}
	if err := store.CompleteNewsArticleFetch(ctx, "https://example.com/story", L1NewsArticleFetchCompletion{
		FinalURL: "https://example.com/story", ContentType: "text/html", FetchProvider: "http",
		FetchURL: "https://reader.example/http://example.com/story", ContentSHA256: "sha256-value",
		Extractor: "html_basic", RawBytes: 123, ArticleText: "Complete article.",
	}, now.Add(2*time.Second)); err != nil {
		t.Fatalf("CompleteNewsArticleFetch failed: %v", err)
	}
	ready, claimed, err := store.ClaimNewsArticleFetch(ctx, "https://EXAMPLE.com/story#other", now.Add(24*time.Hour))
	if err != nil || claimed || ready == nil || ready.Status != L1NewsArticleFetchStatusReady || ready.ArticleText != "Complete article." || ready.AttemptCount != 1 {
		t.Fatalf("ready article must be permanent and reusable: entry=%+v claimed=%v err=%v", ready, claimed, err)
	}
	if ready.FetchURL != "https://reader.example/http://example.com/story" || ready.ContentSHA256 != "sha256-value" {
		t.Fatalf("ready article provenance was not persisted: %+v", ready)
	}
}

func TestCompleteNewsArticleFetchSynchronizesAllNewsRowsForURL(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	const articleURL = "https://example.com/shared-story"
	for i, sourceID := range []string{"rss:top", "rss:business"} {
		metaJSON, err := json.Marshal(map[string]interface{}{
			"feed_item_title":      "Shared headline",
			"article_fetch_status": L1NewsArticleFetchStatusUnavailable,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `
INSERT INTO l1_news_item (
	id, staging_id, category, source_id, source_url, published_at, fetched_at,
	raw_text, raw_hash, summary_draft, keywords_json, license_note, meta_json,
	created_at, updated_at
) VALUES (?, ?, 'business', ?, ?, ?, ?, ?, ?, '', '[]', '', ?, ?, ?)
`, "news-row-"+sourceID, "staging-row-"+sourceID, sourceID, articleURL, now, now,
			"Shared headline\nTruncated RSS summary…", rawTextHash("Shared headline\nTruncated RSS summary…"), string(metaJSON), now.Add(time.Duration(i)*time.Second), now); err != nil {
			t.Fatalf("failed to insert news row: %v", err)
		}
	}
	if _, claimed, err := store.ClaimNewsArticleFetch(ctx, articleURL, now); err != nil || !claimed {
		t.Fatalf("claim failed: claimed=%v err=%v", claimed, err)
	}
	completion := L1NewsArticleFetchCompletion{
		FinalURL: articleURL, FetchURL: "https://r.jina.ai/http://example.com/shared-story", ContentType: "text/plain", FetchProvider: "jina_reader",
		Extractor: "html_basic", RawBytes: 456, ArticleText: "Complete article body with its final sentence.",
		ContentSHA256: "content-sha256",
	}
	if err := store.CompleteNewsArticleFetch(ctx, articleURL, completion, now.Add(time.Second)); err != nil {
		t.Fatalf("CompleteNewsArticleFetch failed: %v", err)
	}
	rows, err := store.db.QueryContext(ctx, `
SELECT raw_text, raw_hash, meta_json
FROM l1_news_item
WHERE source_url = ?
ORDER BY source_id
`, articleURL)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		count++
		var rawText, rawHash, metaJSON string
		if err := rows.Scan(&rawText, &rawHash, &metaJSON); err != nil {
			t.Fatal(err)
		}
		if rawText != "Shared headline\n"+completion.ArticleText || rawHash != rawTextHash(rawText) {
			t.Fatalf("news body was not synchronized: raw=%q hash=%q", rawText, rawHash)
		}
		var meta map[string]interface{}
		if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
			t.Fatal(err)
		}
		if meta["article_fetch_status"] != L1NewsArticleFetchStatusReady || int(meta["article_extracted_chars"].(float64)) != len([]rune(completion.ArticleText)) {
			t.Fatalf("news meta was not synchronized: %#v", meta)
		}
		if meta["article_original_url"] != articleURL || meta["article_fetch_url"] != completion.FetchURL || meta["article_content_sha256"] != completion.ContentSHA256 || meta["article_fetched_at"] == nil {
			t.Fatalf("news provenance was not synchronized: %#v", meta)
		}
	}
	if count != 2 {
		t.Fatalf("expected both news rows to remain, got %d", count)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	staleMeta, _ := json.Marshal(map[string]interface{}{
		"feed_item_title": "Shared headline", "article_fetch_status": L1NewsArticleFetchStatusUnavailable,
	})
	if _, err := store.db.ExecContext(ctx, `
UPDATE l1_news_item
SET raw_text = 'Shared headline
Truncated again…', meta_json = ?
WHERE id = 'news-row-rss:business'
`, string(staleMeta)); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimNewsArticleFetch(ctx, articleURL, now.Add(time.Hour)); err != nil || claimed {
		t.Fatalf("ready cache reuse failed: claimed=%v err=%v", claimed, err)
	}
	var reusedRawText, reusedMetaJSON string
	if err := store.db.QueryRowContext(ctx, `
SELECT raw_text, meta_json FROM l1_news_item WHERE id = 'news-row-rss:business'
`).Scan(&reusedRawText, &reusedMetaJSON); err != nil {
		t.Fatal(err)
	}
	var reusedMeta map[string]interface{}
	if err := json.Unmarshal([]byte(reusedMetaJSON), &reusedMeta); err != nil {
		t.Fatal(err)
	}
	if reusedRawText != "Shared headline\n"+completion.ArticleText || reusedMeta["article_fetch_status"] != L1NewsArticleFetchStatusReady {
		t.Fatalf("ready cache did not repair stale duplicate row: raw=%q meta=%#v", reusedRawText, reusedMeta)
	}
}

func TestL1SQLiteStoreNewsArticleFetchRetriesOnlyAfterBackoff(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	if _, claimed, err := store.ClaimNewsArticleFetch(ctx, "https://example.com/retry", now); err != nil || !claimed {
		t.Fatalf("first claim failed: claimed=%v err=%v", claimed, err)
	}
	if err := store.FailNewsArticleFetch(ctx, "https://example.com/retry", "http_status_error", now, 5*time.Minute); err != nil {
		t.Fatalf("FailNewsArticleFetch failed: %v", err)
	}
	if _, claimed, err := store.ClaimNewsArticleFetch(ctx, "https://example.com/retry", now.Add(4*time.Minute)); err != nil || claimed {
		t.Fatalf("backoff must prevent an early retry: claimed=%v err=%v", claimed, err)
	}
	entry, claimed, err := store.ClaimNewsArticleFetch(ctx, "https://example.com/retry", now.Add(5*time.Minute))
	if err != nil || !claimed || entry.AttemptCount != 2 {
		t.Fatalf("retry must be claimable after backoff: entry=%+v claimed=%v err=%v", entry, claimed, err)
	}
}

func TestL1SQLiteStoreReopensIncompleteReadyArticle(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	url := "https://example.com/incomplete"
	if _, claimed, err := store.ClaimNewsArticleFetch(ctx, url, now); err != nil || !claimed {
		t.Fatalf("claim failed: claimed=%v err=%v", claimed, err)
	}
	if err := store.CompleteNewsArticleFetch(ctx, url, L1NewsArticleFetchCompletion{ArticleText: "title only"}, now); err != nil {
		t.Fatalf("complete failed: %v", err)
	}
	if err := store.ReopenIncompleteNewsArticleFetch(ctx, url, 200, now.Add(time.Minute)); err != nil {
		t.Fatalf("ReopenIncompleteNewsArticleFetch failed: %v", err)
	}
	entry, claimed, err := store.ClaimNewsArticleFetch(ctx, url, now.Add(time.Minute))
	if err != nil || !claimed || entry.AttemptCount != 2 {
		t.Fatalf("incomplete ready article must be repairable: entry=%+v claimed=%v err=%v", entry, claimed, err)
	}
}

func TestL1SQLiteStoreKeepsShortNHKArticleReady(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	url := "https://news.web.nhk/newsweb/na/na-k10015196931000"
	if _, claimed, err := store.ClaimNewsArticleFetch(ctx, url, now); err != nil || !claimed {
		t.Fatalf("claim failed: claimed=%v err=%v", claimed, err)
	}
	if err := store.CompleteNewsArticleFetch(ctx, url, L1NewsArticleFetchCompletion{
		Extractor: "nhk_news_article", ArticleText: "省略記号なしで完結する短いNHK記事です。",
	}, now); err != nil {
		t.Fatalf("complete failed: %v", err)
	}
	if err := store.ReopenIncompleteNewsArticleFetch(ctx, url, 200, now.Add(time.Minute)); err != nil {
		t.Fatalf("ReopenIncompleteNewsArticleFetch failed: %v", err)
	}
	entry, claimed, err := store.ClaimNewsArticleFetch(ctx, url, now.Add(time.Hour))
	if err != nil || claimed || entry == nil || entry.Status != L1NewsArticleFetchStatusReady {
		t.Fatalf("short NHK article must remain reusable: entry=%+v claimed=%v err=%v", entry, claimed, err)
	}
}
