package main

import (
	"testing"
	"time"
)

func TestSTTTimingTraceSnapshotMeasuresWhatTheRelayCanObserve(t *testing.T) {
	start := time.Unix(100, 0)
	trace := newSTTTimingTrace("relay", start)
	trace.markStop(start.Add(1400 * time.Millisecond))

	snap := trace.snapshot(start.Add(1850 * time.Millisecond))
	if snap.StopToFinalMS != "450.0" {
		t.Fatalf("StopToFinalMS = %q", snap.StopToFinalMS)
	}
	if snap.TotalMS != "1850.0" {
		t.Fatalf("TotalMS = %q", snap.TotalMS)
	}
}

func TestSTTTimingTraceWithoutAStopHasNoStopToFinal(t *testing.T) {
	// PORTAL never sends stop while it talks: STT closes the utterance by silence,
	// so there is no moment on the relay's side to measure from.
	start := time.Unix(100, 0)
	snap := newSTTTimingTrace("relay", start).snapshot(start.Add(time.Second))
	if snap.StopToFinalMS != "n/a" || snap.TotalMS != "1000.0" {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestSTTTimingTraceKeepsTheFirstStop(t *testing.T) {
	start := time.Unix(100, 0)
	trace := newSTTTimingTrace("relay", start)
	trace.markStop(start.Add(time.Second))
	trace.markStop(start.Add(2 * time.Second)) // a repeated stop is not a later stop
	if snap := trace.snapshot(start.Add(3 * time.Second)); snap.StopToFinalMS != "2000.0" {
		t.Fatalf("StopToFinalMS = %q", snap.StopToFinalMS)
	}
}
