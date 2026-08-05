package l1sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestL1SQLiteStore_XBookmarkStagingPageFiltersAndSummarizes(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	save := func(eventID, title, text, collection string, tags []map[string]interface{}, needsReview bool, references ...map[string]interface{}) {
		t.Helper()
		_, saveErr := store.SaveStagingItem(ctx, L1StagingItem{
			Kind:      L1StagingKindExternalFetch,
			Namespace: "kb:general",
			EventID:   eventID,
			SourceID:  "x:bookmarks_vault_migration",
			SourceURL: "https://x.com/example/status/" + eventID,
			RawText:   text,
			Meta: map[string]interface{}{
				"collection":    collection,
				"title":         title,
				"use_case_tags": tags,
				"classification": map[string]interface{}{
					"method":       "rules",
					"needs_review": needsReview,
				},
				"references": references,
			},
		})
		if saveErr != nil {
			t.Fatalf("SaveStagingItem(%s) failed: %v", eventID, saveErr)
		}
	}

	save("100", "AI prompt", "Codex prompt recipe", "x_bookmark", []map[string]interface{}{{"major": "ai", "minor": "ai_tip", "confidence": 0.91}}, true,
		map[string]interface{}{"kind": "external_url", "url": "https://example.com/article", "page_title": "Agent article", "body_text": "linked reference body"})
	save("200", "株式メモ", "一次資料を確認する", "x_bookmark", []map[string]interface{}{{"major": "finance", "minor": "equity_research", "confidence": 0.88}}, false)
	save("300", "通常ニュース", "Bookmarkではない", "daily_news", []map[string]interface{}{{"major": "research", "minor": "news_watch", "confidence": 0.9}}, false)

	page, err := store.XBookmarkStagingPage(ctx, L1XBookmarkViewQuery{
		Major:  "ai",
		Minor:  "ai_tip",
		Review: L1XBookmarkReviewNeedsReview,
		Search: "prompt",
		Limit:  12,
	})
	if err != nil {
		t.Fatalf("XBookmarkStagingPage failed: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].EventID != "100" {
		t.Fatalf("unexpected filtered page: %+v", page)
	}
	if page.Summary.Total != 2 || page.Summary.NeedsReview != 1 {
		t.Fatalf("summary must cover every X Bookmark: %+v", page.Summary)
	}
	if page.Summary.MajorCounts["ai"] != 1 || page.Summary.MajorCounts["finance"] != 1 {
		t.Fatalf("unexpected major counts: %+v", page.Summary.MajorCounts)
	}
	if page.Summary.MinorCounts["ai_tip"] != 1 || page.Summary.MinorCounts["equity_research"] != 1 {
		t.Fatalf("unexpected minor counts: %+v", page.Summary.MinorCounts)
	}

	referencePage, err := store.XBookmarkStagingPage(ctx, L1XBookmarkViewQuery{Search: "linked reference body", Limit: 12})
	if err != nil {
		t.Fatalf("XBookmarkStagingPage reference search failed: %v", err)
	}
	if referencePage.Total != 1 || len(referencePage.Items) != 1 || referencePage.Items[0].EventID != "100" {
		t.Fatalf("reference body must be searchable: %+v", referencePage)
	}

	secondPage, err := store.XBookmarkStagingPage(ctx, L1XBookmarkViewQuery{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("XBookmarkStagingPage second page failed: %v", err)
	}
	if secondPage.Total != 2 || len(secondPage.Items) != 1 {
		t.Fatalf("unexpected pagination: %+v", secondPage)
	}
}

func TestL1SQLiteStore_XBookmarkStagingPageRejectsInvalidQuery(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	for _, query := range []L1XBookmarkViewQuery{
		{Review: "unknown"},
		{Limit: 51},
		{Offset: -1},
	} {
		if _, err := store.XBookmarkStagingPage(context.Background(), query); err == nil {
			t.Fatalf("expected invalid query to fail: %+v", query)
		}
	}
}

func TestL1SQLiteStore_XBookmarkWorkflowSourceReturnsStructuredSourceWithoutMutation(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	staged, err := store.SaveStagingItem(ctx, L1StagingItem{
		Kind: L1StagingKindExternalFetch, Namespace: "kb:general", EventID: "workflow-source",
		SourceID: "x:bookmarks_browser", SourceURL: "https://x.com/example/status/123", RawText: "prompt body",
		Meta: map[string]interface{}{
			"collection": "x_bookmark", "title": "prompt title",
			"author":     map[string]interface{}{"name": "Alice", "username": "alice"},
			"media":      []map[string]interface{}{{"type": "image", "url": "https://pbs.twimg.com/media/one.jpg", "alt": "青い空"}},
			"references": []map[string]interface{}{{"kind": "external_url", "url": "https://example.com/article", "page_title": "一次資料"}},
		},
	})
	if err != nil {
		t.Fatalf("SaveStagingItem failed: %v", err)
	}

	source, err := store.XBookmarkWorkflowSource(ctx, staged.ID)
	if err != nil {
		t.Fatalf("XBookmarkWorkflowSource failed: %v", err)
	}
	if source.ID != staged.ID || source.Title != "prompt title" || source.AuthorUsername != "alice" {
		t.Fatalf("unexpected source: %+v", source)
	}
	if len(source.Media) != 1 || source.Media[0].Alt != "青い空" || len(source.References) != 1 || source.References[0]["page_title"] != "一次資料" {
		t.Fatalf("structured source projection missing: %+v", source)
	}
	remaining, err := store.RecentStagingItems(ctx, L1StagingStatusPending, 10)
	if err != nil || len(remaining) != 1 || remaining[0].ID != staged.ID {
		t.Fatalf("workflow source read mutated staging: items=%+v err=%v", remaining, err)
	}
}
