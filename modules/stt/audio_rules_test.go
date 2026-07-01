package stt

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFinalTimeoutFromMilliseconds(t *testing.T) {
	if got := FinalTimeoutFromMilliseconds(150); got != DefaultFinalTimeout {
		t.Fatalf("FinalTimeoutFromMilliseconds(150) = %s, want %s", got, DefaultFinalTimeout)
	}
	if got := FinalTimeoutFromMilliseconds(800); got != 800*time.Millisecond {
		t.Fatalf("FinalTimeoutFromMilliseconds(800) = %s", got)
	}
}

func TestHTTPTimeoutFromMilliseconds(t *testing.T) {
	if got := HTTPTimeoutFromMilliseconds(200); got != DefaultHTTPTimeout {
		t.Fatalf("HTTPTimeoutFromMilliseconds(200) = %s, want %s", got, DefaultHTTPTimeout)
	}
	if got := HTTPTimeoutFromMilliseconds(1500); got != 1500*time.Millisecond {
		t.Fatalf("HTTPTimeoutFromMilliseconds(1500) = %s", got)
	}
}

func TestSilenceAbsThreshold(t *testing.T) {
	if got := SilenceAbsThreshold(0); got != DefaultSilenceAbsThreshold {
		t.Fatalf("SilenceAbsThreshold(0) = %d", got)
	}
	if got := SilenceAbsThreshold(123); got != 123 {
		t.Fatalf("SilenceAbsThreshold(123) = %d", got)
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

func TestNormalizeAudioPayloadWrapsPCM16AsWAV(t *testing.T) {
	pcm := []byte{0x10, 0x01, 0x20, 0x02, 0xff}
	got := NormalizeAudioPayload(pcm)
	if !IsWAV(got) {
		t.Fatalf("NormalizeAudioPayload() did not create WAV header")
	}
	if len(got) != 44+4 {
		t.Fatalf("NormalizeAudioPayload() len = %d, want %d", len(got), 48)
	}
}

func TestNormalizeAudioPayloadKeepsWAV(t *testing.T) {
	wav := PCM16LEToWAV([]byte{0x10, 0x01}, 16000)
	got := NormalizeAudioPayload(wav)
	if &got[0] != &wav[0] {
		t.Fatalf("NormalizeAudioPayload() should preserve existing WAV slice")
	}
}

func TestIsLikelySilentWAV(t *testing.T) {
	silent := PCM16LEToWAV([]byte{0x00, 0x00, 0x01, 0x00}, 16000)
	if !IsLikelySilentWAV(silent, 220) {
		t.Fatal("silent wav should be detected")
	}
	loud := PCM16LEToWAV([]byte{0x10, 0x01, 0x20, 0x02}, 16000)
	if IsLikelySilentWAV(loud, 220) {
		t.Fatal("loud wav should not be detected as silent")
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
