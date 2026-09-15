package tts

import (
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestBuildSynthesisResult(t *testing.T) {
	requestID, responseID := core.NewRequestID(), core.NewResponseID()
	got := BuildSynthesisResult(SynthesisRequest{
		SessionID:   "s1",
		UtteranceID: "u1",
		CharacterID: "mio",
		SpeechText:  "😊こんにちは",
		DisplayText: "こんにちは",
	}, SynthesisOutput{
		RequestID:  requestID,
		ResponseID: responseID,
		AudioPath:  "/tmp/audio.wav",
		AudioURL:   "/audio.wav",
		DurationMS: 1500,
	})

	if len(got.Chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %+v", got.Chunks)
	}
	chunk := got.Chunks[0]
	if got.RequestID != requestID || got.ResponseID != responseID ||
		chunk.Ref.SessionID != "s1" || chunk.Ref.ResponseID != responseID || chunk.Ref.UtteranceID != "u1" {
		t.Fatalf("chunk ref was not mapped: %+v", chunk.Ref)
	}
	if chunk.CharacterID != "mio" || chunk.SpeechText != "😊こんにちは" || chunk.DisplayText != "こんにちは" {
		t.Fatalf("text metadata was not mapped: %+v", chunk)
	}
	if chunk.AudioPath != "/tmp/audio.wav" || chunk.AudioURL != "/audio.wav" || chunk.Duration != 1500*time.Millisecond {
		t.Fatalf("audio output was not mapped: %+v", chunk)
	}
}
