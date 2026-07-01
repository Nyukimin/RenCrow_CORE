package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequireHTTPMethodAllowsExpectedMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/modules", nil)
	rr := httptest.NewRecorder()

	if !RequireHTTPMethod(rr, req, http.MethodGet) {
		t.Fatal("expected method to be allowed")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected response code: got %d", rr.Code)
	}
}

func TestRequireHTTPMethodRejectsUnexpectedMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/modules", nil)
	rr := httptest.NewRecorder()

	if RequireHTTPMethod(rr, req, http.MethodGet) {
		t.Fatal("expected method to be rejected")
	}
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected response code: got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "method not allowed") {
		t.Fatalf("unexpected response body: %q", rr.Body.String())
	}
}

func TestWriteJSONSetsContentTypeAndEncodesValue(t *testing.T) {
	rr := httptest.NewRecorder()

	if err := WriteJSON(rr, map[string]string{"status": "ready"}); err != nil {
		t.Fatalf("WriteJSON returned error: %v", err)
	}

	if got := rr.Header().Get("Content-Type"); got != JSONContentType {
		t.Fatalf("unexpected content type: got %q want %q", got, JSONContentType)
	}
	if got := rr.Body.String(); got != "{\"status\":\"ready\"}\n" {
		t.Fatalf("unexpected body: %q", got)
	}
}
