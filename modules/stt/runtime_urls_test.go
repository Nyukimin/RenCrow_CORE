package stt

import "testing"

func TestInferGatewayURL(t *testing.T) {
	if got := InferGatewayURL(" ws://192.168.1.36:8090/stt ", "ws://fallback"); got != "ws://192.168.1.36:8090/stt" {
		t.Fatalf("explicit gateway = %q", got)
	}
	if got := InferGatewayURL("", " ws://192.168.1.36:8090/stt "); got != "ws://192.168.1.36:8090/stt" {
		t.Fatalf("fallback gateway = %q", got)
	}
}

func TestInferProviderURLDefaultsToGoSTTFile(t *testing.T) {
	got := InferProviderURL(RuntimeURLConfig{
		Provider:   ProviderExternalHTTP,
		ServerHost: "127.0.0.1",
		ServerPort: 8443,
		TLSEnabled: true,
	})
	want := "https://127.0.0.1:8443/stt/file"
	if got != want {
		t.Fatalf("provider url = %q, want %q", got, want)
	}
}

func TestInferProviderURLPreservesExternalCompatibilityURL(t *testing.T) {
	got := InferProviderURL(RuntimeURLConfig{
		Provider:    ProviderExternalHTTP,
		ProviderURL: "http://127.0.0.1:8080/inference",
	})
	if got != "http://127.0.0.1:8080/inference" {
		t.Fatalf("provider url = %q", got)
	}
}

func TestInferLegacyInferenceProviderURLUsesTTSHost(t *testing.T) {
	got := InferLegacyInferenceProviderURL("http://127.0.0.1:7870", "")
	if got != "http://127.0.0.1:8080/inference" {
		t.Fatalf("legacy provider url = %q", got)
	}
}

func TestStreamURLInfersRealtimeEndpointFromProviderURL(t *testing.T) {
	got := StreamURL(RuntimeURLConfig{
		ProviderURL: "http://192.168.1.33:8766/v1/audio/transcriptions",
	})
	if got != "ws://192.168.1.33:8766/ws/transcribe" {
		t.Fatalf("stream url = %q", got)
	}
}

func TestStreamURLUsesExplicitValue(t *testing.T) {
	got := StreamURL(RuntimeURLConfig{
		ProviderURL: "http://192.168.1.33:8766/v1/audio/transcriptions",
		StreamURL:   "wss://stt.local/ws/transcribe",
	})
	if got != "wss://stt.local/ws/transcribe" {
		t.Fatalf("stream url = %q", got)
	}
}
