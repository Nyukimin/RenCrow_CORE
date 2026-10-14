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

func TestGatewayStreamURLSwapsTheSchemeAndAppendsTheFixedStreamPath(t *testing.T) {
	tests := []struct {
		name string
		base string
		want string
	}{
		{"http becomes ws", " http://127.0.0.1:8766/ ", "ws://127.0.0.1:8766/v1/audio/stream"},
		{"https becomes wss", "https://stt.example.invalid:8766", "wss://stt.example.invalid:8766/v1/audio/stream"},
		{"empty uses the default gateway", "", "ws://127.0.0.1:8766/v1/audio/stream"},
		{"a path prefix is kept like the transcription URL keeps it", "http://127.0.0.1:8766/gw/", "ws://127.0.0.1:8766/gw/v1/audio/stream"},
		{"a query is not carried over", "http://127.0.0.1:8766?x=1", "ws://127.0.0.1:8766/v1/audio/stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GatewayStreamURL(tt.base); got != tt.want {
				t.Fatalf("GatewayStreamURL(%q) = %q, want %q", tt.base, got, tt.want)
			}
		})
	}
}

func TestGatewayStreamURLRejectsAnUnsupportedScheme(t *testing.T) {
	if got := GatewayStreamURL("ftp://127.0.0.1:8766"); got != "" {
		t.Fatalf("GatewayStreamURL(ftp) = %q, want empty so the caller reports a configuration error", got)
	}
	if got := GatewayStreamURL("not a url"); got != "" {
		t.Fatalf("GatewayStreamURL(garbage) = %q, want empty", got)
	}
}
