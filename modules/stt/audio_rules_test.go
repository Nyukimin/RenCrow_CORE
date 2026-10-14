package stt

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHTTPTimeoutFromMilliseconds(t *testing.T) {
	if got := HTTPTimeoutFromMilliseconds(200); got != DefaultHTTPTimeout {
		t.Fatalf("HTTPTimeoutFromMilliseconds(200) = %s, want %s", got, DefaultHTTPTimeout)
	}
	if got := HTTPTimeoutFromMilliseconds(1500); got != 1500*time.Millisecond {
		t.Fatalf("HTTPTimeoutFromMilliseconds(1500) = %s", got)
	}
}

func TestParseControlMessage(t *testing.T) {
	got, ok := ParseControlMessage([]byte(`{"type":"final_pending"}`))
	if !ok || got != "final_pending" {
		t.Fatalf("ParseControlMessage() = %q,%t", got, ok)
	}
	got, ok = ParseControlMessage([]byte(`{"type":"stop"}`))
	if !ok || got != "stop" {
		t.Fatalf("ParseControlMessage(stop) = %q,%t", got, ok)
	}
	if got, ok := ParseControlMessage([]byte(`{"type":`)); ok || got != "" {
		t.Fatalf("ParseControlMessage(invalid) = %q,%t", got, ok)
	}
	if got, ok := ParseControlMessage([]byte{0, 1}); ok || got != "" {
		t.Fatalf("ParseControlMessage(binary) = %q,%t", got, ok)
	}
}

func TestIsWAVRecognisesARIFFWAVEHeader(t *testing.T) {
	wav := make([]byte, 44)
	copy(wav[0:4], "RIFF")
	copy(wav[8:12], "WAVE")
	if !IsWAV(wav) {
		t.Fatal("a RIFF/WAVE header must be recognised: /stt refuses such a frame")
	}
	pcm := make([]byte, 64)
	if IsWAV(pcm) || IsWAV(wav[:43]) || IsWAV(nil) {
		t.Fatal("PCM, a truncated header and nothing are not WAV")
	}
}

func TestAdjustAdaptiveTimeoutClamps(t *testing.T) {
	if got := AdjustAdaptiveTimeout(1*time.Second, -500*time.Millisecond, 800*time.Millisecond, 2*time.Second); got != 800*time.Millisecond {
		t.Fatalf("AdjustAdaptiveTimeout min clamp = %s", got)
	}
	if got := AdjustAdaptiveTimeout(1900*time.Millisecond, 500*time.Millisecond, 800*time.Millisecond, 2*time.Second); got != 2*time.Second {
		t.Fatalf("AdjustAdaptiveTimeout max clamp = %s", got)
	}
	if got := AdjustAdaptiveTimeout(1*time.Second, 100*time.Millisecond, 800*time.Millisecond, 2*time.Second); got != 1100*time.Millisecond {
		t.Fatalf("AdjustAdaptiveTimeout normal = %s", got)
	}
}

func TestIsTimeoutError(t *testing.T) {
	if !IsTimeoutError(context.DeadlineExceeded) {
		t.Fatal("context deadline should be treated as timeout")
	}
	if !IsTimeoutError(sttTimeoutErr{}) {
		t.Fatal("net timeout should be treated as timeout")
	}
	if !IsTimeoutError(errors.New("client.timeout exceeded while awaiting headers")) {
		t.Fatal("client timeout text should be treated as timeout")
	}
	if IsTimeoutError(errors.New("boom")) {
		t.Fatal("non-timeout error should not be treated as timeout")
	}
}

type sttTimeoutErr struct{}

func (sttTimeoutErr) Error() string {
	return "timeout"
}

func (sttTimeoutErr) Timeout() bool {
	return true
}

func (sttTimeoutErr) Temporary() bool {
	return true
}
