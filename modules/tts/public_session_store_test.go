package tts

import "testing"

func TestPublicSessionStoreResetsOldRoutesAsStale(t *testing.T) {
	store := NewPublicSessionStore()
	store.Register(PublicSessionRouteRegistration{InternalSessionID: "old-tts", PublicSessionID: "idle-old", PublicPlaybackRef: "idle-old:0000"})
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
	store.Register(PublicSessionRouteRegistration{InternalSessionID: "tts-a", PublicSessionID: "idle-1", PublicPlaybackRef: "idle-1:0000"})
	store.Register(PublicSessionRouteRegistration{InternalSessionID: "tts-b", PublicSessionID: "idle-1", PublicPlaybackRef: "idle-1:0001"})

	first := store.ResolveChunk("tts-a", 0)
	second := store.ResolveChunk("tts-a", 1)
	third := store.ResolveChunk("tts-b", 0)
	if first.SessionID != "idle-1" || first.ChunkIndex != 0 || second.ChunkIndex != 1 || third.ChunkIndex != 2 {
		t.Fatalf("unexpected chunk sequence: first=%+v second=%+v third=%+v", first, second, third)
	}
	if got := store.NextPublicPlaybackRef("idle-1"); got != "idle-1:0000" {
		t.Fatalf("first response = %q", got)
	}
	if got := store.NextPublicPlaybackRef("idle-1"); got != "idle-1:0001" {
		t.Fatalf("second response = %q", got)
	}
}

func TestPublicSessionStoreMarksTimedOutRoute(t *testing.T) {
	store := NewPublicSessionStore()
	store.Register(PublicSessionRouteRegistration{InternalSessionID: "tts-1", PublicSessionID: "idle-1", PublicPlaybackRef: "idle-1:0000", MessageID: "idle-1:msg:0001", TurnIndex: 1})
	store.Register(PublicSessionRouteRegistration{InternalSessionID: "tts-2", PublicSessionID: "idle-1", PublicPlaybackRef: "idle-1:0001", MessageID: "idle-1:msg:0002", TurnIndex: 2})
	matched := store.MarkTimedOut("idle-1", "idle-1:msg:0001", 1, false)
	if len(matched) != 1 || matched[0] != "tts-1" {
		t.Fatalf("unexpected matches: %#v", matched)
	}
	if !store.IsStale("tts-1") || store.IsStale("tts-2") {
		t.Fatalf("unexpected stale state tts-1=%t tts-2=%t", store.IsStale("tts-1"), store.IsStale("tts-2"))
	}
}

func TestPublicSessionStoreRetireKeepsLateCallbacksStale(t *testing.T) {
	store := NewPublicSessionStore()
	store.Register(PublicSessionRouteRegistration{InternalSessionID: "tts-retire", PublicSessionID: "idle-retire", PublicPlaybackRef: "idle-retire:0000"})

	store.Retire("tts-retire")

	if !store.IsStale("tts-retire") {
		t.Fatal("retired route must remain stale for late callbacks")
	}
	if got := store.Snapshot(); got.RouteCount != 0 || got.StaleRouteCount != 0 {
		t.Fatalf("unexpected retired snapshot: %+v", got)
	}
}

func TestPublicSessionStoreRetireByPublicPlaybackRefKeepsLateCallbacksStale(t *testing.T) {
	store := NewPublicSessionStore()
	store.Register(PublicSessionRouteRegistration{InternalSessionID: "tts-retire-response", PublicSessionID: "idle-retire", PublicPlaybackRef: "idle-retire:0001"})

	store.RetireByPublicPlaybackRef("idle-retire:0001")

	if !store.IsStale("tts-retire-response") {
		t.Fatal("response-retired route must remain stale for late callbacks")
	}
	if got := store.Snapshot(); got.RouteCount != 0 || got.StaleRouteCount != 0 {
		t.Fatalf("unexpected response-retired snapshot: %+v", got)
	}
}

func TestPublicSessionStoreCancelPlaybackGenerationPreservesNewerRoutes(t *testing.T) {
	store := NewPublicSessionStore()
	if !store.Register(PublicSessionRouteRegistration{
		InternalSessionID: "tts-old", PublicSessionID: "idle-1", PublicPlaybackRef: "idle-1:old",
		RuntimeEpoch: "runtime-a", PlaybackGeneration: 4,
	}) {
		t.Fatal("old route registration failed")
	}
	if !store.Register(PublicSessionRouteRegistration{
		InternalSessionID: "tts-new", PublicSessionID: "idle-1", PublicPlaybackRef: "idle-1:new",
		RuntimeEpoch: "runtime-a", PlaybackGeneration: 5,
	}) {
		t.Fatal("new route registration failed")
	}

	store.CancelPlaybackGeneration("runtime-a", 4)
	if !store.IsStale("tts-old") {
		t.Fatal("old route should be stale after generation cancellation")
	}
	if store.IsStale("tts-new") {
		t.Fatal("newer route should remain current")
	}
	if got := store.Snapshot(); got.RouteCount != 1 {
		t.Fatalf("current route count = %d, want 1", got.RouteCount)
	}
	if store.Register(PublicSessionRouteRegistration{
		InternalSessionID: "tts-old-late", PublicSessionID: "idle-1", PublicPlaybackRef: "idle-1:late",
		RuntimeEpoch: "runtime-a", PlaybackGeneration: 4,
	}) {
		t.Fatal("late old route registration should be rejected")
	}
	if !store.IsStale("tts-old-late") {
		t.Fatal("late old route should remain stale")
	}
}

func TestPublicSessionStoreProvidesExactCurrentRouteProof(t *testing.T) {
	store := NewPublicSessionStore()
	const (
		internalID = "tts-internal"
		publicID   = "idle-session"
		epoch      = "runtime-epoch-7"
	)
	if !store.Register(PublicSessionRouteRegistration{
		InternalSessionID: internalID, PublicSessionID: publicID,
		RuntimeEpoch: epoch, PlaybackGeneration: 12,
	}) {
		t.Fatal("current route registration failed")
	}
	proof, ok := store.CurrentRouteProof(internalID)
	if !ok {
		t.Fatal("current route proof was unavailable")
	}
	if proof.InternalSessionID != internalID || proof.PublicSessionID != publicID ||
		proof.RuntimeEpoch != epoch || proof.PlaybackGeneration != 12 ||
		proof.StoreGeneration != 0 || !proof.Current {
		t.Fatalf("unexpected route proof: %+v", proof)
	}
	if !store.ValidateCurrentRouteProof(proof) {
		t.Fatal("exact current route proof was rejected")
	}

	for name, mutate := range map[string]func(*PublicSessionRouteProof){
		"internal session":    func(p *PublicSessionRouteProof) { p.InternalSessionID = "other-internal" },
		"public session":      func(p *PublicSessionRouteProof) { p.PublicSessionID = "other-public" },
		"runtime epoch":       func(p *PublicSessionRouteProof) { p.RuntimeEpoch = "other-epoch" },
		"playback generation": func(p *PublicSessionRouteProof) { p.PlaybackGeneration++ },
		"store generation":    func(p *PublicSessionRouteProof) { p.StoreGeneration++ },
		"current flag":        func(p *PublicSessionRouteProof) { p.Current = false },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := proof
			mutate(&candidate)
			if store.ValidateCurrentRouteProof(candidate) {
				t.Fatalf("mutated proof was accepted: %+v", candidate)
			}
		})
	}
}

func TestPublicSessionStoreRouteProofExpiresOnTimeoutResetRetireOrRemoval(t *testing.T) {
	for _, tc := range []struct {
		name       string
		invalidate func(*PublicSessionStore)
	}{
		{name: "timeout", invalidate: func(s *PublicSessionStore) { s.MarkTimedOut("idle-session", "", -1, true) }},
		{name: "reset", invalidate: func(s *PublicSessionStore) { s.ResetForIdleChat() }},
		{name: "full reset", invalidate: func(s *PublicSessionStore) { s.ResetAll() }},
		{name: "retire", invalidate: func(s *PublicSessionStore) { s.Retire("tts-internal") }},
		{name: "clear", invalidate: func(s *PublicSessionStore) { s.Clear("tts-internal") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewPublicSessionStore()
			if !store.Register(PublicSessionRouteRegistration{
				InternalSessionID: "tts-internal", PublicSessionID: "idle-session",
				RuntimeEpoch: "runtime-epoch", PlaybackGeneration: 2,
			}) {
				t.Fatal("route registration failed")
			}
			proof, ok := store.CurrentRouteProof("tts-internal")
			if !ok {
				t.Fatal("route proof was unavailable")
			}
			tc.invalidate(store)
			if store.ValidateCurrentRouteProof(proof) {
				t.Fatal("invalidated route proof was accepted")
			}
			if _, ok := store.CurrentRouteProof("tts-internal"); ok {
				t.Fatal("proof was returned for a noncurrent route")
			}
		})
	}
	store := NewPublicSessionStore()
	if _, ok := store.CurrentRouteProof("never-registered"); ok {
		t.Fatal("proof was returned for an unregistered route")
	}
	for name, reg := range map[string]PublicSessionRouteRegistration{
		"non-idle public session": {
			InternalSessionID: "tts-internal", PublicSessionID: "chat-session",
			RuntimeEpoch: "runtime-epoch", PlaybackGeneration: 2,
		},
		"missing runtime epoch": {
			InternalSessionID: "tts-internal", PublicSessionID: "idle-session", PlaybackGeneration: 2,
		},
		"missing playback generation": {
			InternalSessionID: "tts-internal", PublicSessionID: "idle-session", RuntimeEpoch: "runtime-epoch",
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := NewPublicSessionStore()
			if !candidate.Register(reg) {
				t.Fatal("test route registration failed")
			}
			if _, ok := candidate.CurrentRouteProof("tts-internal"); ok {
				t.Fatal("incomplete or non-IdleChat route produced a public proof")
			}
		})
	}
}
