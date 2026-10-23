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

func TestRegisterImageRoutesRegistersCharImageViewerAPI(t *testing.T) {
	mux := http.NewServeMux()
	registerImageRoutes(mux, &config.Config{})

	characters := httptest.NewRecorder()
	mux.ServeHTTP(characters, httptest.NewRequest(http.MethodGet, "/viewer/charimage/characters", nil))
	if characters.Code != http.StatusOK || !strings.Contains(characters.Body.String(), `"characters":[`) {
		t.Fatalf("CharImage characters route status=%d body=%s", characters.Code, characters.Body.String())
	}

	compose := httptest.NewRecorder()
	mux.ServeHTTP(compose, httptest.NewRequest(http.MethodPost, "/viewer/charimage/compose", strings.NewReader(`{"character_id":"01_sera","render_mode":"2d","scene_variant_text":""}`)))
	if compose.Code != http.StatusOK || !strings.Contains(compose.Body.String(), `"final_prompt":`) {
		t.Fatalf("CharImage compose route status=%d body=%s", compose.Code, compose.Body.String())
	}

	generate := httptest.NewRecorder()
	mux.ServeHTTP(generate, httptest.NewRequest(http.MethodPost, "/viewer/charimage/generate", strings.NewReader(`{"character_id":"01_sera","render_mode":"2d","scene_variant_text":""}`)))
	if generate.Code != http.StatusServiceUnavailable || !strings.Contains(generate.Body.String(), `"CHARIMAGE_UNAVAILABLE"`) {
		t.Fatalf("CharImage Generate without configured durable store status=%d body=%s", generate.Code, generate.Body.String())
	}
}

func TestRegisterImageRoutesDoesNotPassTypedNilGatewayToCharImage(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{Databases: config.DatabasePathsConfig{
			CharImageHistory: filepath.Join(root, "charimage_history.db"),
		}},
	}
	mux := http.NewServeMux()
	registerImageRoutes(mux, cfg)

	generate := httptest.NewRecorder()
	mux.ServeHTTP(generate, httptest.NewRequest(http.MethodPost, "/viewer/charimage/generate", strings.NewReader(`{"character_id":"01_sera","render_mode":"2d","scene_variant_text":""}`)))
	if generate.Code != http.StatusServiceUnavailable || !strings.Contains(generate.Body.String(), `"CHARIMAGE_UNAVAILABLE"`) {
		t.Fatalf("CharImage Generate with Image disabled status=%d body=%s", generate.Code, generate.Body.String())
	}
}
