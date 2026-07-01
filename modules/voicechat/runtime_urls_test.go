package voicechat

import "testing"

func TestInferGatewayURLPrioritizesExplicitGateway(t *testing.T) {
	got := InferGatewayURL(
		" ws://192.168.1.207:8081/v1/chat/audio/sessions ",
		"ws://ignored/chat/sessions",
		"http://192.168.1.207:8081",
	)
	want := "ws://192.168.1.207:8081/v1/chat/audio/sessions"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestInferGatewayURLFallsBackToRencrowChatWS(t *testing.T) {
	got := InferGatewayURL("", " ws://192.168.1.207:8081/v1/chat/audio/sessions ", "http://192.168.1.207:8081")
	want := "ws://192.168.1.207:8081/v1/chat/audio/sessions"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestInferGatewayURLFromChatBaseUsesWSSForHTTPS(t *testing.T) {
	got := InferGatewayURLFromChatBase("https://192.168.1.31:8081/")
	want := "wss://192.168.1.31:8081/v1/chat/audio/sessions"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestInferGatewayURLFromChatBaseEmptyWhenUnset(t *testing.T) {
	if got := InferGatewayURL("", "", " "); got != "" {
		t.Fatalf("expected empty gateway url, got %q", got)
	}
}
