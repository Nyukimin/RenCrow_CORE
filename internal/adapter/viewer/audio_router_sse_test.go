package viewer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

type publishAudioAfterFlushWriter struct {
	*httptest.ResponseRecorder
	hub                *EventHub
	events             []orchestrator.OrchestratorEvent
	cancel             context.CancelFunc
	flushes            int
	cancelOnFirstFlush bool
}

func (w *publishAudioAfterFlushWriter) Flush() {
	w.ResponseRecorder.Flush()
	w.flushes++
	if w.flushes == 1 {
		for _, event := range w.events {
			w.hub.OnEvent(event)
		}
		if w.cancelOnFirstFlush {
			w.cancel()
		}
	}
}

func TestHandleAudioRouterSSEFailsClosedWithoutFilter(t *testing.T) {
	hub := NewEventHub(10)
	h := HandleAudioRouterSSE(hub)
	req := httptest.NewRequest(http.MethodGet, "/viewer/audio-router/events", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("legacy audio handler must fail closed without a filter, got 200: %s", rec.Body.String())
	}
	if hub.ClientCount() != 0 {
		t.Fatalf("missing filter subscribed to global EventHub: clients=%d", hub.ClientCount())
	}
}

func TestHandleAudioRouterSSEFiltersPublicPrivateAndUncertainSessions(t *testing.T) {
	hub := NewEventHub(10)
	privateEvent := orchestrator.OrchestratorEvent{
		Type:      "tts.audio_chunk",
		SessionID: "owner-bound-session",
		Content:   `{"character_id":"mio","audio_url":"https://private.example/audio.mp3"}`,
	}
	uncertainEvent := orchestrator.OrchestratorEvent{
		Type:    "tts.audio_chunk",
		Content: `{"character_id":"mio","audio_url":"https://uncertain.example/audio.mp3"}`,
	}
	publicEvent := orchestrator.OrchestratorEvent{
		Type:      "tts.audio_chunk",
		SessionID: "public-session",
		Content:   `{"character_id":"mio","audio_url":"https://public.example/audio.mp3"}`,
	}
	req := httptest.NewRequest(http.MethodGet, "/viewer/audio-router/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)
	accessCalls := 0
	h := HandleAudioRouterSSEWithFilter(hub, func(_ context.Context, event orchestrator.OrchestratorEvent) (bool, error) {
		accessCalls++
		if event.SessionID == "public-session" {
			cancel()
			return true, nil
		}
		return false, nil
	})
	rec := &publishAudioAfterFlushWriter{
		ResponseRecorder: httptest.NewRecorder(),
		hub:              hub,
		events:           []orchestrator.OrchestratorEvent{uncertainEvent, privateEvent, publicEvent},
		cancel:           cancel,
	}
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if accessCalls != 2 {
		t.Fatalf("filter calls = %d, want private and public sessions only; uncertain session must be denied before callback", accessCalls)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "public.example/audio.mp3") {
		t.Fatalf("public session audio missing from stream: %s", body)
	}
	for _, secret := range []string{"private.example/audio.mp3", "uncertain.example/audio.mp3"} {
		if strings.Contains(body, secret) {
			t.Fatalf("stream leaked %q: %s", secret, body)
		}
	}
}

func TestHandleAudioRouterSSEStopsOnFilterErrorWithoutLeaking(t *testing.T) {
	hub := NewEventHub(10)
	req := httptest.NewRequest(http.MethodGet, "/viewer/audio-router/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)
	h := HandleAudioRouterSSEWithFilter(hub, func(_ context.Context, _ orchestrator.OrchestratorEvent) (bool, error) {
		cancel()
		return false, context.DeadlineExceeded
	})
	privateEvent := orchestrator.OrchestratorEvent{
		Type:      "tts.audio_chunk",
		SessionID: "owner-bound-session",
		Content:   `{"character_id":"mio","audio_url":"https://private.example/audio.mp3"}`,
	}
	rec := &publishAudioAfterFlushWriter{
		ResponseRecorder: httptest.NewRecorder(),
		hub:              hub,
		events:           []orchestrator.OrchestratorEvent{privateEvent},
		cancel:           cancel,
	}
	h(rec, req)

	if strings.Contains(rec.Body.String(), "private.example/audio.mp3") {
		t.Fatalf("filter error leaked owner-bound audio: %s", rec.Body.String())
	}
}
