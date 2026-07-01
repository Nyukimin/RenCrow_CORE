package tts

import (
	"testing"
	"time"
)

func TestShouldRetrySynthesis(t *testing.T) {
	if !ShouldRetrySynthesis("engine-unavailable", 1) {
		t.Fatal("engine unavailable should retry on second attempt window")
	}
	if ShouldRetrySynthesis("engine-unavailable", 2) {
		t.Fatal("engine unavailable should stop after attempt 2")
	}
	if !ShouldRetrySynthesis("synthesis_failed", 0) {
		t.Fatal("synthesis failed should retry on first attempt")
	}
	if ShouldRetrySynthesis("invalid_request", 0) {
		t.Fatal("invalid request must not retry")
	}
}

func TestSynthesisBackoffForAttempt(t *testing.T) {
	if got := SynthesisBackoffForAttempt(-1); got != 200*time.Millisecond {
		t.Fatalf("negative attempt backoff = %s", got)
	}
	if got := SynthesisBackoffForAttempt(2); got != 800*time.Millisecond {
		t.Fatalf("attempt 2 backoff = %s", got)
	}
}

func TestParseSynthesisError(t *testing.T) {
	code, message := ParseSynthesisError([]byte(`{"error":{"code":"engine-unavailable","message":" busy "}}`))
	if code != "ENGINE_UNAVAILABLE" || message != "busy" {
		t.Fatalf("ParseSynthesisError() = %q,%q", code, message)
	}
	code, message = ParseSynthesisError([]byte(`not json`))
	if code != "" || message != "" {
		t.Fatalf("ParseSynthesisError(invalid) = %q,%q", code, message)
	}
}

func TestShouldRetrySynthesisTransportError(t *testing.T) {
	if !ShouldRetrySynthesisTransportError("dial tcp: connection refused", 0) {
		t.Fatal("connection refused should retry")
	}
	if !ShouldRetrySynthesisTransportError("Client.Timeout exceeded while awaiting headers", 1) {
		t.Fatal("timeout should retry")
	}
	if ShouldRetrySynthesisTransportError("connection refused", 2) {
		t.Fatal("attempt 2 should not retry transport error")
	}
	if ShouldRetrySynthesisTransportError("", 0) {
		t.Fatal("empty message should not retry")
	}
}
