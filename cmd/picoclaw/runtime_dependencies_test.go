package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildComplexityHotspotCoderDiffHandlerReturns503WhenCoderUnavailable(t *testing.T) {
	handler := buildComplexityHotspotCoderDiffHandler(nil, llmRuntimeProviders{}, nil, nil)
	if handler == nil {
		t.Fatal("handler is nil")
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/complexity-hotspots/coder-diffs", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "complexity coder diff mode unavailable") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}
