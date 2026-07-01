package tts

import "testing"

func TestBuildPlaybackTimeoutConsumptionTrimsAndCountsMatches(t *testing.T) {
	got := BuildPlaybackTimeoutConsumption(PlaybackTimeoutInput{
		Kind:      " timeout ",
		SessionID: " idle-timeout ",
		MessageID: " idle-timeout:msg:0001 ",
		TurnIndex: 1,
	}, []string{" tts-1 ", "", "tts-2"})

	if got.Kind != "timeout" ||
		got.SessionID != "idle-timeout" ||
		got.MessageID != "idle-timeout:msg:0001" ||
		got.TurnIndex != 1 {
		t.Fatalf("unexpected timeout consumption fields: %+v", got)
	}
	if got.AllForSession {
		t.Fatal("utterance timeout should not consume all for session")
	}
	if got.MatchedCount != 2 || len(got.MatchedInternalSessionIDs) != 2 {
		t.Fatalf("unexpected matches: %+v", got)
	}
}

func TestBuildPlaybackTimeoutConsumptionDetectsSessionAudioTimeout(t *testing.T) {
	got := BuildPlaybackTimeoutConsumption(PlaybackTimeoutInput{
		Kind:           PlaybackTimeoutKindSessionAudio,
		SessionID:      "idle-drain",
		RemainingIndex: 1,
		RemainingCount: 2,
	}, []string{"tts-1"})

	if !got.AllForSession {
		t.Fatal("session audio timeout should consume all for session")
	}
	if got.RemainingIndex != 1 || got.RemainingCount != 2 || got.MatchedCount != 1 {
		t.Fatalf("unexpected session timeout consumption: %+v", got)
	}
}

func TestPublicSessionStoreMarkTimeoutReturnsConsumption(t *testing.T) {
	store := NewPublicSessionStore()
	store.Register(PublicSessionRouteRegistration{
		InternalSessionID: "tts-1",
		PublicSessionID:   "idle-timeout",
		ResponseID:        "idle-timeout:0000",
		MessageID:         "idle-timeout:msg:0001",
		TurnIndex:         1,
	})
	store.Register(PublicSessionRouteRegistration{
		InternalSessionID: "tts-2",
		PublicSessionID:   "idle-timeout",
		ResponseID:        "idle-timeout:0001",
		MessageID:         "idle-timeout:msg:0002",
		TurnIndex:         2,
	})

	got := store.MarkTimeout(PlaybackTimeoutInput{
		Kind:      "timeout",
		SessionID: "idle-timeout",
		MessageID: "idle-timeout:msg:0001",
		TurnIndex: 1,
	})
	if got.MatchedCount != 1 || len(got.MatchedInternalSessionIDs) != 1 || got.MatchedInternalSessionIDs[0] != "tts-1" {
		t.Fatalf("unexpected timeout consumption: %+v", got)
	}
	if !store.IsStale("tts-1") {
		t.Fatal("matched timeout route should be stale")
	}
	if store.IsStale("tts-2") {
		t.Fatal("unmatched timeout route should remain current")
	}
}
