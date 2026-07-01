package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	modulecore "github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

func TestCurrentModuleDescriptorsIncludeAllRuntimeModules(t *testing.T) {
	descriptors := currentModuleDescriptors()
	seen := map[string]modulecore.ModuleDescriptor{}
	for _, descriptor := range descriptors {
		seen[descriptor.Name] = descriptor
	}
	for _, name := range []string{"core", "llm", "chat", "worker", "tts", "tts.playback", "stt", "stt.viewer_input"} {
		if _, ok := seen[name]; !ok {
			t.Fatalf("module descriptor missing: %s in %+v", name, descriptors)
		}
	}
	if len(seen["tts.playback"].OwnsState) == 0 {
		t.Fatalf("tts.playback state ownership was not described: %+v", seen["tts.playback"])
	}
	if len(seen["stt.viewer_input"].OwnsState) == 0 {
		t.Fatalf("stt.viewer_input state ownership was not described: %+v", seen["stt.viewer_input"])
	}
	if !containsString(seen["worker"].Endpoints, moduleWorkerDiagnosticsPath) {
		t.Fatalf("worker diagnostics endpoint missing: %+v", seen["worker"])
	}
	if !containsString(seen["llm"].Endpoints, moduleLLMDiagnosticsPath) {
		t.Fatalf("llm diagnostics endpoint missing: %+v", seen["llm"])
	}
	if !containsString(seen["tts"].Endpoints, moduleTTSDiagnosticsPath) {
		t.Fatalf("tts diagnostics endpoint missing: %+v", seen["tts"])
	}
	if !containsString(seen["stt"].Endpoints, moduleSTTDiagnosticsPath) {
		t.Fatalf("stt diagnostics endpoint missing: %+v", seen["stt"])
	}
}

func TestModuleManifestEndpointsMatchRegisteredRoutes(t *testing.T) {
	registered := map[string]bool{}
	for _, path := range currentRegisteredModuleEndpointPaths() {
		registered[path] = true
	}
	manifest := map[string]bool{}
	for _, descriptor := range currentModuleDescriptors() {
		for _, endpoint := range descriptor.Endpoints {
			manifest[endpoint] = true
			if !registered[endpoint] {
				t.Fatalf("module manifest endpoint is not registered: module=%s endpoint=%s", descriptor.Name, endpoint)
			}
		}
	}
	for _, path := range currentRegisteredModuleEndpointPaths() {
		if !manifest[path] {
			t.Fatalf("registered module route is missing from manifest: %s", path)
		}
	}
}

func TestRegisterModuleRoutesRegistersManifestEndpoints(t *testing.T) {
	mux := http.NewServeMux()
	dependencies := &Dependencies{}
	registerModuleRoutes(mux, dependencies, sttRuntime{})

	for _, path := range currentRegisteredModuleEndpointPaths() {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		_, pattern := mux.Handler(req)
		if pattern == "" {
			t.Fatalf("module route was not registered: %s", path)
		}
	}
}

func TestHandleModuleManifestReturnsJSON(t *testing.T) {
	handler := handleModuleManifest()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, moduleManifestPath, nil)
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var got modulecore.ManifestSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got.UpdatedAt == "" || len(got.Modules) < 8 {
		t.Fatalf("unexpected manifest: %+v", got)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
