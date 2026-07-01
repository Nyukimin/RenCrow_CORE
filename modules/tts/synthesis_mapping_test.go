package tts

import (
	"testing"
	"time"
)

func TestBuildSynthesisResult(t *testing.T) {
	got := BuildSynthesisResult(SynthesisRequest{
		SessionID:   "s1",
		ResponseID:  "r1",
		UtteranceID: "u1",
		CharacterID: "mio",
		SpeechText:  "😊こんにちは",
		DisplayText: "こんにちは",
	}, SynthesisOutput{
		AudioPath:  "/tmp/audio.wav",
		AudioURL:   "/audio.wav",
		DurationMS: 1500,
	})

	if len(got.Chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %+v", got.Chunks)
	}
	chunk := got.Chunks[0]
	if chunk.Ref.SessionID != "s1" || chunk.Ref.ResponseID != "r1" || chunk.Ref.UtteranceID != "u1" {
		t.Fatalf("chunk ref was not mapped: %+v", chunk.Ref)
	}
	if chunk.CharacterID != "mio" || chunk.SpeechText != "😊こんにちは" || chunk.DisplayText != "こんにちは" {
		t.Fatalf("text metadata was not mapped: %+v", chunk)
	}
	if chunk.AudioPath != "/tmp/audio.wav" || chunk.AudioURL != "/audio.wav" || chunk.Duration != 1500*time.Millisecond {
		t.Fatalf("audio output was not mapped: %+v", chunk)
	}
}
