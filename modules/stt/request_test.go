package stt

import "testing"

func TestCloneTranscriptionRequestCopiesAudio(t *testing.T) {
	original := TranscriptionRequest{Audio: []byte{1, 2, 3}}
	got := CloneTranscriptionRequest(original)
	got.Audio[0] = 9

	if original.Audio[0] != 1 {
		t.Fatalf("audio was aliased: %+v", original.Audio)
	}
}
