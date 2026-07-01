package tts

import "testing"

func TestResolvePublicChunkAssignsGlobalChunkNumber(t *testing.T) {
	route, ok := NewPublicSessionRoute(PublicSessionRouteRegistration{
		InternalSessionID: "idle-tts-a",
		PublicSessionID:   "idle-1",
		ResponseID:        "idle-1:0000",
	})
	if !ok {
		t.Fatal("route should be accepted")
	}

	first := ResolvePublicChunk(&route, "idle-tts-a", 0, 2)
	if first.SessionID != "idle-1" || first.ChunkIndex != 2 || first.NextChunkNumber != 3 || !first.Assigned {
		t.Fatalf("unexpected first chunk resolution: %+v", first)
	}
	again := ResolvePublicChunk(&route, "idle-tts-a", 0, 99)
	if again.SessionID != "idle-1" || again.ChunkIndex != 2 || again.NextChunkNumber != 99 || again.Assigned {
		t.Fatalf("existing chunk mapping should be reused: %+v", again)
	}
}

func TestResolvePublicChunkPassesThroughWithoutRoute(t *testing.T) {
	got := ResolvePublicChunk(nil, " normal ", 7, 3)
	if got.SessionID != "normal" || got.ChunkIndex != 7 || got.NextChunkNumber != 3 || got.Assigned {
		t.Fatalf("unexpected passthrough chunk resolution: %+v", got)
	}
}

func TestResolvePublicResponseIDForMessage(t *testing.T) {
	msg := ResolvePublicResponseIDForMessage("idle-align", "idle-align:msg:0007", 0)
	if msg.ResponseID != "idle-align:0007" || msg.NextResponseNumber != 8 || !msg.Advance {
		t.Fatalf("message response resolution = %+v", msg)
	}
	domain := ResolvePublicResponseIDForMessage("forecast-align", "forecast-align:domain:0000", 2)
	if domain.ResponseID != "forecast-align:domain:0000" || domain.NextResponseNumber != 2 || domain.Advance {
		t.Fatalf("domain response resolution = %+v", domain)
	}
	next := ResolvePublicResponseIDForMessage("idle-align", "idle-align:topic", 3)
	if next.ResponseID != "idle-align:0003" || next.NextResponseNumber != 4 || !next.Advance {
		t.Fatalf("fallback response resolution = %+v", next)
	}
}
