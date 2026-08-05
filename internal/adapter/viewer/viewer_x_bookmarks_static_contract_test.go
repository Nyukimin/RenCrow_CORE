package viewer

import (
	"os"
	"strings"
	"testing"
)

func TestViewerInformationCollectionShowsXBookmarks(t *testing.T) {
	htmlData, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}
	jsData, err := os.ReadFile("assets/js/tabs/collection-x-bookmarks.js")
	if err != nil {
		t.Fatalf("read collection-x-bookmarks.js: %v", err)
	}
	html := string(htmlData)
	for _, needle := range []string{
		`<a class="tab-btn tab-link" href="/viewer/x-bookmarks">X Bookmark</a>`,
		`<option value="x-bookmarks">X Bookmark</option>`,
	} {
		if !strings.Contains(html, needle) {
			t.Fatalf("Information Collection X Bookmark entry contract missing %q", needle)
		}
	}
	if strings.Contains(html, `id="collectionXBookmarkItems"`) {
		t.Fatal("Information Collection must not embed the X Bookmark list")
	}

	pageData, err := os.ReadFile("x_bookmarks.html")
	if err != nil {
		t.Fatalf("read x_bookmarks.html: %v", err)
	}
	page := string(pageData)
	for _, needle := range []string{
		`id="collectionXBookmarkTotal"`,
		`id="collectionXBookmarkReviewCount"`,
		`id="collectionXBookmarkMajorFilter"`,
		`id="collectionXBookmarkMinorFilter"`,
		`id="collectionXBookmarkReviewFilter"`,
		`id="collectionXBookmarkSearch"`,
		`id="collectionXBookmarkItems"`,
		`id="collectionXBookmarkPrev"`,
		`id="collectionXBookmarkNext"`,
		`/viewer/assets/js/tabs/collection-x-bookmarks.js`,
	} {
		if !strings.Contains(page, needle) {
			t.Fatalf("dedicated X Bookmark Viewer contract missing %q", needle)
		}
	}
	js := string(jsData)
	for _, needle := range []string{
		"/viewer/source-registry?action=x-bookmarks",
		"function refreshXBookmarkData(",
		"function renderXBookmarkData(",
		"use_case_tags",
		"needs_review",
		"raw_text",
		"references",
		"function xBookmarkReferenceCard(",
		"function xBookmarkMedia(",
		"function runXBookmarkWorkflow(",
		"/viewer/x-bookmarks/workflows/run",
		"image_prompt_draw",
		"ai_tip_rencrow_evaluation",
		"PromptをCopy",
		"リンク先本文を表示",
		"本文未取得",
		"rel=\"noopener noreferrer\"",
	} {
		if !strings.Contains(js, needle) {
			t.Fatalf("X Bookmark JavaScript contract missing %q", needle)
		}
	}
}
