package stt

import (
	"errors"
	"net"
	"strings"
	"time"
)

const (
	DefaultHTTPTimeout = 3000 * time.Millisecond
	MinHTTPTimeout     = 300 * time.Millisecond
	DefaultSampleRate  = 16000
)

func HTTPTimeoutFromMilliseconds(ms int) time.Duration {
	if ms < int(MinHTTPTimeout/time.Millisecond) {
		return DefaultHTTPTimeout
	}
	return time.Duration(ms) * time.Millisecond
}

// IsWAV reports whether b starts like a RIFF/WAVE file. /stt takes raw PCM16
// only, so a frame that does is refused.
func IsWAV(b []byte) bool {
	return len(b) >= 44 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WAVE"
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
