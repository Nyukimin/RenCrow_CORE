package tts

import "testing"

func TestNormalizePlaybackAckTrimsExplicitError(t *testing.T) {
	got := NormalizePlaybackAck(PlaybackAckInput{
		Status:    " error ",
		ErrorCode: " TTS_AUDIO_DISABLED ",
		Error:     " disabled ",
	})
	if got.Status != "error" || got.ErrorCode != "TTS_AUDIO_DISABLED" || got.Error != "disabled" {
		t.Fatalf("unexpected normalized ack: %+v", got)
	}
}

func TestNormalizePlaybackAckConvertsDeprecatedFallbackToError(t *testing.T) {
	got := NormalizePlaybackAck(PlaybackAckInput{Status: "fallback"})
	if got.Status != PlaybackAckStatusError {
		t.Fatalf("status = %q, want %q", got.Status, PlaybackAckStatusError)
	}
	if got.ErrorCode != DeprecatedFallbackAckErrorCode {
		t.Fatalf("error code = %q, want %q", got.ErrorCode, DeprecatedFallbackAckErrorCode)
	}
	if got.Error != DeprecatedFallbackAckErrorText {
		t.Fatalf("error text = %q, want %q", got.Error, DeprecatedFallbackAckErrorText)
	}
}

func TestNormalizePlaybackAckKeepsFallbackErrorDetails(t *testing.T) {
	got := NormalizePlaybackAck(PlaybackAckInput{
		Status:    "fallback",
		ErrorCode: "TTS_AUDIO_BLOCKED",
		Error:     "browser blocked audio",
	})
	if got.Status != "error" || got.ErrorCode != "TTS_AUDIO_BLOCKED" || got.Error != "browser blocked audio" {
		t.Fatalf("unexpected normalized ack: %+v", got)
	}
}

func TestBuildPlaybackAckReceiptTrimsFieldsAndPreservesMatchState(t *testing.T) {
	got := BuildPlaybackAckReceipt(PlaybackAckInput{
		ResponseID:     " response-1 ",
		SessionID:      " session-1 ",
		UtteranceID:    " utterance-1 ",
		ViewerClientID: " viewer-1 ",
		Status:         " ended ",
	}, true, true)

	if !got.OK || !got.Matched || !got.ActiveAudio {
		t.Fatalf("unexpected receipt flags: %+v", got)
	}
	if got.ResponseID != "response-1" ||
		got.SessionID != "session-1" ||
		got.UtteranceID != "utterance-1" ||
		got.ViewerClientID != "viewer-1" ||
		got.Status != "ended" {
		t.Fatalf("unexpected receipt fields: %+v", got)
	}
}

func TestShouldConsumePendingForPlaybackAckRequiresActiveAudio(t *testing.T) {
	if !ShouldConsumePendingForPlaybackAck(true) {
		t.Fatal("active audio ack should consume pending")
	}
	if ShouldConsumePendingForPlaybackAck(false) {
		t.Fatal("inactive audio ack must not consume pending")
	}
}
