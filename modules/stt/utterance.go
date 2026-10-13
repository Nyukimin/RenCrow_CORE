package stt

import (
	"encoding/binary"
	"errors"
	"time"
)

// Utterance accumulation rules. The Viewer endpoints speech with its own VAD;
// CORE accumulates the PCM of one utterance and asks RenCrow_STT to transcribe
// it exactly once. The limits below are CORE-side safety valves, not a second
// endpointer: MaxLength bounds memory and latency, IdleTimeout covers a client
// that stops sending without a stop control.

const (
	DefaultMinSpeech            = 150 * time.Millisecond
	DefaultMaxUtterance         = 30 * time.Second
	DefaultUtteranceIdleTimeout = 2 * time.Second

	// EndReason* say why CORE closed an utterance.
	EndReasonStop        = "stop"
	EndReasonMaxLength   = "max_length"
	EndReasonIdleTimeout = "idle_timeout"
	EndReasonRestart     = "restart"

	// EmptyReason* say why a final carries no text.
	EmptyReasonNoAudio           = "no_audio"
	EmptyReasonSilent            = "silent"
	EmptyReasonTooShort          = "too_short"
	EmptyReasonFiltered          = "filtered"
	EmptyReasonNoSpeech          = "no_speech"
	EmptyReasonNoActiveUtterance = "no_active_utterance"

	utteranceWindowSamples = DefaultSampleRate / 50 // 20 ms
	utteranceWindow        = 20 * time.Millisecond
	pcmBytesPerSample      = 2
)

// ErrUnsupportedAudioFormat is returned for audio that is not 16 kHz mono PCM16.
var ErrUnsupportedAudioFormat = errors.New("unsupported audio format: 16 kHz mono PCM16 is required")

// UtteranceLimits are the CORE-side bounds of one utterance.
type UtteranceLimits struct {
	// MinSpeech is the least voiced audio worth sending to the engine.
	MinSpeech time.Duration
	// MaxLength closes an utterance and continues in a new one.
	MaxLength time.Duration
	// IdleTimeout closes an utterance whose frames stopped arriving.
	IdleTimeout time.Duration
	// SilenceAbsThreshold is the mean absolute PCM16 value from which a 20 ms
	// window counts as voiced.
	SilenceAbsThreshold int
}

// WithDefaults fills zero values and keeps MinSpeech below MaxLength.
func (l UtteranceLimits) WithDefaults() UtteranceLimits {
	if l.MinSpeech <= 0 {
		l.MinSpeech = DefaultMinSpeech
	}
	if l.MaxLength <= 0 {
		l.MaxLength = DefaultMaxUtterance
	}
	if l.IdleTimeout <= 0 {
		l.IdleTimeout = DefaultUtteranceIdleTimeout
	}
	l.SilenceAbsThreshold = SilenceAbsThreshold(l.SilenceAbsThreshold)
	if l.MinSpeech >= l.MaxLength {
		l.MinSpeech = l.MaxLength / 2
	}
	return l
}

// UtteranceBuffer accumulates 16 kHz mono PCM16LE for one utterance. It is not
// safe for concurrent use; the session loop owns it.
type UtteranceBuffer struct {
	limits   UtteranceLimits
	maxBytes int
	pcm      []byte

	hasCarry  bool
	carryByte byte

	windowSum     int
	windowSamples int
	voicedWindows int
}

// NewUtteranceBuffer returns an empty buffer bounded by limits.MaxLength.
func NewUtteranceBuffer(limits UtteranceLimits) *UtteranceBuffer {
	limits = limits.WithDefaults()
	maxSamples := int(limits.MaxLength * DefaultSampleRate / time.Second)
	if maxSamples < utteranceWindowSamples {
		maxSamples = utteranceWindowSamples // a caller that loops on overflow must progress
	}
	return &UtteranceBuffer{limits: limits, maxBytes: maxSamples * pcmBytesPerSample}
}

// Append adds PCM16LE bytes. When MaxLength is reached the bytes that did not
// fit are returned so the caller can close this utterance and continue in a new
// one. An odd trailing byte is carried to the next call.
func (b *UtteranceBuffer) Append(pcm []byte) []byte {
	if len(pcm) == 0 {
		return nil
	}
	all := pcm
	if b.hasCarry {
		all = append([]byte{b.carryByte}, pcm...)
		b.hasCarry = false
	}
	even := len(all) &^ 1
	room := (b.maxBytes - len(b.pcm)) &^ 1
	if room < 0 {
		room = 0
	}
	take := even
	if take > room {
		take = room
	}
	b.consume(all[:take])
	if take < even {
		return append([]byte(nil), all[take:]...)
	}
	if even < len(all) {
		b.carryByte = all[len(all)-1]
		b.hasCarry = true
	}
	return nil
}

func (b *UtteranceBuffer) consume(data []byte) {
	for i := 0; i+1 < len(data); i += pcmBytesPerSample {
		sample := int(int16(binary.LittleEndian.Uint16(data[i:])))
		if sample < 0 {
			sample = -sample
		}
		b.windowSum += sample
		b.windowSamples++
		if b.windowSamples == utteranceWindowSamples {
			if b.windowSum/utteranceWindowSamples >= b.limits.SilenceAbsThreshold {
				b.voicedWindows++
			}
			b.windowSum, b.windowSamples = 0, 0
		}
	}
	b.pcm = append(b.pcm, data...)
}

// Duration is the accumulated audio length.
func (b *UtteranceBuffer) Duration() time.Duration {
	return time.Duration(len(b.pcm)/pcmBytesPerSample) * time.Second / DefaultSampleRate
}

// Voiced is the total length of 20 ms windows at or above the voiced threshold.
func (b *UtteranceBuffer) Voiced() time.Duration {
	return time.Duration(b.voicedWindows) * utteranceWindow
}

// EmptyReason is why the utterance must not be sent to the engine, or "" when
// it carries enough speech.
func (b *UtteranceBuffer) EmptyReason() string {
	switch {
	case len(b.pcm) == 0:
		return EmptyReasonNoAudio
	case b.voicedWindows == 0:
		return EmptyReasonSilent
	case b.Voiced() < b.limits.MinSpeech:
		return EmptyReasonTooShort
	default:
		return ""
	}
}

// WAV returns the utterance as a canonical 16 kHz mono PCM16 WAV.
func (b *UtteranceBuffer) WAV() []byte {
	return PCM16LEToWAV(b.pcm, DefaultSampleRate)
}

// IsVoicedPCM reports whether the mean absolute value of pcm reaches
// absThreshold. The session uses it to start an utterance from raw audio.
func IsVoicedPCM(pcm []byte, absThreshold int) bool {
	samples := len(pcm) / pcmBytesPerSample
	if samples == 0 {
		return false
	}
	sum := 0
	for i := 0; i+1 < len(pcm); i += pcmBytesPerSample {
		sample := int(int16(binary.LittleEndian.Uint16(pcm[i:])))
		if sample < 0 {
			sample = -sample
		}
		sum += sample
	}
	return sum/samples >= absThreshold
}

// WAVToPCM16 extracts the PCM payload of a RIFF/WAVE chunk. Only 16 kHz mono
// 16-bit PCM is accepted. Header sizes are checked against the bytes actually
// present and never used to allocate: a data size of 0 or 0xFFFFFFFF is the
// streaming marker (the writer could not seek back) and takes the rest of the
// payload; any other data size larger than the payload is rejected.
func WAVToPCM16(wav []byte) ([]byte, error) {
	if !IsWAV(wav) {
		return nil, ErrUnsupportedAudioFormat
	}
	formatSeen := false
	for pos := 12; pos+8 <= len(wav); {
		id := string(wav[pos : pos+4])
		size := int64(binary.LittleEndian.Uint32(wav[pos+4 : pos+8]))
		body := pos + 8
		switch id {
		case "fmt ":
			if size < 16 || body+16 > len(wav) {
				return nil, ErrUnsupportedAudioFormat
			}
			format := binary.LittleEndian.Uint16(wav[body:])
			channels := binary.LittleEndian.Uint16(wav[body+2:])
			rate := binary.LittleEndian.Uint32(wav[body+4:])
			bits := binary.LittleEndian.Uint16(wav[body+14:])
			if format != 1 || channels != 1 || rate != DefaultSampleRate || bits != 16 {
				return nil, ErrUnsupportedAudioFormat
			}
			formatSeen = true
		case "data":
			if !formatSeen {
				return nil, ErrUnsupportedAudioFormat
			}
			end := int64(body) + size
			switch {
			case size == 0 || size == 0xFFFFFFFF:
				end = int64(len(wav))
			case end > int64(len(wav)):
				return nil, ErrUnsupportedAudioFormat
			}
			length := (int(end) - body) &^ 1
			return wav[body : body+length], nil
		}
		next := int64(body) + size + size&1
		if next > int64(len(wav)) {
			break
		}
		pos = int(next)
	}
	return nil, ErrUnsupportedAudioFormat
}
