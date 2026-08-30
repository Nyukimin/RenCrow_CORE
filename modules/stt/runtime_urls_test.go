package stt

import "testing"

func TestGatewayTranscriptionURLUsesRenCrowSTTPublicContract(t *testing.T) {
	if got := GatewayTranscriptionURL(" http://127.0.0.1:8766/ "); got != "http://127.0.0.1:8766/v1/audio/transcriptions" {
		t.Fatalf("GatewayTranscriptionURL() = %q", got)
	}
}

func TestInferGatewayHTTPURLDefaultsToRenCrowSTTPublicContract(t *testing.T) {
	got := InferGatewayHTTPURL(RuntimeURLConfig{
		Provider:   ProviderRenCrowSTT,
		ServerHost: "127.0.0.1",
		ServerPort: 8443,
		TLSEnabled: true,
	})
	want := "https://127.0.0.1:8443/v1/audio/transcriptions"
	if got != want {
		t.Fatalf("provider url = %q, want %q", got, want)
	}
}

func TestInferGatewayHTTPURLPreservesGatewayURL(t *testing.T) {
	got := InferGatewayHTTPURL(RuntimeURLConfig{
		Provider:       ProviderRenCrowSTT,
		GatewayHTTPURL: "http://127.0.0.1:8080/inference",
	})
	if got != "http://127.0.0.1:8080/inference" {
		t.Fatalf("provider url = %q", got)
	}
}
