package main

import (
	"testing"
	"time"
)

func TestSTTTimingTraceSnapshotMeasuresFirstAudioVoiceProvisionalAndFinalGaps(t *testing.T) {
	start := time.Unix(100, 0)
	trace := &sttTimingTrace{
		sessionID:          "session-1",
		mode:               "direct",
		startedAt:          start,
		firstAudioAt:       start.Add(120 * time.Millisecond),
		firstVoiceAt:       start.Add(260 * time.Millisecond),
		firstProvisionalAt: start.Add(540 * time.Millisecond),
		lastVoiceAt:        start.Add(1400 * time.Millisecond),
	}

	snap := trace.snapshot(start.Add(1850 * time.Millisecond))
	if snap.FirstAudioMS != "120.0" {
		t.Fatalf("FirstAudioMS = %q", snap.FirstAudioMS)
	}
	if snap.FirstVoiceMS != "260.0" {
		t.Fatalf("FirstVoiceMS = %q", snap.FirstVoiceMS)
	}
	if snap.FirstProvisionalMS != "540.0" {
		t.Fatalf("FirstProvisionalMS = %q", snap.FirstProvisionalMS)
	}
	if snap.SilenceToFinalMS != "450.0" {
		t.Fatalf("SilenceToFinalMS = %q", snap.SilenceToFinalMS)
	}
	if snap.TotalMS != "1850.0" {
		t.Fatalf("TotalMS = %q", snap.TotalMS)
	}
}
