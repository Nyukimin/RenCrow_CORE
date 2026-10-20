package viewer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
)

func TestSpeechProgressForwardsExactActiveTurnAndIsIdempotent(t *testing.T) {
	registry := NewSpeechCancellationRegistry()
	latch := orchestrator.NewSpeechTurnCancellation()
	_, release, err := registry.RegisterTurn("root-task-1", "turn-1", latch)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	progress := speech.PlaybackProgressFromContext(orchestrator.WithSpeechTurnCancellation(context.Background(), latch))
	if err := progress.MarkReady(0); err != nil {
		t.Fatal(err)
	}
	handler := registry.HandleProgress(func(viewerClientID string) bool {
		return viewerClientID == "audio-owner"
	})

	for _, phase := range []string{"started", "started", "ended", "ended"} {
		rec := postSpeechProgress(t, handler, `{"public_playback_ref":"root-task-1","turn_id":"turn-1","utterance_index":0,"phase":"`+phase+`","viewer_client_id":"audio-owner"}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("phase=%s status=%d want=%d body=%s", phase, rec.Code, http.StatusNoContent, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("phase=%s Cache-Control=%q want=no-store", phase, got)
		}
	}
	if err := progress.WaitStarted(context.Background(), 0); err != nil {
		t.Fatalf("WaitStarted(0): %v", err)
	}
	if err := progress.WaitEnded(context.Background(), 0); err != nil {
		t.Fatalf("WaitEnded(0): %v", err)
	}
}

func TestSpeechProgressIgnoresEveryPhaseForLocalPlaybackTurn(t *testing.T) {
	registry := NewSpeechCancellationRegistry()
	latch := orchestrator.NewSpeechTurnCancellation()
	if err := latch.SetPlaybackOutput("local"); err != nil {
		t.Fatal(err)
	}
	_, release, err := registry.RegisterTurn("root-task-local", "turn-local", latch)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	progress := speech.PlaybackProgressFromContext(orchestrator.WithSpeechTurnCancellation(context.Background(), latch))
	if err := progress.MarkReady(0); err != nil {
		t.Fatal(err)
	}
	handler := registry.HandleProgress(func(string) bool { return true })
	for _, phase := range []string{"started", "ended", "error", "cancelled"} {
		rec := postSpeechProgress(t, handler, `{"public_playback_ref":"root-task-local","turn_id":"turn-local","utterance_index":0,"phase":"`+phase+`","viewer_client_id":"audio-owner"}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("phase=%s status=%d want=%d body=%s", phase, rec.Code, http.StatusNoContent, rec.Body.String())
		}
	}
	if err := progress.MarkStarted(0); err != nil {
		t.Fatalf("local turn was changed by browser progress: MarkStarted(0): %v", err)
	}
	if err := progress.MarkEnded(0); err != nil {
		t.Fatalf("local turn was changed by browser progress: MarkEnded(0): %v", err)
	}
}

func TestSpeechProgressIgnoresStaleIdentityInactiveViewerAndUnreadyIndex(t *testing.T) {
	registry := NewSpeechCancellationRegistry()
	latch := orchestrator.NewSpeechTurnCancellation()
	_, release, err := registry.RegisterTurn("root-task-2", "turn-2", latch)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	progress := speech.PlaybackProgressFromContext(orchestrator.WithSpeechTurnCancellation(context.Background(), latch))
	if err := progress.MarkReady(0); err != nil {
		t.Fatal(err)
	}
	handler := registry.HandleProgress(func(viewerClientID string) bool {
		return viewerClientID == "audio-owner"
	})
	for _, body := range []string{
		`{"public_playback_ref":"wrong-root","turn_id":"turn-2","utterance_index":0,"phase":"started","viewer_client_id":"audio-owner"}`,
		`{"public_playback_ref":"root-task-2","turn_id":"wrong-turn","utterance_index":0,"phase":"started","viewer_client_id":"audio-owner"}`,
		`{"public_playback_ref":"root-task-2","turn_id":"turn-2","utterance_index":0,"phase":"started","viewer_client_id":"inactive-viewer"}`,
		`{"public_playback_ref":"root-task-2","turn_id":"turn-2","utterance_index":1,"phase":"started","viewer_client_id":"audio-owner"}`,
		`{"public_playback_ref":"root-task-2","turn_id":"turn-2","utterance_index":0,"phase":"ended","viewer_client_id":"audio-owner"}`,
		`{"public_playback_ref":"root-task-2","turn_id":"turn-2","utterance_index":1,"phase":"error","viewer_client_id":"audio-owner"}`,
		`{"public_playback_ref":"root-task-2","turn_id":"turn-2","utterance_index":1,"phase":"cancelled","viewer_client_id":"audio-owner"}`,
	} {
		rec := postSpeechProgress(t, handler, body)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("stale progress status=%d want=%d body=%s", rec.Code, http.StatusNoContent, rec.Body.String())
		}
	}
	// Wrong identity and unready-index reports must not mutate playback state.
	if err := progress.MarkEnded(0); err == nil {
		t.Fatal("stale reports advanced the turn to ended")
	}
	good := postSpeechProgress(t, handler, `{"public_playback_ref":"root-task-2","turn_id":"turn-2","utterance_index":0,"phase":"started","viewer_client_id":"audio-owner"}`)
	if good.Code != http.StatusNoContent {
		t.Fatalf("valid progress status=%d want=%d body=%s", good.Code, http.StatusNoContent, good.Body.String())
	}
	if err := progress.WaitStarted(context.Background(), 0); err != nil {
		t.Fatalf("valid exact-turn progress did not advance state: %v", err)
	}
	if err := progress.MarkReady(1); err != nil {
		t.Fatalf("unready external error/cancellation mutated turn state: %v", err)
	}
}

func TestSpeechProgressErrorAndCancellationOnlyAffectBoundSpeechTurn(t *testing.T) {
	t.Run("error releases progress waiter", func(t *testing.T) {
		registry := NewSpeechCancellationRegistry()
		latch := orchestrator.NewSpeechTurnCancellation()
		_, release, err := registry.RegisterTurn("root-task-error", "turn-error", latch)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		progress := speech.PlaybackProgressFromContext(orchestrator.WithSpeechTurnCancellation(context.Background(), latch))
		if err := progress.MarkReady(0); err != nil {
			t.Fatal(err)
		}
		handler := registry.HandleProgress(func(string) bool { return true })
		rec := postSpeechProgress(t, handler, `{"public_playback_ref":"root-task-error","turn_id":"turn-error","utterance_index":0,"phase":"error","viewer_client_id":"audio-owner"}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusNoContent, rec.Body.String())
		}
		if err := progress.WaitStarted(context.Background(), 0); err == nil {
			t.Fatal("playback error did not release waiter with a failure")
		}
	})

	t.Run("cancelled report cancels speech child only", func(t *testing.T) {
		registry := NewSpeechCancellationRegistry()
		latch := orchestrator.NewSpeechTurnCancellation()
		_, release, err := registry.RegisterTurn("root-task-cancel", "turn-cancel", latch)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		turnCtx := orchestrator.WithSpeechTurnCancellation(context.Background(), latch)
		speechCtx, done := orchestrator.SpeechSynthesisContext(turnCtx)
		defer done()
		progress := speech.PlaybackProgressFromContext(turnCtx)
		if err := progress.MarkReady(0); err != nil {
			t.Fatal(err)
		}
		handler := registry.HandleProgress(func(string) bool { return true })
		rec := postSpeechProgress(t, handler, `{"public_playback_ref":"root-task-cancel","turn_id":"turn-cancel","utterance_index":0,"phase":"cancelled","viewer_client_id":"audio-owner"}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusNoContent, rec.Body.String())
		}
		if !errors.Is(speechCtx.Err(), context.Canceled) {
			t.Fatalf("speech context error=%v want=%v", speechCtx.Err(), context.Canceled)
		}
		if turnCtx.Err() != nil {
			t.Fatalf("turn context was canceled: %v", turnCtx.Err())
		}
	})
}

func TestSpeechProgressRequestContract(t *testing.T) {
	registry := NewSpeechCancellationRegistry()
	handler := registry.HandleProgress(func(string) bool { return true })
	for _, tt := range []struct {
		name   string
		method string
		body   string
		want   int
	}{
		{name: "wrong method", method: http.MethodGet, body: `{}`, want: http.StatusMethodNotAllowed},
		{name: "invalid json", method: http.MethodPost, body: `{`, want: http.StatusBadRequest},
		{name: "unknown phase", method: http.MethodPost, body: `{"public_playback_ref":"r","turn_id":"t","utterance_index":0,"phase":"ready","viewer_client_id":"v"}`, want: http.StatusBadRequest},
		{name: "phase whitespace", method: http.MethodPost, body: `{"public_playback_ref":"r","turn_id":"t","utterance_index":0,"phase":" started ","viewer_client_id":"v"}`, want: http.StatusBadRequest},
		{name: "missing required field", method: http.MethodPost, body: `{"public_playback_ref":"r","turn_id":"t","phase":"started","viewer_client_id":"v"}`, want: http.StatusBadRequest},
		{name: "negative index", method: http.MethodPost, body: `{"public_playback_ref":"r","turn_id":"t","utterance_index":-1,"phase":"started","viewer_client_id":"v"}`, want: http.StatusBadRequest},
		{name: "index outside plan", method: http.MethodPost, body: `{"public_playback_ref":"r","turn_id":"t","utterance_index":6,"phase":"started","viewer_client_id":"v"}`, want: http.StatusBadRequest},
		{name: "unknown field", method: http.MethodPost, body: `{"public_playback_ref":"r","turn_id":"t","utterance_index":0,"phase":"started","viewer_client_id":"v","receipt":true}`, want: http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, "/viewer/tts/progress", strings.NewReader(tt.body))
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tt.want, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control=%q want=no-store", got)
			}
		})
	}
}

func TestSpeechProgressIgnoresExpiredTurn(t *testing.T) {
	registry := NewSpeechCancellationRegistry()
	latch := orchestrator.NewSpeechTurnCancellation()
	_, release, err := registry.RegisterTurn("root-task-expired", "turn-expired", latch)
	if err != nil {
		t.Fatal(err)
	}
	release()
	progress := speech.PlaybackProgressFromContext(orchestrator.WithSpeechTurnCancellation(context.Background(), latch))
	if err := progress.MarkReady(0); err != nil {
		t.Fatal(err)
	}
	handler := registry.HandleProgress(func(string) bool { return true })
	rec := postSpeechProgress(t, handler, `{"public_playback_ref":"root-task-expired","turn_id":"turn-expired","utterance_index":0,"phase":"started","viewer_client_id":"audio-owner"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if err := progress.MarkEnded(0); err == nil {
		t.Fatal("expired turn was advanced by stale progress")
	}
}

func postSpeechProgress(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/viewer/tts/progress", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
