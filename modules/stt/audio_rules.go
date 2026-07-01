package stt

import (
	"encoding/json"
	"errors"
	"net"
	"strings"
	"time"
)

const (
	DefaultFinalTimeout        = 1200 * time.Millisecond
	MinFinalTimeout            = 200 * time.Millisecond
	DefaultSilenceAbsThreshold = 220
	DefaultHTTPTimeout         = 3000 * time.Millisecond
	MinHTTPTimeout             = 300 * time.Millisecond
	DefaultSampleRate          = 16000
)

func FinalTimeoutFromMilliseconds(ms int) time.Duration {
	if ms < int(MinFinalTimeout/time.Millisecond) {
		return DefaultFinalTimeout
	}
	return time.Duration(ms) * time.Millisecond
}

func SilenceAbsThreshold(value int) int {
	if value < 1 {
		return DefaultSilenceAbsThreshold
	}
	return value
}

func HTTPTimeoutFromMilliseconds(ms int) time.Duration {
	if ms < int(MinHTTPTimeout/time.Millisecond) {
		return DefaultHTTPTimeout
	}
	return time.Duration(ms) * time.Millisecond
}

func ParseControlMessage(payload []byte) (string, bool) {
	if len(payload) == 0 {
		return "", false
	}
	lead := payload[0]
	if lead != '{' && lead != '[' && lead != '"' {
		return "", false
	}
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		return "", false
	}
	msgType, _ := obj["type"].(string)
	if strings.TrimSpace(msgType) != "" {
		return strings.TrimSpace(msgType), true
	}
	return "", false
}

func NormalizeAudioPayload(payload []byte) []byte {
	if IsWAV(payload) {
		return payload
	}
	if len(payload) < 2 {
		return payload
	}
	audioLen := len(payload)
	if audioLen%2 != 0 {
		audioLen--
	}
	return PCM16LEToWAV(payload[:audioLen], DefaultSampleRate)
}

func IsLikelySilentWAV(wav []byte, absThreshold int) bool {
	if len(wav) <= 44 {
		return false
	}
	if !IsWAV(wav) {
		return false
	}
	sampleBytes := wav[44:]
	if len(sampleBytes) < 2 {
		return false
	}
	var sum int64
	var n int64
	for i := 0; i+1 < len(sampleBytes); i += 2 {
		s := int16(sampleBytes[i]) | int16(sampleBytes[i+1])<<8
		if s < 0 {
			sum += int64(-s)
		} else {
			sum += int64(s)
		}
		n++
	}
	if n == 0 {
		return false
	}
	avgAbs := int(sum / n)
	return avgAbs < absThreshold
}

func IsWAV(b []byte) bool {
	return len(b) >= 44 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WAVE"
}

func PCM16LEToWAV(pcm []byte, sampleRate int) []byte {
	if sampleRate <= 0 {
		sampleRate = DefaultSampleRate
	}
	dataSize := len(pcm)
	out := make([]byte, 44+dataSize)
	copy(out[0:4], "RIFF")
	putLE32(out[4:8], uint32(36+dataSize))
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	putLE32(out[16:20], 16)
	putLE16(out[20:22], 1)
	putLE16(out[22:24], 1)
	putLE32(out[24:28], uint32(sampleRate))
	putLE32(out[28:32], uint32(sampleRate*2))
	putLE16(out[32:34], 2)
	putLE16(out[34:36], 16)
	copy(out[36:40], "data")
	putLE32(out[40:44], uint32(dataSize))
	copy(out[44:], pcm)
	return out
}

func AdjustAdaptiveTimeout(cur, delta, minV, maxV time.Duration) time.Duration {
	next := cur + delta
	if next < minV {
		return minV
	}
	if next > maxV {
		return maxV
	}
	return next
}

func IsTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "client.timeout exceeded") || strings.Contains(msg, "context deadline exceeded")
}

func putLE16(dst []byte, v uint16) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
}

func putLE32(dst []byte, v uint32) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
	dst[2] = byte(v >> 16)
	dst[3] = byte(v >> 24)
}
