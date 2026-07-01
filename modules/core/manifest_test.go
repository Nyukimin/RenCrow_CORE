package core

import "testing"

func TestRegisteredModuleEndpointPathsIncludesEveryRuntimeEndpoint(t *testing.T) {
	got := RegisteredModuleEndpointPaths()
	want := []string{
		ModuleManifestEndpoint,
		ModuleHealthEndpoint,
		ModuleLLMDiagnosticsEndpoint,
		ModuleChatRouteEndpoint,
		ModuleWorkerDiagnosticsEndpoint,
		ModuleTTSDiagnosticsEndpoint,
		ModuleTTSPlaybackStateEndpoint,
		ModuleSTTDiagnosticsEndpoint,
		ModuleSTTViewerInputEndpoint,
	}
	if len(got) != len(want) {
		t.Fatalf("unexpected endpoint count: got=%d want=%d endpoints=%v", len(got), len(want), got)
	}
	for idx, endpoint := range want {
		if got[idx] != endpoint {
			t.Fatalf("unexpected endpoint at index %d: got=%s want=%s all=%v", idx, got[idx], endpoint, got)
		}
	}
}

func TestCurrentModuleDescriptorsDescribeAllRuntimeModules(t *testing.T) {
	descriptors := CurrentModuleDescriptors()
	seen := map[string]ModuleDescriptor{}
	for _, descriptor := range descriptors {
		seen[descriptor.Name] = descriptor
		if descriptor.Owner == "" || descriptor.Kind == "" || descriptor.Description == "" {
			t.Fatalf("descriptor is missing required metadata: %+v", descriptor)
		}
	}
	for _, name := range []string{"core", "llm", "chat", "worker", "tts", "tts.playback", "stt", "stt.viewer_input", "voicechat", "browseractor", "webgather"} {
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
}

func TestCurrentModuleDescriptorsUseRegisteredEndpoints(t *testing.T) {
	registered := map[string]bool{}
	for _, endpoint := range RegisteredModuleEndpointPaths() {
		registered[endpoint] = true
	}
	described := map[string]bool{}
	for _, descriptor := range CurrentModuleDescriptors() {
		for _, endpoint := range descriptor.Endpoints {
			described[endpoint] = true
			if !registered[endpoint] {
				t.Fatalf("descriptor %s uses an unregistered module endpoint: %s", descriptor.Name, endpoint)
			}
		}
	}
	for _, endpoint := range RegisteredModuleEndpointPaths() {
		if !described[endpoint] {
			t.Fatalf("registered module endpoint is missing from descriptors: %s", endpoint)
		}
	}
}
