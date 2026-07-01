package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
)

func TestBuildViewerRuntimeHandlersRegistersSourceRegistryUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"))
	if deps.viewerSourceRegistry == nil {
		t.Fatal("viewerSourceRegistry handler is nil")
	}

	req := httptest.NewRequest(http.MethodGet, "/viewer/source-registry", nil)
	rec := httptest.NewRecorder()
	deps.viewerSourceRegistry.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "source registry unavailable") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestBuildViewerRuntimeHandlersRegistersMemoryLayersUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"))
	if deps.viewerMemoryLayers == nil {
		t.Fatal("viewerMemoryLayers handler is nil")
	}

	req := httptest.NewRequest(http.MethodGet, "/viewer/memory/layers", nil)
	rec := httptest.NewRecorder()
	deps.viewerMemoryLayers.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "memory layers unavailable") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestBuildViewerRuntimeHandlersRegistersDomainGraphUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"))
	if deps.viewerDomainGraphAssertions == nil {
		t.Fatal("viewerDomainGraphAssertions handler is nil")
	}

	req := httptest.NewRequest(http.MethodGet, "/viewer/domain-graph/assertions", nil)
	rec := httptest.NewRecorder()
	deps.viewerDomainGraphAssertions.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "domain graph unavailable") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestBuildViewerRuntimeHandlersRegistersMovieDomainGraphSyncUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"))
	if deps.viewerMovieDomainGraphSync == nil {
		t.Fatal("viewerMovieDomainGraphSync handler is nil")
	}

	req := httptest.NewRequest(http.MethodPost, "/viewer/movie-catalog/domain-graph-sync", nil)
	rec := httptest.NewRecorder()
	deps.viewerMovieDomainGraphSync.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "movie domain graph sync unavailable") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestBuildViewerRuntimeHandlersRegistersHobbyDomainGraphSyncUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"))
	if deps.viewerHobbyDomainGraphSync == nil {
		t.Fatal("viewerHobbyDomainGraphSync handler is nil")
	}

	req := httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/domain-graph-sync", nil)
	rec := httptest.NewRecorder()
	deps.viewerHobbyDomainGraphSync.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "hobby domain graph sync unavailable") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}
