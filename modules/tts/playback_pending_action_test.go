package tts

import "testing"

func TestBuildPendingPlaybackCompletionAction(t *testing.T) {
	got := BuildPendingPlaybackCompletionAction(" response-1 ", " tts-1 ", " idle-1 ", true)
	if !got.Matched || got.ResponseID != "response-1" || got.TTSSessionID != "tts-1" {
		t.Fatalf("unexpected completion action: %+v", got)
	}
	if !got.ClosePendingWait || !got.CloseTopicGate || got.ClearPublicBy != "response-1" {
		t.Fatalf("completion action did not request expected cleanup: %+v", got)
	}
}

func TestBuildPendingPlaybackCompletionActionUnmatched(t *testing.T) {
	got := BuildPendingPlaybackCompletionAction(" response-1 ", "tts-1", "idle-1", false)
	if got.Matched || got.ClosePendingWait || got.CloseTopicGate || got.ClearPublicBy != "" {
		t.Fatalf("unmatched completion should not request cleanup: %+v", got)
	}
	if got.ResponseID != "response-1" || got.TTSSessionID != "" || got.TopicIdleSessionID != "" {
		t.Fatalf("unmatched completion should only preserve requested response id: %+v", got)
	}
}

func TestBuildPendingPlaybackClearAction(t *testing.T) {
	got := BuildPendingPlaybackClearAction(" tts-1 ", " idle-1 ", true)
	if !got.Matched || got.TTSSessionID != "tts-1" || got.TopicIdleSessionID != "idle-1" {
		t.Fatalf("unexpected clear action: %+v", got)
	}
	if !got.ClosePendingWait || !got.CloseTopicGate || got.ClearPublicSession != "tts-1" {
		t.Fatalf("clear action did not request expected cleanup: %+v", got)
	}
}

func TestBuildPendingPlaybackClearActionUnmatched(t *testing.T) {
	got := BuildPendingPlaybackClearAction(" tts-1 ", "idle-1", false)
	if got.Matched || got.ClosePendingWait || got.CloseTopicGate || got.ClearPublicSession != "" {
		t.Fatalf("unmatched clear should not request cleanup: %+v", got)
	}
	if got.TTSSessionID != "tts-1" || got.TopicIdleSessionID != "" {
		t.Fatalf("unmatched clear should only preserve requested tts session id: %+v", got)
	}
}
