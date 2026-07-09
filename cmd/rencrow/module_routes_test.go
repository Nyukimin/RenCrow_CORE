package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulellm "github.com/Nyukimin/RenCrow_CORE/modules/llm"
)

func TestRegisteredModuleRoutesServeRuntimeContractJSON(t *testing.T) {
	mux := http.NewServeMux()
	dependencies := &Dependencies{
		moduleLLMProviders: map[string]modulellm.Provider{
			"chat": fakeModuleLLMProvider{name: "chat-provider"},
		},
		moduleChatService:    &fakeChatModuleService{},
		moduleTTSProvider:    fakeModuleTTSProvider{},
		moduleTTSPlayback:    fakeTTSPlaybackObserver{},
		moduleSTTViewerInput: fakeSTTViewerInputObserver{},
		moduleWorkerExecutor: fakeModuleWorkerExecutor{},
	}
	registerModuleRoutes(mux, dependencies, sttRuntime{Module: fakeModuleSTTProvider{}})

	tests := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: moduleManifestPath},
		{method: http.MethodGet, path: moduleHealthPath},
		{method: http.MethodGet, path: moduleLLMDiagnosticsPath},
		{method: http.MethodPost, path: moduleChatRoutePath, body: `{"session_id":"s1","channel":"viewer","user_id":"u1","text":"実装して"}`},
		{method: http.MethodGet, path: moduleWorkerDiagnosticsPath},
		{method: http.MethodGet, path: moduleTTSDiagnosticsPath},
		{method: http.MethodGet, path: moduleTTSPlaybackStatePath},
		{method: http.MethodGet, path: moduleSTTDiagnosticsPath},
		{method: http.MethodGet, path: moduleSTTViewerInputPath},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, tt.path, bytes.NewBufferString(tt.body))
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("unexpected status for %s %s: %d body=%s", tt.method, tt.path, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("unexpected content type for %s: %q", tt.path, got)
			}
			var payload map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("invalid json for %s: %v body=%s", tt.path, err, rec.Body.String())
			}
			if len(payload) == 0 {
				t.Fatalf("empty json payload for %s", tt.path)
			}
		})
	}
}

func TestRegisteredModuleRoutesRejectWrongMethods(t *testing.T) {
	mux := http.NewServeMux()
	registerModuleRoutes(mux, &Dependencies{}, sttRuntime{})

	for _, path := range currentRegisteredModuleEndpointPaths() {
		method := http.MethodPost
		if path == moduleChatRoutePath {
			method = http.MethodGet
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, nil)
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected method not allowed for %s %s, got %d body=%s", method, path, rec.Code, rec.Body.String())
		}
	}
}

func TestModuleRouteConstantsMatchCoreManifest(t *testing.T) {
	want := map[string]bool{}
	for _, path := range modulecore.RegisteredModuleEndpointPaths() {
		want[path] = true
	}
	for _, path := range []string{
		moduleManifestPath,
		moduleHealthPath,
		moduleLLMDiagnosticsPath,
		moduleChatRoutePath,
		moduleWorkerDiagnosticsPath,
		moduleTTSDiagnosticsPath,
		moduleTTSPlaybackStatePath,
		moduleSTTDiagnosticsPath,
		moduleSTTViewerInputPath,
	} {
		if !want[path] {
			t.Fatalf("module route constant is not in core manifest: %s", path)
		}
	}
}
