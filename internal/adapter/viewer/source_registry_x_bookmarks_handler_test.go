package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

func TestHandleSourceRegistry_XBookmarksIsReadOnlyAndExposesClassification(t *testing.T) {
	ctx := context.Background()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	staged, err := store.SaveStagingItem(ctx, l1sqlite.L1StagingItem{
		Kind:         l1sqlite.L1StagingKindExternalFetch,
		Namespace:    "kb:general",
		EventID:      "x-viewer-1",
		SourceID:     "x:bookmarks_vault_migration",
		SourceURL:    "https://x.com/example/status/1",
		RawText:      "画像生成promptの本文",
		SummaryDraft: "画像生成prompt",
		Meta: map[string]interface{}{
			"collection": "x_bookmark",
			"title":      "画像生成prompt",
			"use_case_tags": []map[string]interface{}{{
				"major": "creative", "minor": "image_prompt", "confidence": 0.97, "method": "rules", "evidence": []string{"text:prompt"},
			}},
			"classification": map[string]interface{}{"method": "rules", "needs_review": false},
			"media": []map[string]interface{}{{
				"type": "image", "url": "https://pbs.twimg.com/media/prompt.jpg", "alt": "青い図書館", "poster": "",
			}},
			"references": []map[string]interface{}{{
				"kind": "external_url", "url": "https://example.com/source", "resolved_url": "https://example.com/article",
				"capture_status": "content_fetched", "page_title": "取得済み記事", "page_description": "記事の説明",
				"body_text": "外部リンク本文", "body_char_count": 8, "body_truncated": false,
				"fetched_at": "2026-08-05T02:00:00Z", "fetch_error": "",
			}},
		},
	})
	if err != nil {
		t.Fatalf("SaveStagingItem failed: %v", err)
	}

	h := HandleSourceRegistry(store)
	req := httptest.NewRequest(http.MethodGet, "/viewer/source-registry?action=x-bookmarks&major=creative&minor=image_prompt&limit=12", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out sourceRegistryXBookmarkPageDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if out.Total != 1 || len(out.Items) != 1 {
		t.Fatalf("unexpected page: %+v", out)
	}
	item := out.Items[0]
	if item.Title != "画像生成prompt" || item.RawText != "画像生成promptの本文" || len(item.UseCaseTags) != 1 {
		t.Fatalf("classification projection missing: %+v", item)
	}
	if item.UseCaseTags[0].Major != "creative" || item.UseCaseTags[0].Minor != "image_prompt" {
		t.Fatalf("unexpected use case tag: %+v", item.UseCaseTags[0])
	}
	if item.ReferenceCount != 1 || len(item.References) != 1 {
		t.Fatalf("reference projection missing: %+v", item)
	}
	if item.MediaCount != 1 || len(item.Media) != 1 || item.Media[0].Alt != "青い図書館" {
		t.Fatalf("media projection missing: %+v", item)
	}
	reference := item.References[0]
	if reference.Kind != "external_url" || reference.PageTitle != "取得済み記事" || reference.BodyText != "外部リンク本文" || reference.ResolvedURL != "https://example.com/article" {
		t.Fatalf("unexpected reference projection: %+v", reference)
	}
	remaining, err := store.RecentStagingItems(ctx, l1sqlite.L1StagingStatusPending, 10)
	if err != nil || len(remaining) != 1 || remaining[0].ID != staged.ID {
		t.Fatalf("read-only request changed pending staging: items=%+v err=%v", remaining, err)
	}
}

func TestHandleSourceRegistry_XBookmarksRejectsInvalidPagination(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	rec := httptest.NewRecorder()
	HandleSourceRegistry(store)(rec, httptest.NewRequest(http.MethodGet, "/viewer/source-registry?action=x-bookmarks&limit=500", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
