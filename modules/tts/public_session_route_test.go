package tts

import "testing"

func TestNewPublicSessionRoute(t *testing.T) {
	got, ok := NewPublicSessionRoute(PublicSessionRouteRegistration{
		InternalSessionID: " internal-tts ",
		PublicSessionID:   " idle-session ",
		ResponseID:        " idle-session:0001 ",
		MessageID:         " idle-session:msg:0001 ",
		TurnIndex:         1,
		Generation:        2,
	})
	if !ok {
		t.Fatal("route should be accepted")
	}
	if got.PublicSessionID != "idle-session" || got.ResponseID != "idle-session:0001" || got.MessageID != "idle-session:msg:0001" {
		t.Fatalf("route was not normalized: %+v", got)
	}
	if got.UtteranceID != "idle-session:msg:0001:utt:0000" || got.Generation != 2 || got.ChunkIndexes == nil {
		t.Fatalf("route defaults were not built: %+v", got)
	}
}

func TestNewPublicSessionRouteRejectsInvalidIDs(t *testing.T) {
	if _, ok := NewPublicSessionRoute(PublicSessionRouteRegistration{InternalSessionID: "same", PublicSessionID: "same"}); ok {
		t.Fatal("same internal/public session should be rejected")
	}
	if _, ok := NewPublicSessionRoute(PublicSessionRouteRegistration{InternalSessionID: "", PublicSessionID: "public"}); ok {
		t.Fatal("empty internal session should be rejected")
	}
}

func TestPublicSessionRouteMatchesTimeout(t *testing.T) {
	route, ok := NewPublicSessionRoute(PublicSessionRouteRegistration{
		InternalSessionID: "tts-1",
		PublicSessionID:   "idle-1",
		ResponseID:        "idle-1:0001",
		MessageID:         "idle-1:msg:0001",
		TurnIndex:         1,
	})
	if !ok {
		t.Fatal("route should be accepted")
	}
	if !route.MatchesTimeout("idle-1", "idle-1:msg:0001", -1, false) {
		t.Fatal("message timeout should match")
	}
	if !route.MatchesTimeout("idle-1", "", 1, false) {
		t.Fatal("turn timeout should match")
	}
	if route.MatchesTimeout("idle-1", "", -1, false) {
		t.Fatal("non-specific timeout should not match without allForSession")
	}
	if !route.MatchesTimeout("idle-1", "", -1, true) {
		t.Fatal("session timeout should match")
	}
}
