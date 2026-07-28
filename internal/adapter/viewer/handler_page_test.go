package viewer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlePageRejectsLegacyExternalModes(t *testing.T) {
	for _, path := range []string{
		"/viewer?mode=lab",
		"/viewer?mode=view",
		"/viewer?mode=live",
	} {
		rec := httptest.NewRecorder()
		HandlePage(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d, want 404", path, rec.Code)
		}
	}
}

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
