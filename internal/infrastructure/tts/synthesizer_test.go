package tts

import (
	"context"
	"errors"
	"testing"
)

type synthProviderStub struct {
	name string
	out  SynthesisOutput
	err  error
}

func (s synthProviderStub) Name() string { return s.name }

func (s synthProviderStub) Synthesize(_ context.Context, _ SynthesisInput) (SynthesisOutput, error) {
	return s.out, s.err
}

func TestFallbackSynthesizer_UsesFirstSuccess(t *testing.T) {
	s := NewFallbackSynthesizer(
		synthProviderStub{name: "sbv2", err: ErrProviderUnavailable},
		synthProviderStub{name: "azure", out: SynthesisOutput{Provider: "azure", AudioFilePath: "/tmp/a.wav"}},
		synthProviderStub{name: "eleven", out: SynthesisOutput{Provider: "eleven", AudioFilePath: "/tmp/b.wav"}},
	)
	got, err := s.Synthesize(context.Background(), SynthesisInput{Text: "hello"})
	if err != nil {
		t.Fatalf("expected success, got err=%v", err)
	}
	if got.Provider != "azure" || got.AudioFilePath == "" {
		t.Fatalf("unexpected output: %+v", got)
	}
}

func TestFallbackSynthesizer_AllFailed(t *testing.T) {
	s := NewFallbackSynthesizer(
		synthProviderStub{name: "sbv2", err: ErrProviderUnavailable},
		synthProviderStub{name: "azure", err: errors.New("boom")},
	)
	_, err := s.Synthesize(context.Background(), SynthesisInput{Text: "hello"})
	if err == nil {
		t.Fatal("expected error")
	}
}

