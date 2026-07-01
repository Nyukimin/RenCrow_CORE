package tts

import "testing"

func TestBuildAudioChunkEventPayloadNormalizesDisplayAndTrack(t *testing.T) {
	got := BuildAudioChunkEventPayload(AudioChunkEventPayloadInput{
		SessionID:   " idle-1 ",
		ResponseID:  " r1 ",
		MessageID:   " m1 ",
		UtteranceID: " u1 ",
		ChunkIndex:  2,
		CharacterID: " mio ",
		SpeechText:  "😊本文",
		DisplayText: " ",
		AudioPath:   " /tmp/a.wav ",
	})

	if got.SessionID != "idle-1" || got.ResponseID != "r1" || got.CharacterID != "mio" {
		t.Fatalf("identity was not normalized: %+v", got)
	}
	if got.SpeechText != "😊本文" || got.Text != "😊本文" || got.DisplayText != "😊本文" {
		t.Fatalf("speech/display fields = %+v", got)
	}
	if got.Track != DefaultTrack || got.AudioPath != "/tmp/a.wav" {
		t.Fatalf("track/audio = %+v", got)
	}
}

func TestBuildSessionCompletedEventPayloadTrimsIdentity(t *testing.T) {
	got := BuildSessionCompletedEventPayload(SessionCompletedEventPayloadInput{
		SessionID:   " idle-1 ",
		ResponseID:  " r1 ",
		MessageID:   " m1 ",
		UtteranceID: " u1 ",
		CharacterID: " shiro ",
	})
	if got.SessionID != "idle-1" || got.ResponseID != "r1" || got.MessageID != "m1" || got.CharacterID != "shiro" {
		t.Fatalf("payload = %+v", got)
	}
}

func TestPlaybackEventRouteForSession(t *testing.T) {
	idle := PlaybackEventRouteForSession(" idle-123 ")
	if idle.Channel != EventChannelIdle || idle.ChatID != "idle-123" {
		t.Fatalf("idle route = %+v", idle)
	}
	viewer := PlaybackEventRouteForSession("normal")
	if viewer.Channel != EventChannelViewer || viewer.ChatID != EventChatIDViewer {
		t.Fatalf("viewer route = %+v", viewer)
	}
}
