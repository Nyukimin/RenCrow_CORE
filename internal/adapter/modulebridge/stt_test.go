package modulebridge

import (
	"context"
	"testing"
	"time"

	internalstt "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/stt"
	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
	modulestt "github.com/Nyukimin/picoclaw_multiLLM/modules/stt"
)

type fakeInternalSTTProvider struct {
	audio []byte
}

func (p *fakeInternalSTTProvider) Name() string {
	return "fake-stt"
}

func (p *fakeInternalSTTProvider) Health(context.Context) internalstt.Health {
	return internalstt.Health{
		Status:   "ready",
		Provider: "fake-stt",
		Model:    "whisper",
		Device:   "cpu",
		Ready:    true,
	}
}

func (p *fakeInternalSTTProvider) Transcribe(_ context.Context, wav []byte) (internalstt.Result, error) {
	p.audio = wav
	return internalstt.Result{
		Text:         "こんにちは",
		Language:     "ja",
		Duration:     1.25,
		Provider:     "fake-stt",
		Model:        "whisper",
		ProcessingMS: 345,
		Segments: []internalstt.Segment{
			{Start: 0.5, End: 1.25, Text: "こんにちは"},
		},
	}, nil
}

func TestSTTProviderAdapterTranscribe(t *testing.T) {
	provider := &fakeInternalSTTProvider{}
	adapter := NewSTTProviderAdapter(provider)

	health := adapter.Health(context.Background())
	if health.Status != core.HealthReady {
		t.Fatalf("unexpected health: %+v", health)
	}

	got, err := adapter.Transcribe(context.Background(), modulestt.TranscriptionRequest{
		RequestID: core.RequestID("req1"),
		Audio:     []byte("wav"),
		Format:    modulestt.AudioFormatWAV,
	})
	if err != nil {
		t.Fatalf("Transcribe returned error: %v", err)
	}
	if string(provider.audio) != "wav" {
		t.Fatalf("audio was not forwarded: %q", string(provider.audio))
	}
	if got.RequestID != "req1" || got.Text != "こんにちは" || got.Language != "ja" {
		t.Fatalf("result fields were not mapped: %+v", got)
	}
	if got.Duration != 1250*time.Millisecond {
		t.Fatalf("duration was not mapped: %s", got.Duration)
	}
	if len(got.Segments) != 1 || got.Segments[0].Start != 500*time.Millisecond || got.Segments[0].End != 1250*time.Millisecond {
		t.Fatalf("segments were not mapped: %+v", got.Segments)
	}
}

func TestNewRuntimeSTTProviderAdapter(t *testing.T) {
	provider := &fakeInternalSTTProvider{}
	adapter := NewRuntimeSTTProviderAdapter(provider)

	health := adapter.Health(context.Background())
	if health.Status != core.HealthReady || !health.Ready {
		t.Fatalf("unexpected runtime adapter health: %+v", health)
	}
}
