package main

import (
	"testing"
	"time"
)

func TestCapacityBackoffDoublesUpToItsBoundAndResets(t *testing.T) {
	backoff := newCapacityBackoff(10*time.Second, 5*time.Minute)
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)

	want := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second, 5 * time.Minute, 5 * time.Minute}
	for i, expected := range want {
		if got := backoff.refused(now); got != expected {
			t.Fatalf("delay after refusal %d = %s, want %s", i+1, got, expected)
		}
		if !backoff.waiting(now.Add(expected - time.Nanosecond)) {
			t.Fatalf("refusal %d: not waiting just before the delay ends", i+1)
		}
		if backoff.waiting(now.Add(expected)) {
			t.Fatalf("refusal %d: still waiting when the delay ends", i+1)
		}
	}
	for i := 0; i < 200; i++ {
		if got := backoff.refused(now); got != 5*time.Minute {
			t.Fatalf("delay after %d more refusals = %s, want the bound (and no overflow)", i, got)
		}
	}
	backoff.succeeded()
	if backoff.waiting(now) {
		t.Fatal("a success must clear the wait")
	}
	if got := backoff.refused(now); got != 10*time.Second {
		t.Fatalf("delay after reset = %s, want the base", got)
	}
}
