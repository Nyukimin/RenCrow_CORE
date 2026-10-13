package stt

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

// pcmTone returns ms milliseconds of constant-amplitude 16 kHz mono PCM16LE.
func pcmTone(ms int, amplitude int16) []byte {
	samples := ms * DefaultSampleRate / 1000
	out := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(amplitude))
	}
	return out
}

func pcmSilence(ms int) []byte { return pcmTone(ms, 0) }

func testLimits() UtteranceLimits {
	return UtteranceLimits{
		MinSpeech:           100 * time.Millisecond,
		MaxLength:           2 * time.Second,
		IdleTimeout:         time.Second,
		SilenceAbsThreshold: 220,
	}
}

func TestUtteranceLimitsWithDefaultsFillsZeroValues(t *testing.T) {
	got := UtteranceLimits{}.WithDefaults()
	if got.MinSpeech != DefaultMinSpeech || got.MaxLength != DefaultMaxUtterance ||
		got.IdleTimeout != DefaultUtteranceIdleTimeout || got.SilenceAbsThreshold != DefaultSilenceAbsThreshold {
		t.Fatalf("defaults = %+v", got)
	}
	kept := UtteranceLimits{MinSpeech: time.Second, MaxLength: 5 * time.Second, IdleTimeout: 3 * time.Second, SilenceAbsThreshold: 77}.WithDefaults()
	if kept.MinSpeech != time.Second || kept.MaxLength != 5*time.Second || kept.IdleTimeout != 3*time.Second || kept.SilenceAbsThreshold != 77 {
		t.Fatalf("explicit values must be kept: %+v", kept)
	}
}

func TestUtteranceLimitsWithDefaultsKeepsMinSpeechBelowMaxLength(t *testing.T) {
	got := UtteranceLimits{MinSpeech: 10 * time.Second, MaxLength: 2 * time.Second}.WithDefaults()
	if got.MinSpeech >= got.MaxLength {
		t.Fatalf("MinSpeech %s must stay below MaxLength %s", got.MinSpeech, got.MaxLength)
	}
}

func TestUtteranceBufferAccumulatesFramesIntoOneUtterance(t *testing.T) {
	b := NewUtteranceBuffer(testLimits())
	for i := 0; i < 5; i++ {
		if overflow := b.Append(pcmTone(100, 3000)); len(overflow) != 0 {
			t.Fatalf("frame %d overflowed %d bytes", i, len(overflow))
		}
	}
	if got := b.Duration(); got != 500*time.Millisecond {
		t.Fatalf("Duration() = %s, want 500ms", got)
	}
	if got := b.Voiced(); got != 500*time.Millisecond {
		t.Fatalf("Voiced() = %s, want 500ms", got)
	}
	if reason := b.EmptyReason(); reason != "" {
		t.Fatalf("EmptyReason() = %q, want usable", reason)
	}
}

func TestUtteranceBufferVoicedCountsOnlyLoudWindows(t *testing.T) {
	b := NewUtteranceBuffer(testLimits())
	b.Append(pcmSilence(300))
	b.Append(pcmTone(200, 3000))
	b.Append(pcmSilence(300))
	if got := b.Duration(); got != 800*time.Millisecond {
		t.Fatalf("Duration() = %s", got)
	}
	if got := b.Voiced(); got != 200*time.Millisecond {
		t.Fatalf("Voiced() = %s, want 200ms", got)
	}
}

func TestUtteranceBufferVoicedUsesAverageAbsoluteThreshold(t *testing.T) {
	b := NewUtteranceBuffer(UtteranceLimits{SilenceAbsThreshold: 220}.WithDefaults())
	b.Append(pcmTone(200, 219))
	if got := b.Voiced(); got != 0 {
		t.Fatalf("below threshold counted as voiced: %s", got)
	}
	b.Append(pcmTone(200, 220))
	if got := b.Voiced(); got != 200*time.Millisecond {
		t.Fatalf("at threshold must count as voiced: %s", got)
	}
	// Negative samples count by absolute value.
	n := NewUtteranceBuffer(UtteranceLimits{SilenceAbsThreshold: 220}.WithDefaults())
	n.Append(pcmTone(200, -3000))
	if got := n.Voiced(); got != 200*time.Millisecond {
		t.Fatalf("negative samples must count by magnitude: %s", got)
	}
}

func TestUtteranceBufferEmptyReason(t *testing.T) {
	tests := []struct {
		name  string
		build func(b *UtteranceBuffer)
		want  string
	}{
		{"no audio", func(b *UtteranceBuffer) {}, EmptyReasonNoAudio},
		{"silence only", func(b *UtteranceBuffer) { b.Append(pcmSilence(1000)) }, EmptyReasonSilent},
		{"single click", func(b *UtteranceBuffer) {
			b.Append(pcmSilence(500))
			b.Append(pcmTone(20, 9000))
			b.Append(pcmSilence(500))
		}, EmptyReasonTooShort},
		{"voiced exactly at the minimum is usable", func(b *UtteranceBuffer) { b.Append(pcmTone(100, 3000)) }, ""},
		{"enough speech", func(b *UtteranceBuffer) { b.Append(pcmTone(400, 3000)) }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewUtteranceBuffer(testLimits())
			tt.build(b)
			if got := b.EmptyReason(); got != tt.want {
				t.Fatalf("EmptyReason() = %q, want %q (voiced=%s)", got, tt.want, b.Voiced())
			}
		})
	}
}

func TestUtteranceBufferDefaultMinSpeechRejectsOneHundredMillisecondFragment(t *testing.T) {
	b := NewUtteranceBuffer(UtteranceLimits{}.WithDefaults())
	b.Append(pcmTone(100, 3000))
	if got := b.EmptyReason(); got != EmptyReasonTooShort {
		t.Fatalf("a 100ms fragment must not be recognised by default, got %q", got)
	}
}

func TestUtteranceBufferReturnsOverflowAtMaxLength(t *testing.T) {
	b := NewUtteranceBuffer(testLimits()) // MaxLength 2s
	if overflow := b.Append(pcmTone(1500, 3000)); len(overflow) != 0 {
		t.Fatalf("unexpected overflow %d", len(overflow))
	}
	overflow := b.Append(pcmTone(1000, 3000))
	if got := b.Duration(); got != 2*time.Second {
		t.Fatalf("buffer must stop at MaxLength, Duration() = %s", got)
	}
	if got := len(overflow); got != 500*DefaultSampleRate/1000*2 {
		t.Fatalf("overflow = %d bytes, want 500ms of PCM", got)
	}
	again := b.Append(pcmTone(100, 3000))
	if len(again) != len(pcmTone(100, 3000)) {
		t.Fatalf("a full buffer must hand every byte back, got %d", len(again))
	}
	if got := b.Duration(); got != 2*time.Second {
		t.Fatalf("full buffer grew to %s", got)
	}
}

func TestUtteranceBufferCarriesOddTrailingByteAcrossFrames(t *testing.T) {
	b := NewUtteranceBuffer(testLimits())
	frame := pcmTone(100, 3000)
	b.Append(frame[:len(frame)-1])
	b.Append(frame[len(frame)-1:])
	if got := b.Duration(); got != 100*time.Millisecond {
		t.Fatalf("odd split must still total 100ms, got %s", got)
	}
	wav := b.WAV()
	if got := binary.LittleEndian.Uint32(wav[40:44]); got != uint32(len(frame)) {
		t.Fatalf("data size = %d, want %d", got, len(frame))
	}
}

func TestUtteranceBufferWAVIsCanonical16kMonoPCM16(t *testing.T) {
	b := NewUtteranceBuffer(testLimits())
	b.Append(pcmTone(250, 1234))
	wav := b.WAV()
	if !IsWAV(wav) {
		t.Fatal("WAV() must start with a RIFF/WAVE header")
	}
	dataBytes := 250 * DefaultSampleRate / 1000 * 2
	if len(wav) != 44+dataBytes {
		t.Fatalf("len(wav) = %d, want %d", len(wav), 44+dataBytes)
	}
	if got := binary.LittleEndian.Uint32(wav[4:8]); got != uint32(36+dataBytes) {
		t.Fatalf("RIFF size = %d", got)
	}
	if got := binary.LittleEndian.Uint16(wav[20:22]); got != 1 {
		t.Fatalf("audio format = %d, want PCM", got)
	}
	if got := binary.LittleEndian.Uint16(wav[22:24]); got != 1 {
		t.Fatalf("channels = %d, want mono", got)
	}
	if got := binary.LittleEndian.Uint32(wav[24:28]); got != 16000 {
		t.Fatalf("sample rate = %d, want 16000", got)
	}
	if got := binary.LittleEndian.Uint32(wav[28:32]); got != 32000 {
		t.Fatalf("byte rate = %d, want 32000", got)
	}
	if got := binary.LittleEndian.Uint16(wav[32:34]); got != 2 {
		t.Fatalf("block align = %d, want 2", got)
	}
	if got := binary.LittleEndian.Uint16(wav[34:36]); got != 16 {
		t.Fatalf("bits per sample = %d, want 16", got)
	}
	if got := binary.LittleEndian.Uint32(wav[40:44]); got != uint32(dataBytes) {
		t.Fatalf("data size = %d, want %d", got, dataBytes)
	}
	if !bytes.Equal(wav[44:], pcmTone(250, 1234)) {
		t.Fatal("WAV payload must be the appended PCM unchanged")
	}
}

func TestIsVoicedPCM(t *testing.T) {
	if IsVoicedPCM(pcmSilence(100), 220) {
		t.Fatal("silence must not be voiced")
	}
	if !IsVoicedPCM(pcmTone(100, 3000), 220) {
		t.Fatal("tone must be voiced")
	}
	if IsVoicedPCM(nil, 220) || IsVoicedPCM([]byte{1}, 220) {
		t.Fatal("fewer than one sample is not voiced")
	}
}

func TestWAVToPCM16ReadsCanonicalAndExtendedHeaders(t *testing.T) {
	pcm := pcmTone(100, 500)
	pcmOut, err := WAVToPCM16(PCM16LEToWAV(pcm, 16000))
	if err != nil || !bytes.Equal(pcmOut, pcm) {
		t.Fatalf("canonical WAV: err=%v equal=%t", err, bytes.Equal(pcmOut, pcm))
	}

	canonical := PCM16LEToWAV(pcm, 16000)
	extra := []byte("LIST\x04\x00\x00\x00abcd")
	withExtra := append([]byte{}, canonical[:36]...)
	withExtra = append(withExtra, extra...)
	withExtra = append(withExtra, canonical[36:]...)
	pcmOut, err = WAVToPCM16(withExtra)
	if err != nil || !bytes.Equal(pcmOut, pcm) {
		t.Fatalf("WAV with extra chunk before data: err=%v equal=%t", err, bytes.Equal(pcmOut, pcm))
	}

	streaming := append([]byte{}, canonical...)
	binary.LittleEndian.PutUint32(streaming[40:44], 0xFFFFFFFF)
	pcmOut, err = WAVToPCM16(streaming)
	if err != nil || !bytes.Equal(pcmOut, pcm) {
		t.Fatalf("streaming WAV with unknown data size: err=%v equal=%t", err, bytes.Equal(pcmOut, pcm))
	}
}

func TestWAVToPCM16RejectsUnsupportedFormats(t *testing.T) {
	pcm := pcmTone(100, 500)
	for name, mutate := range map[string]func([]byte){
		"48k rate":  func(w []byte) { binary.LittleEndian.PutUint32(w[24:28], 48000) },
		"stereo":    func(w []byte) { binary.LittleEndian.PutUint16(w[22:24], 2) },
		"8 bit":     func(w []byte) { binary.LittleEndian.PutUint16(w[34:36], 8) },
		"float fmt": func(w []byte) { binary.LittleEndian.PutUint16(w[20:22], 3) },
	} {
		wav := PCM16LEToWAV(pcm, 16000)
		mutate(wav)
		if _, err := WAVToPCM16(wav); err == nil {
			t.Fatalf("%s: expected an unsupported-format error", name)
		}
	}
	if _, err := WAVToPCM16([]byte("RIFFxxxxWAVE")); err == nil {
		t.Fatal("WAV without fmt/data chunks must be rejected")
	}
}

func TestUtteranceBufferAlwaysMakesProgressEvenWithTinyMaxLength(t *testing.T) {
	b := NewUtteranceBuffer(UtteranceLimits{MaxLength: time.Nanosecond}.WithDefaults())
	frame := pcmTone(100, 3000)
	overflow := b.Append(frame)
	if b.Duration() == 0 {
		t.Fatal("a buffer that accepts nothing makes a caller that loops on overflow spin forever")
	}
	if len(overflow) >= len(frame) {
		t.Fatalf("overflow %d bytes of %d: no progress", len(overflow), len(frame))
	}
}
