package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
	moduletts "github.com/Nyukimin/picoclaw_multiLLM/modules/tts"
)

type fakeTTSPlaybackObserver struct{}

func (fakeTTSPlaybackObserver) Health(context.Context) core.HealthReport {
	return core.HealthReport{Module: "tts.playback", Status: core.HealthLive, Ready: true}
}

func (fakeTTSPlaybackObserver) Snapshot(context.Context) (moduletts.PlaybackStateSnapshot, error) {
	return moduletts.PlaybackStateSnapshot{
		PendingSessionCount:  1,
		PendingResponseCount: 1,
		PublicRouteCount:     2,
	}, nil
}

func TestHandleModuleTTSPlaybackState(t *testing.T) {
	handler := handleModuleTTSPlaybackState(fakeTTSPlaybackObserver{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/modules/tts/playback-state", nil)
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var got moduletts.PlaybackStateReport
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got.Health.Module != "tts.playback" || got.Snapshot.PendingSessionCount != 1 || got.Snapshot.PublicRouteCount != 2 {
		t.Fatalf("unexpected response: %+v", got)
	}
	if got.Health.CheckedAt.IsZero() {
		t.Fatalf("health checked_at was not set: %+v", got.Health)
	}
}

func TestCollectTTSPlaybackStateSnapshot(t *testing.T) {
	clearAllIdleChatTTSPending()
	resetTTSPublicSessionRoutesForIdleChat()
	registerTTSPublicSessionWithMessage("internal-tts-1", "idle-session-1", "idle-session-1:0000", "idle-session-1:msg:0000", 0)
	registerIdleChatTTSPending("internal-tts-1", "idle-session-1:0000")
	t.Cleanup(func() {
		clearAllIdleChatTTSPending()
		resetTTSPublicSessionRoutesForIdleChat()
	})

	got := collectTTSPlaybackStateSnapshot()
	if got.PendingSessionCount != 1 || got.PendingResponseCount != 1 {
		t.Fatalf("pending state was not captured: %+v", got)
	}
	if got.PublicRouteCount != 1 {
		t.Fatalf("public session state was not captured: %+v", got)
	}
}
