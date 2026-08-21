package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func TestBuildViewerRuntimeHandlersUsesConfiguredGameObserverURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/games/status":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/games/launch":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true,"game_id":"nethack","session_id":"session-1","status":"launching"}`))
		default:
			t.Fatalf("upstream path=%q", r.URL.Path)
		}
	}))
	t.Cleanup(upstream.Close)
	legacyURL := "http://127.0.0.1:1"
	if err := os.Setenv("RENCROW_GAMES_OBSERVER_URL", legacyURL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("RENCROW_GAMES_OBSERVER_URL") })

	deps := &Dependencies{}
	cfg := &config.Config{WorkspaceDir: t.TempDir()}
	cfg.Games.ObserverURL = upstream.URL
	buildViewerRuntimeHandlers(cfg, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/games/observer-api/games/status", nil)
	deps.viewerGamesObserverProxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("observer proxy status=%d body=%q", rec.Code, rec.Body.String())
	}
	launchRec := httptest.NewRecorder()
	launchReq := httptest.NewRequest(http.MethodPost, "/viewer/games/launch", strings.NewReader(`{"game_id":"nethack"}`))
	deps.viewerGamesLaunch.ServeHTTP(launchRec, launchReq)
	if launchRec.Code != http.StatusOK || !strings.Contains(launchRec.Body.String(), `"session_id":"session-1"`) {
		t.Fatalf("game launch status=%d body=%q", launchRec.Code, launchRec.Body.String())
	}
}

func TestBuildViewerRuntimeHandlersRegistersSourceRegistryUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
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
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
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
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
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
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
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
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
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
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
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
