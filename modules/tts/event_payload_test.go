package tts

import (
	"encoding/json"
	"testing"
)

func TestBuildAudioChunkEventPayloadNormalizesDisplayAndTrack(t *testing.T) {
	got := BuildAudioChunkEventPayload(AudioChunkEventPayloadInput{
		SessionID:         " idle-1 ",
		PublicPlaybackRef: " r1 ",
		MessageID:         " m1 ",
		UtteranceID:       " u1 ",
		ChunkIndex:        2,
		CharacterID:       " mio ",
		SpeechText:        "😊本文",
		DisplayText:       " ",
		AudioPath:         " /tmp/a.wav ",
	})

	if got.SessionID != "idle-1" || got.PublicPlaybackRef != "r1" || got.CharacterID != "mio" {
		t.Fatalf("identity was not normalized: %+v", got)
	}
	if got.SpeechText != "😊本文" || got.Text != "😊本文" || got.DisplayText != "😊本文" {
		t.Fatalf("speech/display fields = %+v", got)
	}
	if got.Track != DefaultTrack || got.AudioPath != "/tmp/a.wav" {
		t.Fatalf("track/audio = %+v", got)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var legacyWire map[string]any
	if err := json.Unmarshal(raw, &legacyWire); err != nil {
		t.Fatal(err)
	}
	if _, ok := legacyWire["turn_id"]; ok {
		t.Fatalf("legacy payload gained turn_id: %s", raw)
	}
	if _, ok := legacyWire["pause_after_ms"]; ok {
		t.Fatalf("legacy payload gained pause_after_ms: %s", raw)
	}
	if _, ok := legacyWire["playback_output"]; ok {
		t.Fatalf("legacy payload gained playback_output: %s", raw)
	}
}

func TestBuildAudioChunkEventPayloadCarriesSpeechPlanMetadata(t *testing.T) {
	got := BuildAudioChunkEventPayload(AudioChunkEventPayloadInput{
		SessionID:         "tts-speech",
		PublicPlaybackRef: "task-ref",
		TurnID:            "turn_abc",
		PlaybackOutput:    "local",
		PauseAfterMS:      300,
		ChunkIndex:        1,
		SpeechText:        "続きます。",
	})
	if got.TurnID != "turn_abc" || got.PlaybackOutput != "local" || got.PauseAfterMS == nil || *got.PauseAfterMS != 300 {
		t.Fatalf("SpeechPlan metadata = %+v", got)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["turn_id"] != "turn_abc" || wire["playback_output"] != "local" || wire["pause_after_ms"] != float64(300) {
		t.Fatalf("SpeechPlan wire metadata = %#v", wire)
	}
	zeroPause, err := json.Marshal(BuildAudioChunkEventPayload(AudioChunkEventPayloadInput{TurnID: "turn_zero", PauseAfterMS: 0}))
	if err != nil {
		t.Fatal(err)
	}
	var zeroWire map[string]any
	if err := json.Unmarshal(zeroPause, &zeroWire); err != nil {
		t.Fatal(err)
	}
	if zeroWire["pause_after_ms"] != float64(0) {
		t.Fatalf("explicit zero pause was omitted: %s", zeroPause)
	}
}

func TestBuildAudioChunkEventPayloadOmitsPlaybackOutputForLegacyChunks(t *testing.T) {
	raw, err := json.Marshal(BuildAudioChunkEventPayload(AudioChunkEventPayloadInput{SpeechText: "legacy"}))
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire["playback_output"]; ok {
		t.Fatalf("legacy payload gained playback_output: %s", raw)
	}
}

func TestBuildSessionCompletedEventPayloadTrimsIdentity(t *testing.T) {
	got := BuildSessionCompletedEventPayload(SessionCompletedEventPayloadInput{
		SessionID:         " idle-1 ",
		PublicPlaybackRef: " r1 ",
		MessageID:         " m1 ",
		UtteranceID:       " u1 ",
		CharacterID:       " shiro ",
	})
	if got.SessionID != "idle-1" || got.PublicPlaybackRef != "r1" || got.MessageID != "m1" || got.CharacterID != "shiro" {
		t.Fatalf("payload = %+v", got)
	}
}

func TestPublicPlaybackPayloadsUseOnlyPublicPlaybackRefWireKey(t *testing.T) {
	chunk := BuildAudioChunkEventPayload(AudioChunkEventPayloadInput{
		SessionID:         "tts-1",
		PublicPlaybackRef: "idle-1:0001",
		ChunkIndex:        1,
		SpeechText:        "hello",
	})
	completed := BuildSessionCompletedEventPayload(SessionCompletedEventPayloadInput{
		SessionID:         "tts-1",
		PublicPlaybackRef: "idle-1:0001",
	})

	for name, payload := range map[string]any{"chunk": chunk, "completed": completed} {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("%s marshal: %v", name, err)
		}
		var wire map[string]any
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatalf("%s wire: %v", name, err)
		}
		if wire["public_playback_ref"] != "idle-1:0001" {
			t.Fatalf("%s public_playback_ref = %#v, want exact opaque ref: %s", name, wire["public_playback_ref"], raw)
		}
		if _, ok := wire["response_id"]; ok {
			t.Fatalf("%s must not emit legacy response_id: %s", name, raw)
		}
	}
}

func TestPlaybackEventRouteForSession(t *testing.T) {
	idle := PlaybackEventRouteForSession(" idle-123 ")
	if idle.Channel != EventChannelIdle || idle.SessionID != "idle-123" {
		t.Fatalf("idle route = %+v", idle)
	}
	viewer := PlaybackEventRouteForSession("normal")
	if viewer.Channel != EventChannelViewer || viewer.SessionID != EventViewerPlaybackSession {
		t.Fatalf("viewer route = %+v", viewer)
	}
}
