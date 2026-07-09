package viewer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

func TestHandleSSE_UsesLastEventIDForHistoryReplay(t *testing.T) {
	hub := NewEventHub(10)
	hub.OnEvent(orchestrator.NewEvent("entry.stage", "chrome", "system", "received", "CHAT", "j1", "s1", "local", "u1"))
	hub.OnEvent(orchestrator.NewEvent("entry.stage", "chrome", "system", "planning", "CHAT", "j1", "s1", "local", "u1"))

	req := httptest.NewRequest(http.MethodGet, "/viewer/events", nil)
	req.Header.Set("Last-Event-ID", "1")
	rec := httptest.NewRecorder()

	ctx := req.Context()
	ctx, cancel := context.WithCancel(ctx)
	cancel() // history送信後に終了
	req = req.WithContext(ctx)

	hub.HandleSSE(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, `"seq":1`) {
		t.Fatalf("expected seq=1 to be skipped, got: %s", body)
	}
	if !strings.Contains(body, `"seq":2`) {
		t.Fatalf("expected seq=2 in replay, got: %s", body)
	}
}

func TestHandleSSE_SendsHeartbeatComment(t *testing.T) {
	prev := sseHeartbeatInterval
	sseHeartbeatInterval = time.Millisecond
	defer func() { sseHeartbeatInterval = prev }()

	hub := NewEventHub(10)
	req := httptest.NewRequest(http.MethodGet, "/viewer/events", nil)
	rec := httptest.NewRecorder()

	ctx, cancel := context.WithCancel(req.Context())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	req = req.WithContext(ctx)

	hub.HandleSSE(rec, req)
	if !strings.Contains(rec.Body.String(), ": heartbeat") {
		t.Fatalf("expected SSE heartbeat comment, got: %s", rec.Body.String())
	}
}

func TestEventHubReportsClientCountChanges(t *testing.T) {
	hub := NewEventHub(10)
	var counts []int
	hub.SetClientCountListener(func(count int) {
		counts = append(counts, count)
	})

	first := hub.Subscribe()
	second := hub.Subscribe()
	if got := hub.ClientCount(); got != 2 {
		t.Fatalf("client count = %d, want 2", got)
	}
	hub.Unsubscribe(first)
	hub.Unsubscribe(second)
	if got := hub.ClientCount(); got != 0 {
		t.Fatalf("client count = %d, want 0", got)
	}
	if got, want := strings.Join(intsToStrings(counts), ","), "1,2,1,0"; got != want {
		t.Fatalf("client count notifications = %s, want %s", got, want)
	}
}

func TestHandleSSE_DoesNotReplayTransientTTSAudioHistory(t *testing.T) {
	hub := NewEventHub(10)
	hub.OnEvent(orchestrator.NewEvent("tts.audio_chunk", "tts", "user", `{"session_id":"s1","chunk_index":0,"character_id":"mio","audio_url":"http://example/audio.wav"}`, "TTS", "", "s1", "viewer", "viewer-user"))
	hub.OnEvent(orchestrator.NewEvent("tts.session_completed", "tts", "user", `{"session_id":"s1","character_id":"mio"}`, "TTS", "", "s1", "viewer", "viewer-user"))
	hub.OnEvent(orchestrator.NewEvent("agent.response", "mio", "user", "visible response", "CHAT", "j1", "s1", "viewer", "viewer-user"))

	req := httptest.NewRequest(http.MethodGet, "/viewer/events", nil)
	rec := httptest.NewRecorder()

	ctx := req.Context()
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	req = req.WithContext(ctx)

	hub.HandleSSE(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, "tts.audio_chunk") || strings.Contains(body, "tts.session_completed") {
		t.Fatalf("transient TTS audio events must not be replayed from history: %s", body)
	}
	if !strings.Contains(body, "visible response") {
		t.Fatalf("non-transient history should still replay, got: %s", body)
	}
}

func intsToStrings(values []int) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strconv.Itoa(value))
	}
	return out
}

func TestHandleSSE_DoesNotReplayIdleChatLiveHistory(t *testing.T) {
	hub := NewEventHub(10)
	hub.OnEvent(orchestrator.NewEvent("idlechat.message", "mio", "shiro", "old idle speech", "IDLECHAT", "", "idle-old", "idlechat", "idle-old"))
	hub.OnEvent(orchestrator.NewEvent("idlechat.summary", "shiro", "user", "old idle summary", "IDLECHAT", "", "idle-old", "idlechat", "idle-old"))
	hub.OnEvent(orchestrator.NewEvent("agent.response", "mio", "user", "visible response", "CHAT", "j1", "s1", "viewer", "viewer-user"))

	req := httptest.NewRequest(http.MethodGet, "/viewer/events", nil)
	rec := httptest.NewRecorder()

	ctx := req.Context()
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	req = req.WithContext(ctx)

	hub.HandleSSE(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, "old idle speech") || strings.Contains(body, "old idle summary") {
		t.Fatalf("idlechat live events must not be replayed from SSE history: %s", body)
	}
	if !strings.Contains(body, "visible response") {
		t.Fatalf("non-idle history should still replay, got: %s", body)
	}
}

func TestHandleAudioRouterSSE_FiltersAndStreamsOnlyLiveAudioChunks(t *testing.T) {
	hub := NewEventHub(10)
	hub.OnEvent(orchestrator.NewEvent("entry.stage", "chrome", "system", "received", "CHAT", "j1", "s1", "local", "u1"))
	hub.OnEvent(orchestrator.NewEvent("tts.audio_chunk", "tts", "user", `{"session_id":"s1","chunk_index":0,"character_id":"mio","audio_url":"http://example/audio.wav"}`, "TTS", "", "s1", "viewer", "viewer-user"))

	req := httptest.NewRequest(http.MethodGet, "/audio-router/events", nil)
	req.Header.Set("Last-Event-ID", "0")
	rec := httptest.NewRecorder()

	ctx := req.Context()
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	req = req.WithContext(ctx)

	HandleAudioRouterSSE(hub)(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, `"eventType":"entry.stage"`) || strings.Contains(body, "received") {
		t.Fatalf("unexpected non-audio event in stream: %s", body)
	}
	if strings.Contains(body, `"character_id":"mio"`) || strings.Contains(body, "event: tts.audio_chunk") {
		t.Fatalf("historical audio chunks must not replay into audio router stream: %s", body)
	}
}
