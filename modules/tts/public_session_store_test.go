package tts

import "testing"

func TestPublicSessionStoreResetsOldRoutesAsStale(t *testing.T) {
	store := NewPublicSessionStore()
	store.Register(PublicSessionRouteRegistration{InternalSessionID: "old-tts", PublicSessionID: "idle-old", ResponseID: "idle-old:0000"})
	if store.IsStale("old-tts") {
		t.Fatal("new route should not be stale")
	}
	store.ResetForIdleChat()
	if !store.IsStale("old-tts") {
		t.Fatal("old route should be stale after reset")
	}
	if got := store.Snapshot(); got.RouteCount != 0 || got.StaleRouteCount != 0 {
		t.Fatalf("stale reset route should not stay in snapshot: %+v", got)
	}
}

func TestPublicSessionStoreResolvesChunkAndResponseSequences(t *testing.T) {
	store := NewPublicSessionStore()
	store.Register(PublicSessionRouteRegistration{InternalSessionID: "tts-a", PublicSessionID: "idle-1", ResponseID: "idle-1:0000"})
	store.Register(PublicSessionRouteRegistration{InternalSessionID: "tts-b", PublicSessionID: "idle-1", ResponseID: "idle-1:0001"})

	first := store.ResolveChunk("tts-a", 0)
	second := store.ResolveChunk("tts-a", 1)
	third := store.ResolveChunk("tts-b", 0)
	if first.SessionID != "idle-1" || first.ChunkIndex != 0 || second.ChunkIndex != 1 || third.ChunkIndex != 2 {
		t.Fatalf("unexpected chunk sequence: first=%+v second=%+v third=%+v", first, second, third)
	}
	if got := store.NextResponseID("idle-1"); got != "idle-1:0000" {
		t.Fatalf("first response = %q", got)
	}
	if got := store.NextResponseID("idle-1"); got != "idle-1:0001" {
		t.Fatalf("second response = %q", got)
	}
}

func TestPublicSessionStoreMarksTimedOutRoute(t *testing.T) {
	store := NewPublicSessionStore()
	store.Register(PublicSessionRouteRegistration{InternalSessionID: "tts-1", PublicSessionID: "idle-1", ResponseID: "idle-1:0000", MessageID: "idle-1:msg:0001", TurnIndex: 1})
	store.Register(PublicSessionRouteRegistration{InternalSessionID: "tts-2", PublicSessionID: "idle-1", ResponseID: "idle-1:0001", MessageID: "idle-1:msg:0002", TurnIndex: 2})
	matched := store.MarkTimedOut("idle-1", "idle-1:msg:0001", 1, false)
	if len(matched) != 1 || matched[0] != "tts-1" {
		t.Fatalf("unexpected matches: %#v", matched)
	}
	if !store.IsStale("tts-1") || store.IsStale("tts-2") {
		t.Fatalf("unexpected stale state tts-1=%t tts-2=%t", store.IsStale("tts-1"), store.IsStale("tts-2"))
	}
}
