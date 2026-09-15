package viewer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	knowledgeapp "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledge"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

func TestHandleSourceRegistry_XBookmarksIsReadOnlyAndExposesClassification(t *testing.T) {
	ctx := context.Background()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	articleBody := strings.Repeat("外部リンク本文", 40)
	bodyHash := sha256.Sum256([]byte(articleBody))
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
				"body_text": articleBody, "body_char_count": len([]rune(articleBody)), "body_truncated": false,
				"fetched_at": "2026-08-05T02:00:00Z", "fetch_error": "",
				"summary": map[string]interface{}{"status": "ready", "text": "記事から生成した日本語サマリ", "body_sha256": hex.EncodeToString(bodyHash[:]), "revision": knowledgeapp.ExternalLinkSummaryRevision, "chunks": 2, "private_internal": "must-not-leak", "provenance": "core.external-link-summary/v1", "evidence_quotes": []string{"外部リンク本文"}},
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
	if reference.Kind != "external_url" || reference.PageTitle != "取得済み記事" || reference.BodyText != articleBody || reference.ResolvedURL != "https://example.com/article" {
		t.Fatalf("unexpected reference projection: %+v", reference)
	}
	if !strings.Contains(rec.Body.String(), "記事から生成した日本語サマリ") || strings.Contains(rec.Body.String(), "must-not-leak") {
		t.Fatal("reference summary must expose its public fields without leaking internal metadata")
	}
	remaining, err := store.RecentStagingItems(ctx, l1sqlite.L1StagingStatusPending, 10)
	if err != nil || len(remaining) != 1 || remaining[0].ID != staged.ID {
		t.Fatalf("read-only request changed pending staging: items=%+v err=%v", remaining, err)
	}
}

func TestXBookmarkLinkSummaryHidesStaleSourceAndInternalMetadata(t *testing.T) {
	got := xBookmarkLinkSummaryDTO(map[string]interface{}{
		"body_text": "更新された記事本文",
		"summary":   map[string]interface{}{"status": "ready", "text": "古い要約", "body_sha256": "old"},
	})
	if got == nil || got.Status != "blocked" || got.ErrorCode != "source_changed" || got.Text != "" {
		t.Fatal("stale summary must not be presented as a summary of the current body")
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

func TestXBookmarkLinkSummaryRejectsUnverifiedSummaryWithMatchingBodyHash(t *testing.T) {
	body := strings.Repeat("本文", 120)
	digest := sha256.Sum256([]byte(body))
	for _, provenance := range []string{"", "core.external-link-summary/v1"} {
		reference := map[string]interface{}{"kind": "external_url", "url": "https://example.com/article", "capture_status": "content_fetched", "body_text": body, "summary": map[string]interface{}{"status": "ready", "text": "未検証の要約", "body_sha256": hex.EncodeToString(digest[:]), "revision": knowledgeapp.ExternalLinkSummaryRevision, "provenance": provenance, "evidence_quotes": []string{"本文に存在しない引用"}}}
		got := xBookmarkLinkSummaryDTO(reference)
		if got == nil || got.Status != "blocked" || got.ErrorCode != "unverified_summary" || got.Text != "" {
			t.Fatal("unverified summary with matching hash was exposed")
		}
	}
}
