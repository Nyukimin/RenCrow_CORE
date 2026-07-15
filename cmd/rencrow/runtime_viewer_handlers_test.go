package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func TestBuildViewerRuntimeHandlersRegistersSourceRegistryUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil)
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
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil)
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

func TestBuildViewerRuntimeHandlersRegistersRecallTraceUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil)
	if deps.viewerRecallTraces == nil {
		t.Fatal("viewerRecallTraces handler is nil")
	}

	req := httptest.NewRequest(http.MethodGet, "/viewer/recall/traces?limit=5", nil)
	rec := httptest.NewRecorder()
	deps.viewerRecallTraces.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"unavailable"`) {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestBuildViewerRuntimeHandlersRegistersDomainGraphUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil)
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
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil)
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
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil)
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

func TestBuildViewerRuntimeHandlersEnablesGameBridgeLLMModeWhenProviderAvailable(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), fakeConversationProvider{name: "chat-provider"})
	if deps.viewerGamesStatus == nil {
		t.Fatal("viewerGamesStatus handler is nil")
	}

	req := httptest.NewRequest(http.MethodGet, "/viewer/games/status", nil)
	rec := httptest.NewRecorder()
	deps.viewerGamesStatus.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"decision_mode":"llm"`) {
		t.Fatalf("status should report llm decision mode: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"llm_router_enabled":true`) {
		t.Fatalf("status should report llm router enabled: %s", rec.Body.String())
	}
}
