package viewer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlePageKeepsDebugViewerInCore(t *testing.T) {
	rec := httptest.NewRecorder()
	HandlePage(rec, httptest.NewRequest(http.MethodGet, "/viewer?tab=ops", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "RenCrow") {
		t.Fatal("debug Viewer HTML was not served")
	}
}

func TestHandlePageIncludesImageGenerationTab(t *testing.T) {
	rec := httptest.NewRecorder()
	HandlePage(rec, httptest.NewRequest(http.MethodGet, "/viewer", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	body := rec.Body.String()
	for _, expected := range []string{
		`data-tab="image"`,
		`id="panel-image"`,
		`id="imagePrompt"`,
		`id="imageGenerateBtn"`,
		`/viewer/assets/js/tabs/image.js`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("Viewer HTML missing %q", expected)
		}
	}
}

func TestHandleXBookmarksPageServesDedicatedReadOnlyViewer(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleXBookmarksPage(rec, httptest.NewRequest(http.MethodGet, "/viewer/x-bookmarks", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q want=no-store", got)
	}
	body := rec.Body.String()
	for _, expected := range []string{
		`<title>X Bookmark | RenCrow</title>`,
		`href="/viewer"`,
		`id="collectionXBookmarkTotal"`,
		`id="collectionXBookmarkReviewCount"`,
		`id="collectionXBookmarkMajorFilter"`,
		`id="collectionXBookmarkMinorFilter"`,
		`id="collectionXBookmarkReviewFilter"`,
		`id="collectionXBookmarkSearch"`,
		`id="collectionXBookmarkItems"`,
		`/viewer/assets/css/x-bookmarks.css`,
		`/viewer/assets/js/tabs/collection-x-bookmarks.js`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("X Bookmark page missing %q", expected)
		}
	}
}
