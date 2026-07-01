package modulebridge

import (
	"context"
	"testing"
	"time"

	internaltts "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/tts"
	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
	moduletts "github.com/Nyukimin/picoclaw_multiLLM/modules/tts"
)

type fakeInternalTTSProvider struct {
	req internaltts.SynthesisInput
}

func (p *fakeInternalTTSProvider) Name() string {
	return "fake-tts"
}

func (p *fakeInternalTTSProvider) Synthesize(_ context.Context, req internaltts.SynthesisInput) (internaltts.SynthesisOutput, error) {
	p.req = req
	return internaltts.SynthesisOutput{
		Provider:      "fake-tts",
		VoiceID:       req.VoiceProfile.VoiceID,
		AudioFilePath: "/tmp/audio.wav",
		DurationMS:    1500,
	}, nil
}

func TestTTSProviderAdapterSynthesize(t *testing.T) {
	provider := &fakeInternalTTSProvider{}
	adapter := NewTTSProviderAdapter(provider, "/tmp/out", "prefix")

	got, err := adapter.Synthesize(context.Background(), moduletts.SynthesisRequest{
		SessionID:   core.SessionID("s1"),
		ResponseID:  core.ResponseID("r1"),
		UtteranceID: core.UtteranceID("u1"),
		CharacterID: "mio",
		VoiceID:     "female_01",
		SpeechText:  "😊こんにちは",
		DisplayText: "こんにちは",
		Emotion: &moduletts.EmotionState{
			PrimaryEmotion: "😊",
			VoiceProfile:   "mio",
		},
	})
	if err != nil {
		t.Fatalf("Synthesize returned error: %v", err)
	}
	if provider.req.Text != "😊こんにちは" || provider.req.VoiceProfile.VoiceID != "female_01" {
		t.Fatalf("request fields were not mapped: %+v", provider.req)
	}
	if provider.req.OutputDir != "/tmp/out" || provider.req.FilePrefix != "prefix" {
		t.Fatalf("output options were not mapped: %+v", provider.req)
	}
	if provider.req.Emotion.Emotion != "😊" {
		t.Fatalf("emotion was not mapped: %+v", provider.req.Emotion)
	}
	if len(got.Chunks) != 1 {
		t.Fatalf("expected 1 audio chunk, got %+v", got.Chunks)
	}
	chunk := got.Chunks[0]
	if chunk.Ref.SessionID != "s1" || chunk.Ref.ResponseID != "r1" || chunk.Ref.UtteranceID != "u1" {
		t.Fatalf("chunk ref was not mapped: %+v", chunk.Ref)
	}
	if chunk.AudioPath != "/tmp/audio.wav" || chunk.Duration != 1500*time.Millisecond {
		t.Fatalf("audio fields were not mapped: %+v", chunk)
	}
}

func TestNewRuntimeTTSProviderAdapterUsesRuntimePrefix(t *testing.T) {
	provider := &fakeInternalTTSProvider{}
	adapter := NewRuntimeTTSProviderAdapter(provider, "/tmp/out")

	_, err := adapter.Synthesize(context.Background(), moduletts.SynthesisRequest{
		SpeechText: "hello",
	})
	if err != nil {
		t.Fatalf("Synthesize returned error: %v", err)
	}
	if provider.req.OutputDir != "/tmp/out" || provider.req.FilePrefix != runtimeTTSModuleFilePrefix {
		t.Fatalf("runtime output options were not mapped: %+v", provider.req)
	}
}
