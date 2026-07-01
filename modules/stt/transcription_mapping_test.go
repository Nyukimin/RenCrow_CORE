package stt

import (
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

func TestBuildTranscriptionResult(t *testing.T) {
	got := BuildTranscriptionResult(TranscriptionRequest{RequestID: "req1"}, TranscriptionOutput{
		Text:        "こんにちは",
		Language:    "ja",
		DurationSec: 1.25,
		Provider:    "fake-stt",
		Model:       "whisper",
		Segments: []SegmentOutput{
			{StartSeconds: 0.5, EndSeconds: 1.25, Text: "こんにちは"},
		},
		ProcessingMS: 345,
	})

	if got.RequestID != "req1" || got.Text != "こんにちは" || got.Language != "ja" {
		t.Fatalf("result fields were not mapped: %+v", got)
	}
	if got.Duration != 1250*time.Millisecond {
		t.Fatalf("duration = %s", got.Duration)
	}
	if len(got.Segments) != 1 || got.Segments[0].Start != 500*time.Millisecond || got.Segments[0].End != 1250*time.Millisecond {
		t.Fatalf("segments were not mapped: %+v", got.Segments)
	}
	if got.Provider != "fake-stt" || got.Model != "whisper" || got.ProcessingMS != 345 {
		t.Fatalf("provider metadata was not mapped: %+v", got)
	}
}

func TestBuildProviderHealth(t *testing.T) {
	ready := BuildProviderHealth(ProviderHealthSnapshot{Status: "ready", Provider: "fake-stt", Model: "whisper", Device: "cpu", Ready: true})
	if ready.Status != core.HealthReady || !ready.Ready || ready.Metadata["provider"] != "fake-stt" {
		t.Fatalf("ready health not mapped: %+v", ready)
	}
	blocked := BuildProviderHealth(ProviderHealthSnapshot{Status: "loading", Ready: false})
	if blocked.Status != core.HealthBlocked || blocked.Ready {
		t.Fatalf("blocked health not mapped: %+v", blocked)
	}
}
