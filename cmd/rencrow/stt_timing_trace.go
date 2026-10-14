package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// sttTimingTrace measures one relayed utterance (STT_TIMING_TRACE=1). The relay
// cannot see speech, only the moments it forwards: when STT announced the
// utterance, when stop went to STT (the Viewer sends one) and when the terminal
// event came back.
type sttTimingTrace struct {
	mode      string
	startedAt time.Time
	stopAt    time.Time
}

type sttTimingSnapshot struct {
	Mode          string
	StopToFinalMS string
	TotalMS       string
}

func newSTTTimingTrace(mode string, startedAt time.Time) *sttTimingTrace {
	return &sttTimingTrace{mode: strings.TrimSpace(mode), startedAt: startedAt}
}

func sttTimingTraceEnabled() bool {
	raw := strings.TrimSpace(os.Getenv("STT_TIMING_TRACE"))
	return raw != "" && raw != "0" && !strings.EqualFold(raw, "false")
}

// markStop records the first stop forwarded to STT.
func (t *sttTimingTrace) markStop(at time.Time) {
	if t.stopAt.IsZero() {
		t.stopAt = at
	}
}

func (t *sttTimingTrace) snapshot(terminalAt time.Time) sttTimingSnapshot {
	return sttTimingSnapshot{
		Mode:          t.mode,
		StopToFinalMS: sttTimingMillis(t.stopAt, terminalAt),
		TotalMS:       sttTimingMillis(t.startedAt, terminalAt),
	}
}

// logTerminal logs the utterance's timings. It never carries the text.
func (t *sttTimingTrace) logTerminal(eventType string, terminalAt time.Time) {
	snap := t.snapshot(terminalAt)
	log.Printf("[STT][timing] mode=%s event=%s stop_to_final_ms=%s total_ms=%s",
		snap.Mode, strings.TrimSpace(eventType), snap.StopToFinalMS, snap.TotalMS)
}

func sttTimingMillis(start, end time.Time) string {
	if start.IsZero() || end.IsZero() {
		return "n/a"
	}
	return fmt.Sprintf("%.1f", end.Sub(start).Seconds()*1000)
}
